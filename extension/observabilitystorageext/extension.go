// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/hybrid"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/postgresql"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/registry"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/custom/extension/storageext"
	"go.opentelemetry.io/collector/custom/identity"
	"go.opentelemetry.io/collector/custom/taskengine"
)

// internalProvider is the internal interface that both ES and PG providers implement.
// It decouples the extension from specific provider implementations.
type internalProvider interface {
	Name() string
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	HealthCheck(ctx context.Context) (bool, string, map[string]any)

	// WriteSpans writes pre-converted spans in the canonical StoredSpan format.
	WriteSpans(ctx context.Context, spans []StoredSpan) error
	WriteTraces(ctx context.Context, td ptrace.Traces) error // deprecated, use WriteSpans
	WriteMetrics(ctx context.Context, md pmetric.Metrics) error
	WriteLogs(ctx context.Context, ld plog.Logs) error

	FlushTraces(ctx context.Context) error
	FlushMetrics(ctx context.Context) error
	FlushLogs(ctx context.Context) error
}

// ObservabilityStorage is the extension that manages the observability data storage provider.
// It holds a provider instance and exposes Writer/Admin interfaces to other components.
type ObservabilityStorage struct {
	config        *Config
	logger        *zap.Logger
	meterProvider metric.MeterProvider
	provider      internalProvider

	// Concrete providers (only one is non-nil based on config.Type)
	esProvider     *elasticsearch.Provider
	pgProvider     *postgresql.Provider
	hybridProvider *hybrid.Provider
	// factoryAccessors holds the registry factory's adapter-assembled readers
	// (metric/trace/log/admin) for the active non-hybrid provider.
	factoryAccessors *accessors

	// Lifecycle management
	scheduler      *lifecycle.LifecycleScheduler
	retentionStore lifecycle.RetentionStore

	// resolvedNodeID is the effective node identity, resolved once at Start via
	// identity.ResolveUniqueNodeID. All distributed-coordination components
	// (rollup lock/watermark, leader elector, scheduler) share this value so the
	// node_id is meaningful (POD_NAME/hostname) rather than a random nanosecond.
	resolvedNodeID string

	// Rollup engine (5m metric downsampling). Only non-nil when enabled and
	// running against the Elasticsearch provider.
	rollupEngine *elasticsearch.RollupEngine
}

// Ensure the extension implements the required interfaces.
var (
	_ extension.Extension             = (*ObservabilityStorage)(nil)
	_ extensioncapabilities.Dependent = (*ObservabilityStorage)(nil)
)

// newObservabilityStorageExtension creates a new instance of the extension.
func newObservabilityStorageExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*ObservabilityStorage, error) {
	return &ObservabilityStorage{
		config:        config,
		logger:        set.Logger,
		meterProvider: set.TelemetrySettings.MeterProvider,
	}, nil
}

// Dependencies implements extensioncapabilities.Dependent.
// This ensures that the controlplane and storage extensions are fully started
// before this extension, so that GetTaskEngine() and GetRedis() are available.
func (e *ObservabilityStorage) Dependencies() []component.ID {
	var deps []component.ID
	if e.config.Scheduler.Enabled && e.config.Scheduler.ControlplaneExtension != "" {
		deps = append(deps, component.MustNewID(e.config.Scheduler.ControlplaneExtension))
	}
	if e.config.Scheduler.Enabled && e.config.Scheduler.StorageExtension != "" {
		deps = append(deps, component.MustNewID(e.config.Scheduler.StorageExtension))
	}
	return deps
}

