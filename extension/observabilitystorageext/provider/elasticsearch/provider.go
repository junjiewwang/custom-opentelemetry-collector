// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package elasticsearch implements the observability storage Provider for Elasticsearch.
// It supports writing and reading Trace, Metric, and Log data to/from ES 7.x/8.x.
package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// Provider implements the observability storage provider for Elasticsearch.
type Provider struct {
	config *Config
	logger *zap.Logger
	client *Client

	traceWriter  *TraceWriter
	metricWriter *MetricWriter
	logWriter    *LogWriter

	traceReader  *TraceReader
	metricReader *MetricReader
	logReader    *LogReader

	admin *Admin
	usage *UsageReporter
}

// NewProvider creates a new Elasticsearch provider instance.
func NewProvider(config *Config, logger *zap.Logger) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("elasticsearch config is nil")
	}
	return &Provider{
		config: config,
		logger: logger.Named("es-provider"),
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "elasticsearch"
}

// Start initializes the ES client, creates index templates, and starts writers.
func (p *Provider) Start(ctx context.Context) error {
	p.logger.Info("Starting Elasticsearch provider",
		zap.Strings("addresses", p.config.Addresses),
	)

	// Initialize ES client
	client, err := NewClient(p.config, p.logger)
	if err != nil {
		return fmt.Errorf("failed to create ES client: %w", err)
	}
	p.client = client

	// Verify connectivity
	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping ES: %w", err)
	}

	// Initialize schema (index templates + ILM policies)
	p.admin = NewAdmin(p.client, p.config, p.logger)
	if err := p.admin.InitSchema(ctx); err != nil {
		p.logger.Warn("Failed to initialize ES schema (will retry on first write)", zap.Error(err))
	}

	// Initialize writers
	p.traceWriter = NewTraceWriter(p.client, p.config, p.logger)
	p.metricWriter = NewMetricWriter(p.client, p.config, p.logger)
	p.logWriter = NewLogWriter(p.client, p.config, p.logger)

	// Initialize readers.
	p.traceReader = NewTraceReader(p.client, p.config, p.logger)
	p.metricReader = NewMetricReader(p.client, p.config, p.logger)
	p.logReader = NewLogReader(p.client, p.config, p.logger)

	// Initialize usage reporter (for daily storage queries)
	p.usage = NewUsageReporter(p.client, p.config, p.logger)

	// Start background flush loops
	p.traceWriter.Start()
	p.metricWriter.Start()
	p.logWriter.Start()

	p.logger.Info("Elasticsearch provider started successfully")
	return nil
}

// Shutdown stops background workers and closes connections.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.logger.Info("Shutting down Elasticsearch provider")

	var errs []error
	if p.traceWriter != nil {
		if err := p.traceWriter.Flush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("trace writer flush: %w", err))
		}
		p.traceWriter.Stop()
	}
	if p.metricWriter != nil {
		if err := p.metricWriter.Flush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("metric writer flush: %w", err))
		}
		p.metricWriter.Stop()
	}
	if p.logWriter != nil {
		if err := p.logWriter.Flush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("log writer flush: %w", err))
		}
		p.logWriter.Stop()
	}

	// Release the HTTP connection pool's idle connections now that the
	// background flush loops are stopped and no in-flight requests remain.
	if p.client != nil {
		p.client.Close()
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// HealthCheck verifies ES cluster connectivity and status.
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
	if err := p.client.Ping(ctx); err != nil {
		return false, fmt.Sprintf("ES ping failed: %v", err), nil
	}

	info, err := p.client.ClusterHealth(ctx)
	if err != nil {
		return false, fmt.Sprintf("ES cluster health check failed: %v", err), nil
	}

	return info["status"] != "red",
		fmt.Sprintf("cluster status: %v", info["status"]),
		info
}

// WriteTraces writes trace data to Elasticsearch.
func (p *Provider) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	return p.traceWriter.WriteTraces(ctx, td)
}

