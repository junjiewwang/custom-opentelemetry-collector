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
	// Trace specifies which backend to use for traces: "elasticsearch" or "postgresql".
	Trace string

	// Metric specifies which backend to use for metrics.
	Metric string

	// Log specifies which backend to use for logs.
	Log string

	// Admin specifies which backend to use for admin operations.
	Admin string

	// ES holds the Elasticsearch provider config (used if any signal routes to ES).
	ES *elasticsearch.Config

	// PG holds the PostgreSQL provider config (used if any signal routes to PG).
	PG *postgresql.Config
}

// Provider implements a hybrid observability storage provider that routes
// different signal types to different backends.
type Provider struct {
	config *Config
	logger *zap.Logger

	// Sub-providers (only initialized if needed based on routing config)
	esProvider *elasticsearch.Provider
	pgProvider *postgresql.Provider

	// Routing: which backend each signal uses
	traceBackend  string
	metricBackend string
	logBackend    string
	adminBackend  string
}

// NewProvider creates a new Hybrid provider instance.
func NewProvider(config *Config, logger *zap.Logger) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("hybrid config is nil")
	}

	// Validate routing
	validBackends := map[string]bool{"elasticsearch": true, "postgresql": true}
	for _, b := range []string{config.Trace, config.Metric, config.Log, config.Admin} {
		if !validBackends[b] {
			return nil, fmt.Errorf("invalid backend in hybrid config: %q (must be 'elasticsearch' or 'postgresql')", b)
		}
	}

	return &Provider{
		config:        config,
		logger:        logger.Named("hybrid-provider"),
		traceBackend:  config.Trace,
		metricBackend: config.Metric,
		logBackend:    config.Log,
		adminBackend:  config.Admin,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "hybrid"
}

// Start initializes the sub-providers that are needed based on routing config.
func (p *Provider) Start(ctx context.Context) error {
	p.logger.Info("Starting Hybrid provider",
		zap.String("trace", p.traceBackend),
		zap.String("metric", p.metricBackend),
		zap.String("log", p.logBackend),
		zap.String("admin", p.adminBackend),
	)

	needsES := p.traceBackend == "elasticsearch" || p.metricBackend == "elasticsearch" ||
		p.logBackend == "elasticsearch" || p.adminBackend == "elasticsearch"
	needsPG := p.traceBackend == "postgresql" || p.metricBackend == "postgresql" ||
		p.logBackend == "postgresql" || p.adminBackend == "postgresql"

	if needsES {
		if p.config.ES == nil {
			return fmt.Errorf("hybrid routing requires elasticsearch config but it is nil")
		}
		esProvider, err := elasticsearch.NewProvider(p.config.ES, p.logger)
		if err != nil {
			return fmt.Errorf("failed to create ES sub-provider: %w", err)
		}
		if err := esProvider.Start(ctx); err != nil {
			return fmt.Errorf("failed to start ES sub-provider: %w", err)
		}
		p.esProvider = esProvider
		p.logger.Info("ES sub-provider started")
	}

	if needsPG {
		if p.config.PG == nil {
			return fmt.Errorf("hybrid routing requires postgresql config but it is nil")
		}
		pgProvider, err := postgresql.NewProvider(p.config.PG, p.logger)
		if err != nil {
			return fmt.Errorf("failed to create PG sub-provider: %w", err)
		}
		if err := pgProvider.Start(ctx); err != nil {
			return fmt.Errorf("failed to start PG sub-provider: %w", err)
		}
		p.pgProvider = pgProvider
		p.logger.Info("PG sub-provider started")
	}

	p.logger.Info("Hybrid provider started successfully")
	return nil
}

// Shutdown gracefully stops all sub-providers.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.logger.Info("Shutting down Hybrid provider")
	var errs []error

	if p.esProvider != nil {
		if err := p.esProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("ES shutdown: %w", err))
		}
	}
	if p.pgProvider != nil {
		if err := p.pgProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("PG shutdown: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("hybrid shutdown errors: %v", errs)
	}
	return nil
}

// HealthCheck verifies all sub-providers are healthy.
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
	details := map[string]any{
		"routing": map[string]string{
			"trace":  p.traceBackend,
			"metric": p.metricBackend,
			"log":    p.logBackend,
			"admin":  p.adminBackend,
		},
	}

	allHealthy := true
	var messages []string

	if p.esProvider != nil {
		healthy, msg, esDetails := p.esProvider.HealthCheck(ctx)
		if !healthy {
			allHealthy = false
		}
		messages = append(messages, fmt.Sprintf("ES: %s", msg))
		details["elasticsearch"] = esDetails
	}

	if p.pgProvider != nil {
		healthy, msg, pgDetails := p.pgProvider.HealthCheck(ctx)
		if !healthy {
			allHealthy = false
		}
		messages = append(messages, fmt.Sprintf("PG: %s", msg))
		details["postgresql"] = pgDetails
	}

	return allHealthy, fmt.Sprintf("hybrid(%v)", messages), details
}