// Start initializes the storage provider and its backend connections.
func (e *ObservabilityStorage) Start(ctx context.Context, host component.Host) error {
	e.logger.Info("Starting observability storage extension",
		zap.String("provider_type", e.config.Type),
	)

	provider, err := e.createProvider()
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("failed to start provider: %w", err)
	}

	// Hybrid sub-providers (esProvider/pgProvider) are only populated by the
	// hybrid provider's Start() — the concrete references are nil until then.
	// Capture them AFTER Start so getHybridTraceReader/LogReader/StorageAdmin
	// (which route trace/log/admin to ES) resolve a non-nil provider instead of
	// returning nil. Without this, metric→VM leaves trace/log reader wiring
	// pointing at a pre-Start nil snapshot and every trace/log query breaks.
	if e.config.Type == "hybrid" {
		e.esProvider = e.hybridProvider.ESProvider()
		e.pgProvider = e.hybridProvider.PGProvider()
	}

	e.provider = provider
	e.logger.Info("Observability storage extension started successfully",
		zap.String("provider", provider.Name()),
	)

	// Resolve the node identity once, before any distributed-coordination
	// component (rollup lock/watermark, leader elector, scheduler) is built, so
	// they all share a meaningful, unique node_id (POD_NAME/hostname in k8s,
	// random suffix elsewhere) instead of independent nanosecond timestamps.
	e.resolvedNodeID = identity.ResolveUniqueNodeID(e.config.Scheduler.NodeID)
	e.logger.Info("Resolved node identity", zap.String("node_id", e.resolvedNodeID))

	// Start lifecycle scheduler if enabled
	if e.config.Scheduler.Enabled {
		e.scheduler = e.buildLifecycleScheduler(host)
		e.scheduler.Start(ctx)
		e.logger.Info("Lifecycle scheduler started")
	}

	// Start metric rollup engine if enabled (ES metric backend only).
	// The 5m rollup engine aggregates raw ES metric indices; when the metric
	// signal is routed to VictoriaMetrics (hybrid.metric != "elasticsearch"),
	// there is nothing for it to do — and worse, it would read the entire
	// historical ES metric corpus on startup and OOM the process (observed
	// live: restart → rollup backfill → 39MiB→695MiB→OOMKilled). Gate it on
	// the metric backend, not merely the presence of an ES sub-provider (which
	// is always present in hybrid mode because trace/log route to ES).
	if e.config.Scheduler.RollupEnabled && e.metricBackendIsES() {
		if engine := e.buildRollupEngine(host); engine != nil {
			engine.Start(ctx)
			e.rollupEngine = engine
			e.logger.Info("Metric rollup engine started")
		}
	}

	return nil
}

// metricBackendIsES reports whether the metric signal is stored in
// Elasticsearch — the only backend the 5m rollup engine understands. In
// non-hybrid ES mode this is trivially true; in hybrid mode it consults the
// metric routing.
func (e *ObservabilityStorage) metricBackendIsES() bool {
	if e.config.Type != "hybrid" {
		return e.config.Type == storedmodel.BackendES
	}
	if e.hybridProvider == nil {
		return false
	}
	return e.hybridProvider.MetricBackend() == storedmodel.BackendES
}

// Shutdown gracefully stops the storage provider.
func (e *ObservabilityStorage) Shutdown(ctx context.Context) error {
	// Stop metric rollup engine first (it depends on provider + Redis).
	if e.rollupEngine != nil {
		e.rollupEngine.Stop()
		e.logger.Info("Metric rollup engine stopped")
	}

	// Stop lifecycle scheduler first (it depends on provider)
	if e.scheduler != nil {
		e.scheduler.Stop()
		e.logger.Info("Lifecycle scheduler stopped")
	}

	if e.provider == nil {
		return nil
	}
	e.logger.Info("Shutting down observability storage extension")
	return e.provider.Shutdown(ctx)
}

// WriteTraces converts OTLP traces to StoredSpan format and writes through the provider.
func (e *ObservabilityStorage) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	if e.provider == nil {
		return fmt.Errorf("provider not initialized")
	}

	// Convert all spans to canonical format at the extension layer.
	spans := convertOTLPTraces(td)
	if len(spans) == 0 {
		return nil
	}
	return e.provider.WriteSpans(ctx, spans)
}

// WriteMetrics writes metric data through the provider.
func (e *ObservabilityStorage) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	if e.provider == nil {
		return fmt.Errorf("provider not initialized")
	}
	return e.provider.WriteMetrics(ctx, md)
}

// WriteLogs writes log data through the provider.
func (e *ObservabilityStorage) WriteLogs(ctx context.Context, ld plog.Logs) error {
	if e.provider == nil {
		return fmt.Errorf("provider not initialized")
	}
	return e.provider.WriteLogs(ctx, ld)
}

// FlushTraces flushes buffered trace data.
func (e *ObservabilityStorage) FlushTraces(ctx context.Context) error {
	if e.provider == nil {
		return nil
	}
	return e.provider.FlushTraces(ctx)
}

// FlushMetrics flushes buffered metric data.
func (e *ObservabilityStorage) FlushMetrics(ctx context.Context) error {
	if e.provider == nil {
		return nil
	}
	return e.provider.FlushMetrics(ctx)
}

