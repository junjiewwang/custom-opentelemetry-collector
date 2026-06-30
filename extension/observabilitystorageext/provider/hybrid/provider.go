// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package hybrid implements a composite observability storage Provider that
// routes different signal types (trace, metric, log) to different backend providers.
// For example: Traces → Elasticsearch (full-text), Metrics → PostgreSQL (time-series).
package hybrid

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/postgresql"
)

// Config holds the Hybrid provider routing configuration.
type Config struct {
	Trace string `mapstructure:storedmodel.SignalTrace`
	Metric string `mapstructure:storedmodel.SignalMetric`
	Log   string `mapstructure:storedmodel.SignalLog`
	Admin string `mapstructure:storedmodel.SignalAdmin`
	ES    *elasticsearch.Config `mapstructure:"es"`
	PG    *postgresql.Config `mapstructure:"pg"`
}

// subProvider is the local interface that both ES and PG providers satisfy.
// It decouples the hybrid router from concrete provider types (DIP).
type subProvider interface {
	Name() string
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	HealthCheck(ctx context.Context) (bool, string, map[string]any)

	WriteTraces(ctx context.Context, td ptrace.Traces) error
	WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error
	WriteLogs(ctx context.Context, ld plog.Logs) error
	WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error
	WriteMetrics(ctx context.Context, md pmetric.Metrics) error
	WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error

	FlushTraces(ctx context.Context) error
	FlushMetrics(ctx context.Context) error
	FlushLogs(ctx context.Context) error
}

// Provider implements a hybrid observability storage provider that routes
// different signal types to different backends via the subProvider interface.
type Provider struct {
	config *Config
	logger *zap.Logger

	// registry: backend name → provider instance (extensible, OCP)
	backends map[string]subProvider

	// routing: signal name → backend name
	routing map[string]string

	// concrete references retained for read-path accessors (backward compat)
	esProvider *elasticsearch.Provider
	pgProvider *postgresql.Provider
}

// NewProvider creates a new Hybrid provider instance.
func NewProvider(config *Config, logger *zap.Logger) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("hybrid config is nil")
	}

	routing := map[string]string{
		storedmodel.SignalTrace: config.Trace,
		storedmodel.SignalMetric: config.Metric,
		storedmodel.SignalLog:   config.Log,
		storedmodel.SignalAdmin: config.Admin,
	}

	for signal, be := range routing {
		if be != storedmodel.BackendES && be != storedmodel.BackendPG {
			return nil, fmt.Errorf("invalid backend for %s: %q (must be %q or %q)",
				signal, be, storedmodel.BackendES, storedmodel.BackendPG)
		}
	}

	return &Provider{
		config:   config,
		logger:   logger.Named("hybrid-provider"),
		backends: make(map[string]subProvider),
		routing:  routing,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string { return "hybrid" }

// Start initializes sub-providers as needed based on routing config.
func (p *Provider) Start(ctx context.Context) error {
	p.logger.Info("Starting Hybrid provider",
		zap.String(storedmodel.SignalTrace, p.routing[storedmodel.SignalTrace]),
		zap.String(storedmodel.SignalMetric, p.routing[storedmodel.SignalMetric]),
		zap.String(storedmodel.SignalLog, p.routing[storedmodel.SignalLog]),
		zap.String(storedmodel.SignalAdmin, p.routing[storedmodel.SignalAdmin]),
	)

	// Determine which backends are needed
	needed := make(map[string]bool)
	for _, be := range p.routing {
		needed[be] = true
	}

	if needed[storedmodel.BackendES] {
		es, err := p.startES(ctx)
		if err != nil {
			return err
		}
		p.backends[storedmodel.BackendES] = es
		p.esProvider = es.(*elasticsearch.Provider)
	}

	if needed[storedmodel.BackendPG] {
		pg, err := p.startPG(ctx)
		if err != nil {
			return err
		}
		p.backends[storedmodel.BackendPG] = pg
		p.pgProvider = pg.(*postgresql.Provider)
	}

	p.logger.Info("Hybrid provider started")
	return nil
}

func (p *Provider) startES(ctx context.Context) (subProvider, error) {
	if p.config.ES == nil {
		return nil, fmt.Errorf("hybrid routing requires elasticsearch config but it is nil")
	}
	es, err := elasticsearch.NewProvider(p.config.ES, p.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create ES sub-provider: %w", err)
	}
	if err := es.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start ES sub-provider: %w", err)
	}
	p.logger.Info("ES sub-provider started")
	return es, nil
}