// WriteSpans writes pre-converted StoredSpan documents.
func (p *Provider) WriteSpans(ctx context.Context, spans []StoredSpan) error {
	return p.traceWriter.WriteSpans(ctx, spans)
}

// WriteLogRecords writes pre-converted StoredLogRecord documents.
func (p *Provider) WriteLogRecords(ctx context.Context, records []StoredLogRecord) error {
	return p.logWriter.WriteLogRecords(ctx, records)
}

// WriteMetricPoints writes pre-converted StoredMetricDataPoint documents.
func (p *Provider) WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error {
	return p.metricWriter.WriteMetricPoints(ctx, points)
}

// WriteMetrics writes metric data to Elasticsearch.
func (p *Provider) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	return p.metricWriter.WriteMetrics(ctx, md)
}

// WriteLogs writes log data to Elasticsearch.
func (p *Provider) WriteLogs(ctx context.Context, ld plog.Logs) error {
	return p.logWriter.WriteLogs(ctx, ld)
}

// FlushTraces flushes buffered trace data.
func (p *Provider) FlushTraces(ctx context.Context) error {
	return p.traceWriter.Flush(ctx)
}

// FlushMetrics flushes buffered metric data.
func (p *Provider) FlushMetrics(ctx context.Context) error {
	return p.metricWriter.Flush(ctx)
}

// FlushLogs flushes buffered log data.
func (p *Provider) FlushLogs(ctx context.Context) error {
	return p.logWriter.Flush(ctx)
}

// Admin returns the storage admin interface.
func (p *Provider) Admin() *Admin {
	return p.admin
}

// GetDailyStorage returns per-day storage usage from ES index stats.
func (p *Provider) GetDailyStorage(ctx context.Context, req storedmodel.DailyStorageRequest) (*storedmodel.DailyStorageResponse, error) {
	if p.usage == nil {
		return &storedmodel.DailyStorageResponse{}, nil
	}
	return p.usage.GetDailyStorage(ctx, req)
}

// Purge removes data older than the given time for the given index pattern.
//
// This is a legacy field-based entry point (no external callers; kept for
// direct/test use). The query is built by the shared buildDeleteByQuery
// helper, so its construction matches the Admin and Purger purge paths. The
// caller-supplied timeField is honored with a nanosecond integer bound
// (appropriate for the long-typed startTimeUnixNano / timeUnixNano fields);
// callers querying the metric epoch_millis field should prefer Admin.Purge,
// which derives the field and bound type from the signal.
func (p *Provider) Purge(ctx context.Context, indexPattern, timeField string, before time.Time) (int64, error) {
	query := buildDeleteByQuery(timeField, before.UnixNano(), "")
	return p.client.DeleteByQuery(ctx, indexPattern, query)
}

// PurgeByApp removes data for a specific app older than the given time.
//
// Legacy field-based entry point (see Purge). The query is built by the shared
// buildDeleteByQuery helper, which scopes to the app via the canonical
// top-level appId field (matching Admin.PurgeByApp), rather than the
// resource.app_id sub-field used previously.
func (p *Provider) PurgeByApp(ctx context.Context, appID, indexPattern, timeField string, before time.Time) (int64, error) {
	query := buildDeleteByQuery(timeField, before.UnixNano(), appID)
	return p.client.DeleteByQuery(ctx, indexPattern, query)
}

// GetConfig returns the provider configuration (for admin operations).
func (p *Provider) GetConfig() *Config {
	return p.config
}

// TraceReader returns the trace reader instance.
func (p *Provider) TraceReader() *TraceReader {
	return p.traceReader
}

// MetricReader returns the metric reader instance.
func (p *Provider) MetricReader() *MetricReader {
	return p.metricReader
}

// LogReader returns the log reader instance.
func (p *Provider) LogReader() *LogReader {
	return p.logReader
}

// GetClient returns the underlying ES client for lifecycle management components.
func (p *Provider) GetClient() *Client {
	return p.client
}