// FlushLogs flushes buffered log data.
func (e *ObservabilityStorage) FlushLogs(ctx context.Context) error {
	if e.provider == nil {
		return nil
	}
	return e.provider.FlushLogs(ctx)
}

// HealthCheck verifies the backend connectivity.
func (e *ObservabilityStorage) HealthCheck(ctx context.Context) (*HealthStatus, error) {
	if e.provider == nil {
		return &HealthStatus{Healthy: false, Message: "provider not initialized"}, nil
	}
	healthy, msg, details := e.provider.HealthCheck(ctx)
	return &HealthStatus{
		Healthy: healthy,
		Message: msg,
		Details: details,
	}, nil
}

// GetProvider returns the underlying ES provider for admin operations.
// Deprecated: Use GetStorageAdmin/GetTraceReader/etc. instead.
func (e *ObservabilityStorage) GetProvider() *elasticsearch.Provider {
	return e.esProvider
}

// GetStorageAdmin returns the StorageAdmin interface.
func (e *ObservabilityStorage) GetStorageAdmin() StorageAdmin {
	if e.config.Type == "hybrid" {
		return e.getHybridStorageAdmin()
	}
	if e.factoryAccessors != nil {
		return e.factoryAccessors.storageAdmin
	}
	return nil
}

// GetTraceReader returns the TraceReader interface.
func (e *ObservabilityStorage) GetTraceReader() TraceReader {
	if e.config.Type == "hybrid" {
		return e.getHybridTraceReader()
	}
	if e.factoryAccessors != nil {
		return e.factoryAccessors.traceReader
	}
	return nil
}

// GetMetricReader returns the MetricReader interface.
func (e *ObservabilityStorage) GetMetricReader() MetricReader {
	if e.config.Type == "hybrid" {
		return e.getHybridMetricReader()
	}
	if e.factoryAccessors != nil {
		return e.factoryAccessors.metricReader
	}
	return nil
}

// GetLogReader returns the LogReader interface.
func (e *ObservabilityStorage) GetLogReader() LogReader {
	if e.config.Type == "hybrid" {
		return e.getHybridLogReader()
	}
	if e.factoryAccessors != nil {
		return e.factoryAccessors.logReader
	}
	return nil
}

// createProvider creates the appropriate provider based on configuration.
func (e *ObservabilityStorage) createProvider() (internalProvider, error) {
	if e.config.Type == "hybrid" {
		hybridCfg := e.convertHybridConfig()
		p, err := hybrid.NewProvider(hybridCfg, e.logger)
		if err != nil {
			return nil, err
		}
		e.hybridProvider = p
		return p, nil
	}

	ensureFactoriesRegistered.Do(registerAllFactories)
	f, err := registry.Get(e.config.Type)
	if err != nil {
		return nil, err
	}
	var providerCfg any
	switch e.config.Type {
	case storedmodel.BackendES:
		providerCfg = e.convertESConfig()
	case storedmodel.BackendPG:
		providerCfg = e.convertPGConfig()
	default:
		providerCfg = nil
	}
	lp, err := f.Create(providerCfg, registry.FactoryContext{
		Logger:         e.logger,
		ExtensionCfg:   e.config,
		RetentionStore: e.retentionStore,
	})
	if err != nil {
		return nil, err
	}
	p, ok := lp.(internalProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q does not implement the internal lifecycle interface", e.config.Type)
	}
	// Capture per-provider references the rest of the extension still uses
	// (rollup wiring, lifecycle scheduler, deprecated GetProvider).
	switch t := lp.(type) {
	case *elasticsearch.Provider:
		e.esProvider = t
	case *postgresql.Provider:
		e.pgProvider = t
	}
	// Factories that host public-interface accessors expose them here.
	if ap, ok := f.(accessorProvider); ok {
		if acc := ap.Accessors(); acc != nil {
			e.factoryAccessors = acc
		}
	}
	return p, nil
}

