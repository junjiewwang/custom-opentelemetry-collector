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
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/hybrid"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/postgresql"
	"go.opentelemetry.io/collector/custom/extension/storageext"
)

// internalProvider is the internal interface that both ES and PG providers implement.
// It decouples the extension from specific provider implementations.
type internalProvider interface {
	Name() string
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	HealthCheck(ctx context.Context) (bool, string, map[string]any)
	WriteTraces(ctx context.Context, td ptrace.Traces) error
	WriteMetrics(ctx context.Context, md pmetric.Metrics) error
	WriteLogs(ctx context.Context, ld plog.Logs) error
	FlushTraces(ctx context.Context) error
	FlushMetrics(ctx context.Context) error
	FlushLogs(ctx context.Context) error
}

// ObservabilityStorage is the extension that manages the observability data storage provider.
// It holds a provider instance and exposes Writer/Admin interfaces to other components.
type ObservabilityStorage struct {
	config   *Config
	logger   *zap.Logger
	provider internalProvider

	// Concrete providers (only one is non-nil based on config.Type)
	esProvider     *elasticsearch.Provider
	pgProvider     *postgresql.Provider
	hybridProvider *hybrid.Provider

	// Lifecycle management
	scheduler      *lifecycle.LifecycleScheduler
	retentionStore lifecycle.RetentionStore
}

// Ensure the extension implements the required interfaces.
var _ extension.Extension = (*ObservabilityStorage)(nil)

// newObservabilityStorageExtension creates a new instance of the extension.
func newObservabilityStorageExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*ObservabilityStorage, error) {
	return &ObservabilityStorage{
		config: config,
		logger: set.Logger,
	}, nil
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

	e.provider = provider
	e.logger.Info("Observability storage extension started successfully",
		zap.String("provider", provider.Name()),
	)

	// Start lifecycle scheduler if enabled
	if e.config.Scheduler.Enabled {
		e.scheduler = e.buildLifecycleScheduler(host)
		e.scheduler.Start(ctx)
		e.logger.Info("Lifecycle scheduler started")
	}

	return nil
}

// Shutdown gracefully stops the storage provider.
func (e *ObservabilityStorage) Shutdown(ctx context.Context) error {
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

// WriteTraces writes trace data through the provider.
func (e *ObservabilityStorage) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	if e.provider == nil {
		return fmt.Errorf("provider not initialized")
	}
	return e.provider.WriteTraces(ctx, td)
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
	switch e.config.Type {
	case "elasticsearch":
		if e.esProvider == nil || e.esProvider.Admin() == nil {
			return nil
		}
		return &storageAdminAdapter{inner: e.esProvider.Admin(), config: e.config}
	case "postgresql":
		if e.pgProvider == nil || e.pgProvider.Admin() == nil {
			return nil
		}
		return &pgStorageAdminAdapter{inner: e.pgProvider.Admin(), config: e.config}
	case "hybrid":
		return e.getHybridStorageAdmin()
	default:
		return nil
	}
}

// GetTraceReader returns the TraceReader interface.
func (e *ObservabilityStorage) GetTraceReader() TraceReader {
	switch e.config.Type {
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
	case "hybrid":
		return e.getHybridTraceReader()
	default:
		return nil
	}
}

// GetMetricReader returns the MetricReader interface.
func (e *ObservabilityStorage) GetMetricReader() MetricReader {
	switch e.config.Type {
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
	case "hybrid":
		return e.getHybridMetricReader()
	default:
		return nil
	}
}

// GetLogReader returns the LogReader interface.
func (e *ObservabilityStorage) GetLogReader() LogReader {
	switch e.config.Type {
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
	case "hybrid":
		return e.getHybridLogReader()
	default:
		return nil
	}
}

// createProvider creates the appropriate provider based on configuration.
func (e *ObservabilityStorage) createProvider() (internalProvider, error) {
	switch e.config.Type {
	case "elasticsearch":
		esCfg := e.convertESConfig()
		p, err := elasticsearch.NewProvider(esCfg, e.logger)
		if err != nil {
			return nil, err
		}
		e.esProvider = p
		return p, nil
	case "postgresql":
		pgCfg := e.convertPGConfig()
		p, err := postgresql.NewProvider(pgCfg, e.logger)
		if err != nil {
			return nil, err
		}
		e.pgProvider = p
		return p, nil
	case "hybrid":
		hybridCfg := e.convertHybridConfig()
		p, err := hybrid.NewProvider(hybridCfg, e.logger)
		if err != nil {
			return nil, err
		}
		e.hybridProvider = p
		// Expose sub-providers for Reader/Admin access
		e.esProvider = p.ESProvider()
		e.pgProvider = p.PGProvider()
		return p, nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", e.config.Type)
	}
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
		return &storageAdminAdapter{inner: e.esProvider.Admin(), config: e.config}
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
	// Initialize the in-memory retention store (shared with API layer for future use)
	e.retentionStore = lifecycle.NewInMemoryRetentionStore()

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
		NodeID:               e.config.Scheduler.NodeID,
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

	// Wire distributed coordinator if configured
	if e.config.Scheduler.Distributed {
		coordinator := e.buildCoordinator(host)
		if coordinator != nil {
			opts = append(opts, lifecycle.WithCoordinator(coordinator))
		}
	}

	return lifecycle.NewScheduler(opts...)
}

// buildCoordinator attempts to build a distributed TaskCoordinator from Redis.
// Returns nil if Redis is unavailable (graceful degradation to single-node mode).
func (e *ObservabilityStorage) buildCoordinator(host component.Host) lifecycle.TaskCoordinator {
	storageExtName := e.config.Scheduler.StorageExtension
	if storageExtName == "" {
		e.logger.Warn("Distributed purge enabled but storage_extension not configured, using local mode")
		return nil
	}

	// Find the storage extension from the host
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
		e.logger.Warn("Storage extension not found, distributed purge falling back to local mode",
			zap.String("storage_extension", storageExtName),
		)
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
		e.logger.Warn("Failed to get Redis client for distributed purge, falling back to local mode",
			zap.String("redis_name", redisName),
			zap.Error(err),
		)
		return nil
	}

	// Determine nodeID
	nodeID := e.config.Scheduler.NodeID
	if nodeID == "" {
		nodeID = fmt.Sprintf("node-%d", time.Now().UnixNano())
	}

	e.logger.Info("Distributed purge coordinator initialized",
		zap.String("node_id", nodeID),
		zap.String("redis_name", redisName),
	)

	return lifecycle.NewRedisCoordinator(redisClient, nodeID, e.logger)
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