// ══════════════════════════════════
// Write operations — route by signal
// ══════════════════════════════════

// WriteTraces routes trace writes to the configured backend.
func (p *Provider) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	switch p.traceBackend {
	case "elasticsearch":
		return p.esProvider.WriteTraces(ctx, td)
	case "postgresql":
		return p.pgProvider.WriteTraces(ctx, td)
	default:
		return fmt.Errorf("no backend for traces")
	}
}

// WriteSpans routes pre-converted spans to the chosen backend.
func (p *Provider) WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error {
	switch p.traceBackend {
	case "elasticsearch":
		return p.esProvider.WriteSpans(ctx, spans)
	case "postgresql":
		return p.pgProvider.WriteSpans(ctx, spans)
	default:
		return fmt.Errorf("no backend for traces")
	}
}

// WriteLogRecords routes pre-converted log records to the chosen backend.
func (p *Provider) WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error {
	switch p.logBackend {
	case "elasticsearch":
		return p.esProvider.WriteLogRecords(ctx, records)
	case "postgresql":
		return p.pgProvider.WriteLogRecords(ctx, records)
	default:
		return fmt.Errorf("no backend for logs")
	}
}

// WriteMetricPoints routes pre-converted metric data points to the chosen backend.
func (p *Provider) WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error {
	switch p.metricBackend {
	case "elasticsearch":
		return p.esProvider.WriteMetricPoints(ctx, points)
	case "postgresql":
		return p.pgProvider.WriteMetricPoints(ctx, points)
	default:
		return fmt.Errorf("no backend for metrics")
	}
}

// WriteMetrics routes metric writes to the configured backend.
func (p *Provider) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	switch p.metricBackend {
	case "elasticsearch":
		return p.esProvider.WriteMetrics(ctx, md)
	case "postgresql":
		return p.pgProvider.WriteMetrics(ctx, md)
	default:
		return fmt.Errorf("no backend for metrics")
	}
}

// WriteLogs routes log writes to the configured backend.
func (p *Provider) WriteLogs(ctx context.Context, ld plog.Logs) error {
	switch p.logBackend {
	case "elasticsearch":
		return p.esProvider.WriteLogs(ctx, ld)
	case "postgresql":
		return p.pgProvider.WriteLogs(ctx, ld)
	default:
		return fmt.Errorf("no backend for logs")
	}
}

// FlushTraces flushes buffered trace data.
func (p *Provider) FlushTraces(ctx context.Context) error {
	switch p.traceBackend {
	case "elasticsearch":
		return p.esProvider.FlushTraces(ctx)
	case "postgresql":
		return p.pgProvider.FlushTraces(ctx)
	default:
		return nil
	}
}

// FlushMetrics flushes buffered metric data.
func (p *Provider) FlushMetrics(ctx context.Context) error {
	switch p.metricBackend {
	case "elasticsearch":
		return p.esProvider.FlushMetrics(ctx)
	case "postgresql":
		return p.pgProvider.FlushMetrics(ctx)
	default:
		return nil
	}
}

// FlushLogs flushes buffered log data.
func (p *Provider) FlushLogs(ctx context.Context) error {
	switch p.logBackend {
	case "elasticsearch":
		return p.esProvider.FlushLogs(ctx)
	case "postgresql":
		return p.pgProvider.FlushLogs(ctx)
	default:
		return nil
	}
}

// ══════════════════════════════════
// Reader accessors — route by signal
// ══════════════════════════════════

// ESProvider returns the Elasticsearch sub-provider (may be nil).
func (p *Provider) ESProvider() *elasticsearch.Provider {
	return p.esProvider
}

// PGProvider returns the PostgreSQL sub-provider (may be nil).
func (p *Provider) PGProvider() *postgresql.Provider {
	return p.pgProvider
}

// TraceBackend returns which backend handles traces.
func (p *Provider) TraceBackend() string {
	return p.traceBackend
}

// MetricBackend returns which backend handles metrics.
func (p *Provider) MetricBackend() string {
	return p.metricBackend
}

// LogBackend returns which backend handles logs.
func (p *Provider) LogBackend() string {
	return p.logBackend
}

// AdminBackend returns which backend handles admin operations.
func (p *Provider) AdminBackend() string {
	return p.adminBackend
}