// convertESConfig converts the extension config to ES provider's internal config.
func (e *ObservabilityStorage) convertESConfig() *elasticsearch.Config {
	src := e.config.Elasticsearch
	return &elasticsearch.Config{
		Addresses:     src.Addresses,
		Username:      src.Username,
		Password:      src.Password,
		BatchSize:     src.BatchSize,
		FlushInterval: src.FlushInterval,
		MaxRetries:    src.MaxRetries,
		Traces: elasticsearch.IndexConfig{
			IndexPrefix:     src.Traces.IndexPrefix,
			IndexDateFormat: src.Traces.IndexDateFormat,
			Shards:          src.Traces.Shards,
			Replicas:        src.Traces.Replicas,
			Retention:       src.Traces.Retention,
			RefreshInterval: src.Traces.RefreshInterval,
		},
		Metrics: elasticsearch.IndexConfig{
			IndexPrefix:     src.Metrics.IndexPrefix,
			IndexDateFormat: src.Metrics.IndexDateFormat,
			Shards:          src.Metrics.Shards,
			Replicas:        src.Metrics.Replicas,
			Retention:       src.Metrics.Retention,
			RefreshInterval: src.Metrics.RefreshInterval,
		},
		Logs: elasticsearch.IndexConfig{
			IndexPrefix:     src.Logs.IndexPrefix,
			IndexDateFormat: src.Logs.IndexDateFormat,
			Shards:          src.Logs.Shards,
			Replicas:        src.Logs.Replicas,
			Retention:       src.Logs.Retention,
			RefreshInterval: src.Logs.RefreshInterval,
		},
		RollupEnabled:      e.config.Scheduler.RollupEnabled,
		RollupReadyAfter:   e.config.Scheduler.RollupReadyAfter,
		RollupTickInterval: e.config.Scheduler.Interval,
	}
}

// convertPGConfig converts the extension config to PG provider's internal config.
func (e *ObservabilityStorage) convertPGConfig() *postgresql.Config {
	src := e.config.PostgreSQL
	return &postgresql.Config{
		DSN:             src.DSN,
		MaxConns:        src.MaxConns,
		MinConns:        src.MinConns,
		MaxConnLifetime: src.MaxConnLifetime,
		MaxConnIdleTime: src.MaxConnIdleTime,
		BatchSize:       src.BatchSize,
		FlushInterval:   src.FlushInterval,
		MaxRetries:      src.MaxRetries,
		UseTimescaleDB:  src.UseTimescaleDB,
		Traces: postgresql.TableConfig{
			TableName:         src.Traces.TableName,
			Retention:         src.Traces.Retention,
			PartitionInterval: src.Traces.PartitionInterval,
		},
		Metrics: postgresql.TableConfig{
			TableName:         src.Metrics.TableName,
			Retention:         src.Metrics.Retention,
			PartitionInterval: src.Metrics.PartitionInterval,
		},
		Logs: postgresql.TableConfig{
			TableName:         src.Logs.TableName,
			Retention:         src.Logs.Retention,
			PartitionInterval: src.Logs.PartitionInterval,
		},
	}
}

// convertHybridConfig converts the extension config to Hybrid provider's internal config.
func (e *ObservabilityStorage) convertHybridConfig() *hybrid.Config {
	src := e.config.Hybrid
	cfg := &hybrid.Config{
		Trace:  src.Trace,
		Metric: src.Metric,
		Log:    src.Log,
		Admin:  src.Admin,
	}

	// Attach sub-provider configs only if present
	if e.config.Elasticsearch != nil {
		cfg.ES = e.convertESConfig()
	}
	if e.config.PostgreSQL != nil {
		cfg.PG = e.convertPGConfig()
	}
	if e.config.VictoriaMetrics != nil {
		// Translate the extension-level config into the provider's own Config
		// via the registry translator (the provider package cannot be
		// imported here — it imports this package for its public-interface
		// implementation). The translated value is an opaque any consumed by
		// the VM factory at Create time.
		translated, err := registry.TranslateConfig(storedmodel.BackendVM, e.config.VictoriaMetrics.GetVMProviderConfig())
		if err != nil {
			e.logger.Warn("victoriametrics config translation failed; VM backend will not start", zap.Error(err))
		} else {
			cfg.VM = &hybrid.VMConfig{Inner: translated}
		}
	}
	return cfg
}

// ══════════════════════════════════════════════
// Hybrid routing helpers for Reader/Admin
// ══════════════════════════════════════════════