func (p *Provider) startPG(ctx context.Context) (subProvider, error) {
	if p.config.PG == nil {
		return nil, fmt.Errorf("hybrid routing requires postgresql config but it is nil")
	}
	pg, err := postgresql.NewProvider(p.config.PG, p.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create PG sub-provider: %w", err)
	}
	if err := pg.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start PG sub-provider: %w", err)
	}
	p.logger.Info("PG sub-provider started")
	return pg, nil
}

// Shutdown gracefully stops all sub-providers.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.logger.Info("Shutting down Hybrid provider")
	var errs []error
	for name, be := range p.backends {
		if err := be.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s shutdown: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("hybrid shutdown errors: %v", errs)
	}
	return nil
}

// HealthCheck verifies all sub-providers are healthy.
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
	details := map[string]any{"routing": p.routing}
	allHealthy := true
	var msgs []string

	for name, be := range p.backends {
		healthy, msg, beDetails := be.HealthCheck(ctx)
		if !healthy {
			allHealthy = false
		}
		msgs = append(msgs, fmt.Sprintf("%s: %s", name, msg))
		details[name] = beDetails
	}

	return allHealthy, fmt.Sprintf("hybrid(%v)", msgs), details
}

// ═══════════════════════════════════════════════════════
// Write routing — unified via backendFor, all nil-safe
// ═══════════════════════════════════════════════════════

// backendFor returns the sub-provider for a signal, or an error.
func (p *Provider) backendFor(signal string) (subProvider, error) {
	name := p.routing[signal]
	be := p.backends[name]
	if be == nil {
		return nil, fmt.Errorf("no backend for signal %q (configured: %q, started: %v)", signal, name, p.backendNames())
	}
	return be, nil
}

func (p *Provider) backendNames() []string {
	names := make([]string, 0, len(p.backends))
	for n := range p.backends {
		names = append(names, n)
	}
	return names
}

func (p *Provider) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	be, err := p.backendFor(storedmodel.SignalTrace)
	if err != nil {
		return err
	}
	return be.WriteTraces(ctx, td)
}

func (p *Provider) WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error {
	be, err := p.backendFor(storedmodel.SignalTrace)
	if err != nil {
		return err
	}
	return be.WriteSpans(ctx, spans)
}

func (p *Provider) WriteLogs(ctx context.Context, ld plog.Logs) error {
	be, err := p.backendFor(storedmodel.SignalLog)
	if err != nil {
		return err
	}
	return be.WriteLogs(ctx, ld)
}

func (p *Provider) WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error {
	be, err := p.backendFor(storedmodel.SignalLog)
	if err != nil {
		return err
	}
	return be.WriteLogRecords(ctx, records)
}

func (p *Provider) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	be, err := p.backendFor(storedmodel.SignalMetric)
	if err != nil {
		return err
	}
	return be.WriteMetrics(ctx, md)
}

func (p *Provider) WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error {
	be, err := p.backendFor(storedmodel.SignalMetric)
	if err != nil {
		return err
	}
	return be.WriteMetricPoints(ctx, points)
}

func (p *Provider) FlushTraces(ctx context.Context) error {
	be, err := p.backendFor(storedmodel.SignalTrace)
	if err != nil {
		return nil // flush is best-effort
	}
	return be.FlushTraces(ctx)
}

func (p *Provider) FlushMetrics(ctx context.Context) error {
	be, err := p.backendFor(storedmodel.SignalMetric)
	if err != nil {
		return nil
	}
	return be.FlushMetrics(ctx)
}

func (p *Provider) FlushLogs(ctx context.Context) error {
	be, err := p.backendFor(storedmodel.SignalLog)
	if err != nil {
		return nil
	}
	return be.FlushLogs(ctx)
}

// ═══════════════════════════════════════════════════════
// Reader accessors — retained for backward compat with
// extension.go's read routing (GetTraceReader etc.)
// Deprecated: prefer routing via internalProvider interface.
// ═══════════════════════════════════════════════════════

func (p *Provider) ESProvider() *elasticsearch.Provider { return p.esProvider }
func (p *Provider) PGProvider() *postgresql.Provider   { return p.pgProvider }
func (p *Provider) TraceBackend() string                { return p.routing[storedmodel.SignalTrace] }
func (p *Provider) MetricBackend() string               { return p.routing[storedmodel.SignalMetric] }
func (p *Provider) LogBackend() string                  { return p.routing[storedmodel.SignalLog] }
func (p *Provider) AdminBackend() string                { return p.routing[storedmodel.SignalAdmin] }