// getHybridTraceReader returns the appropriate TraceReader based on hybrid routing.
func (e *ObservabilityStorage) getHybridTraceReader() TraceReader {
	if e.hybridProvider == nil {
		return nil
	}
	switch e.hybridProvider.TraceBackend() {
	case "elasticsearch":
		if e.esProvider == nil || e.esProvider.TraceReader() == nil {
			return nil
		}
		return &traceReaderAdapter{inner: e.esProvider.TraceReader()}
	case "postgresql":
		if e.pgProvider == nil || e.pgProvider.TraceReader() == nil {
			return nil
		}
		return &pgTraceReaderAdapter{inner: e.pgProvider.TraceReader()}
	default:
		return nil
	}
}

// getHybridMetricReader returns the appropriate MetricReader based on hybrid routing.
func (e *ObservabilityStorage) getHybridMetricReader() MetricReader {
	if e.hybridProvider == nil {
		return nil
	}
	switch e.hybridProvider.MetricBackend() {
	case "elasticsearch":
		if e.esProvider == nil || e.esProvider.MetricReader() == nil {
			return nil
		}
		return &metricReaderAdapter{inner: e.esProvider.MetricReader()}
	case "postgresql":
		if e.pgProvider == nil || e.pgProvider.MetricReader() == nil {
			return nil
		}
		return &pgMetricReaderAdapter{inner: e.pgProvider.MetricReader()}
	case storedmodel.BackendVM:
		// The VM reader arrives as an opaque value from the hybrid provider
		// (neither side can name the other's types — see hybrid provider.go).
		// Assert it to the public MetricReader interface here, at the layer
		// that owns the interface.
		raw := e.hybridProvider.VMMetricReader()
		if raw == nil {
			return nil
		}
		r, ok := raw.(MetricReader)
		if !ok {
			e.logger.Warn("hybrid: victoriametrics metric reader has unexpected type")
			return nil
		}
		return r
	default:
		return nil
	}
}

// getHybridLogReader returns the appropriate LogReader based on hybrid routing.
func (e *ObservabilityStorage) getHybridLogReader() LogReader {
	if e.hybridProvider == nil {
		return nil
	}
	switch e.hybridProvider.LogBackend() {
	case "elasticsearch":
		if e.esProvider == nil || e.esProvider.LogReader() == nil {
			return nil
		}
		return &logReaderAdapter{inner: e.esProvider.LogReader()}
	case "postgresql":
		if e.pgProvider == nil || e.pgProvider.LogReader() == nil {
			return nil
		}
		return &pgLogReaderAdapter{inner: e.pgProvider.LogReader()}
	default:
		return nil
	}
}

// getHybridStorageAdmin returns the appropriate StorageAdmin based on hybrid routing.
func (e *ObservabilityStorage) getHybridStorageAdmin() StorageAdmin {
	if e.hybridProvider == nil {
		return nil
	}
	switch e.hybridProvider.AdminBackend() {
	case "elasticsearch":
		if e.esProvider == nil || e.esProvider.Admin() == nil {
			return nil
		}
		return &storageAdminAdapter{inner: e.esProvider.Admin(), config: e.config, retentionStore: e.retentionStore}
	case "postgresql":
		if e.pgProvider == nil || e.pgProvider.Admin() == nil {
			return nil
		}
		return &pgStorageAdminAdapter{inner: e.pgProvider.Admin(), config: e.config}
	default:
		return nil
	}
}

// ══════════════════════════════════════════════
// Lifecycle Scheduler Integration
// ══════════════════════════════════════════════

// buildLifecycleScheduler constructs the scheduler by wiring all dependencies.
// It uses the Strategy Pattern: the specific purger/reporter implementations are
// chosen based on the active provider type (DIP — depend on abstractions).
func (e *ObservabilityStorage) buildLifecycleScheduler(host component.Host) *lifecycle.LifecycleScheduler {
	// Initialize the retention store adapter — delegates to AppRetentionProvider when available.
	// Starts empty (all overrides = nil = use platform default); admin injects the provider later.
	e.retentionStore = newAppRetentionStoreAdapter()

	// Convert extension config to lifecycle domain types
	defaults := lifecycle.RetentionDefaults{
		Trace:  e.config.Retention.DefaultTrace,
		Metric: e.config.Retention.DefaultMetric,
		Log:    e.config.Retention.DefaultLog,
	}
	limits := lifecycle.RetentionLimits{
		MaxTrace:  e.config.Retention.MaxTrace,
		MaxMetric: e.config.Retention.MaxMetric,
		MaxLog:    e.config.Retention.MaxLog,
	}

	// Build scheduler config from extension-level config
	schedulerCfg := lifecycle.SchedulerConfig{
		Enabled:              e.config.Scheduler.Enabled,
		Interval:             e.config.Scheduler.Interval,
		DryRun:               e.config.Scheduler.DryRun,
		UsageWarningRatio:    e.config.Scheduler.UsageWarningRatio,
		UsageCriticalRatio:   e.config.Scheduler.UsageCriticalRatio,
		TrendBufferSize:      e.config.Scheduler.TrendBufferSize,
		Distributed:          e.config.Scheduler.Distributed,
		DistributedThreshold: e.config.Scheduler.DistributedThreshold,
		WorkerConcurrency:    e.config.Scheduler.WorkerConcurrency,
		TaskTimeout:          e.config.Scheduler.TaskTimeout,
		MaxRetries:           e.config.Scheduler.MaxRetries,
		VerifyTimeout:        e.config.Scheduler.VerifyTimeout,
		VerifyPollInterval:   e.config.Scheduler.VerifyPollInterval,
		NodeID:               e.resolvedNodeID,
	}

	// Build the scheduler options (functional options pattern)
	opts := []lifecycle.SchedulerOption{
		lifecycle.WithResolver(lifecycle.NewRetentionResolver(e.retentionStore, defaults, limits)),
		lifecycle.WithAuditEmitter(lifecycle.NewZapAuditEmitter(e.logger)),
		lifecycle.WithConfig(schedulerCfg),
		lifecycle.WithLogger(e.logger.Named("lifecycle")),
	}

	// Wire provider-specific purger and usage reporter (Strategy Pattern)
	switch e.config.Type {
	case "elasticsearch":
		if e.esProvider != nil && e.esProvider.GetClient() != nil {
			esCfg := e.convertESConfig()
			opts = append(opts,
				lifecycle.WithPurger(elasticsearch.NewPurger(e.esProvider.GetClient(), esCfg, e.logger)),
				lifecycle.WithUsageReporter(elasticsearch.NewUsageReporter(e.esProvider.GetClient(), esCfg, e.logger)),
			)
		}
	case "hybrid":
		// For hybrid, use ES purger/reporter if ES provider is available
		if e.esProvider != nil && e.esProvider.GetClient() != nil {
			esCfg := e.convertESConfig()
			opts = append(opts,
				lifecycle.WithPurger(elasticsearch.NewPurger(e.esProvider.GetClient(), esCfg, e.logger)),
				lifecycle.WithUsageReporter(elasticsearch.NewUsageReporter(e.esProvider.GetClient(), esCfg, e.logger)),
			)
		}
		// TODO: Add PG purger support when postgresql.Purger is implemented (Sprint 2)
	case "postgresql":
		// TODO: Add PG purger/reporter when postgresql lifecycle support is implemented (Sprint 2)
		e.logger.Warn("Lifecycle scheduler: PostgreSQL purger not yet implemented, purge will be skipped")
	}

	// Wire distributed engine if configured
	if e.config.Scheduler.Distributed {
		engine := e.resolveEngine(host)
		if engine != nil {
			nodeID := e.resolvedNodeID
			var elector lifecycle.LeaderElector = lifecycle.NewLocalLeaderElector()
			// Try Redis-based leader elector if storage is available
			if redisElector := e.buildRedisLeaderElector(host, nodeID); redisElector != nil {
				elector = redisElector
			}
			opts = append(opts, lifecycle.WithEngine(engine, elector))
		} else {
			e.logger.Warn("Distributed purge enabled but no engine could be created (no Redis), falling back to single-node mode")
		}
	}

	return lifecycle.NewScheduler(opts...)
}

// resolveEngine attempts to obtain a taskengine.Engine, preferring shared from controlplaneext,
// falling back to building a local engine with Redis. Returns nil only if no Redis is available.
func (e *ObservabilityStorage) resolveEngine(host component.Host) taskengine.Engine {
	// Strategy 1: Share engine from controlplane extension (same process)
	if engine := e.getSharedEngine(host); engine != nil {
		e.logger.Info("Distributed purge using shared engine from controlplane extension",
			zap.String("controlplane_extension", e.config.Scheduler.ControlplaneExtension))
		return engine
	}

	// Strategy 2: Build a local engine instance with Redis
	engine := e.buildLocalEngine(host)
	if engine != nil {
		e.logger.Info("Distributed purge using local engine instance (independent deployment)")
		return engine
	}

	return nil
}

// buildLocalEngine creates a standalone taskengine.Engine backed by Redis.
// Returns nil if Redis is not available.
func (e *ObservabilityStorage) buildLocalEngine(host component.Host) taskengine.Engine {
	storageExtName := e.config.Scheduler.StorageExtension
	if storageExtName == "" {
		e.logger.Warn("Distributed purge: storage_extension not configured, cannot create engine")
		return nil
	}

	// Find the storage extension
	storageType := component.MustNewType(storageExtName)
	var storage storageext.Storage
	for id, ext := range host.GetExtensions() {
		if id.Type() == storageType {
			if s, ok := ext.(storageext.Storage); ok {
				storage = s
				break
			}
		}
	}

	if storage == nil {
		e.logger.Warn("Storage extension not found, cannot create engine for distributed purge",
			zap.String("storage_extension", storageExtName))
		return nil
	}

	// Get Redis client
	redisName := e.config.Scheduler.RedisName
	var redisClient redis.UniversalClient
	var err error
	if redisName == "" || redisName == "default" {
		redisClient, err = storage.GetDefaultRedis()
	} else {
		redisClient, err = storage.GetRedis(redisName)
	}

	if err != nil {
		e.logger.Warn("Failed to get Redis for engine, distributed purge unavailable",
			zap.String("redis_name", redisName), zap.Error(err))
		return nil
	}

	// Build engine with Redis store
	storeCfg := taskengine.RedisStoreConfig{
		KeyPrefix: "te",
		ResultTTL: 24 * time.Hour,
	}
	store := taskengine.NewRedisStore(redisClient, e.logger.Named("lifecycle-engine-store"), storeCfg)

	engineCfg := taskengine.EngineConfig{
		DefaultTimeout:    e.config.Scheduler.TaskTimeout,
		DefaultMaxRetries: e.config.Scheduler.MaxRetries,
	}
	engine := taskengine.NewEngine(store, nil, e.logger.Named("lifecycle-engine"), engineCfg)

	// Start the engine
	if startErr := engine.Start(context.Background()); startErr != nil {
		e.logger.Error("Failed to start local engine", zap.Error(startErr))
		return nil
	}

	e.logger.Info("Local task engine created for distributed purge",
		zap.String("redis_name", redisName))
	return engine
}

// getSharedEngine attempts to obtain a shared taskengine.Engine from controlplaneext.
// Returns nil if the controlplane extension is not configured, not found, or not engine-backed.
func (e *ObservabilityStorage) getSharedEngine(host component.Host) taskengine.Engine {
	cpExtName := e.config.Scheduler.ControlplaneExtension
	if cpExtName == "" {
		return nil
	}

	cpType := component.MustNewType(cpExtName)
	for id, ext := range host.GetExtensions() {
		if id.Type() == cpType {
			// Use interface to avoid direct package import cycle
			type engineGetter interface {
				GetTaskEngine() taskengine.Engine
			}
			if eg, ok := ext.(engineGetter); ok {
				engine := eg.GetTaskEngine()
				if engine != nil {
					return engine
				}
				e.logger.Warn("Controlplane extension found but no engine available (task_manager.type != 'engine')",
					zap.String("controlplane_extension", cpExtName))
				return nil
			}
			e.logger.Warn("Controlplane extension found but does not implement engine getter",
				zap.String("controlplane_extension", cpExtName))
			return nil
		}
	}

	e.logger.Debug("Controlplane extension not found, will create local engine",
		zap.String("controlplane_extension", cpExtName))
	return nil
}

// buildRedisLeaderElector builds a Redis-backed leader elector for distributed purge.
// Returns nil if Redis is not available (falls back to local elector).
func (e *ObservabilityStorage) buildRedisLeaderElector(host component.Host, nodeID string) lifecycle.LeaderElector {
	storageExtName := e.config.Scheduler.StorageExtension
	if storageExtName == "" {
		return nil
	}

	storageType := component.MustNewType(storageExtName)
	var storage storageext.Storage
	for id, ext := range host.GetExtensions() {
		if id.Type() == storageType {
			if s, ok := ext.(storageext.Storage); ok {
				storage = s
				break
			}
		}
	}

	if storage == nil {
		return nil
	}

	redisName := e.config.Scheduler.RedisName
	var redisClient redis.UniversalClient
	var err error
	if redisName == "" || redisName == "default" {
		redisClient, err = storage.GetDefaultRedis()
	} else {
		redisClient, err = storage.GetRedis(redisName)
	}

	if err != nil {
		e.logger.Warn("Failed to get Redis for leader elector, using local elector",
			zap.Error(err))
		return nil
	}

	return lifecycle.NewRedisLeaderElector(redisClient, nodeID, e.logger)
}

// buildRollupEngine builds the 5m metric rollup engine when the provider is
// Elasticsearch and Redis is available. Returns nil if either prerequisite is
// missing (rollup silently disabled).
func (e *ObservabilityStorage) buildRollupEngine(host component.Host) *elasticsearch.RollupEngine {
	if e.esProvider == nil {
		e.logger.Warn("Rollup enabled but provider is not elasticsearch, disabling rollup")
		return nil
	}

	// Obtain Redis client for claim/watermark coordination.
	redisClient := e.getRedisClient(host)
	if redisClient == nil {
		e.logger.Warn("Rollup enabled but Redis unavailable, disabling rollup")
		return nil
	}

	nodeID := e.resolvedNodeID

	client := e.esProvider.GetClient()
	reader := e.esProvider.MetricReader()
	writer := e.esProvider.MetricWriter()
	if client == nil || reader == nil || writer == nil {
		e.logger.Warn("Rollup enabled but ES provider components not ready, disabling rollup")
		return nil
	}

	lock := lifecycle.NewRollupLock(redisClient, nodeID, e.logger)
	agg := elasticsearch.NewRollupAggregator(reader, e.logger)

	cfg := elasticsearch.RollupEngineConfig{
		Enabled:      true,
		TickInterval: e.config.Scheduler.Interval,
		ReadyAfter:   2 * time.Hour,
	}
	if e.config.Scheduler.RollupReadyAfter > 0 {
		cfg.ReadyAfter = e.config.Scheduler.RollupReadyAfter
	}
	cfg.ApplyDefaults()

	engine := elasticsearch.NewRollupEngine(cfg, client, agg, writer, lock, e.logger)

	// Wire rollup self-monitoring instruments via the collector's MeterProvider.
	// When nil (no telemetry configured), NewRollupMetrics falls back to a no-op
	// meter and the engine's nil-guards make instrumentation a no-op.
	if e.meterProvider != nil {
		engine.SetMetrics(elasticsearch.NewRollupMetrics(e.meterProvider.Meter("otelcol/rollup"), nodeID))
	}

	return engine
}

// getRedisClient returns a Redis client from the configured storage extension,
// or nil if unavailable. Shared by leader election and rollup coordination.
func (e *ObservabilityStorage) getRedisClient(host component.Host) redis.UniversalClient {
	storageExtName := e.config.Scheduler.StorageExtension
	if storageExtName == "" {
		return nil
	}
	storageType := component.MustNewType(storageExtName)
	var storage storageext.Storage
	for id, ext := range host.GetExtensions() {
		if id.Type() == storageType {
			if s, ok := ext.(storageext.Storage); ok {
				storage = s
				break
			}
		}
	}
	if storage == nil {
		return nil
	}
	redisName := e.config.Scheduler.RedisName
	var redisClient redis.UniversalClient
	var err error
	if redisName == "" || redisName == "default" {
		redisClient, err = storage.GetDefaultRedis()
	} else {
		redisClient, err = storage.GetRedis(redisName)
	}
	if err != nil {
		return nil
	}
	return redisClient
}

// GetLifecycleScheduler returns the lifecycle scheduler for API access (usage trends, etc.).
// Returns nil if the scheduler is not enabled.
func (e *ObservabilityStorage) GetLifecycleScheduler() *lifecycle.LifecycleScheduler {
	return e.scheduler
}

// GetRetentionStore returns the retention store for API access (per-app overrides).
// Returns nil if the scheduler is not enabled.
func (e *ObservabilityStorage) GetRetentionStore() lifecycle.RetentionStore {
	return e.retentionStore
}

// SetAppRetentionProvider injects both AppRetentionProvider and AppManager into the
// retention store adapter. Called by adminext after both extensions are initialized.
func (e *ObservabilityStorage) SetAppRetentionProvider(provider appmanager.AppRetentionProvider, apps appmanager.AppManager) {
	if adapter, ok := e.retentionStore.(*appRetentionStoreAdapter); ok {
		adapter.SetProviders(provider, apps)
	}
}


