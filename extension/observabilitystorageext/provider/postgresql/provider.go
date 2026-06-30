// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package postgresql implements the observability storage Provider for PostgreSQL.
// It supports writing and reading Trace, Metric, and Log data with native partitioning
// and optional TimescaleDB integration for time-series metrics.
package postgresql

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// Provider implements the observability storage provider for PostgreSQL.
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

	// hasTimescaleDB is determined at startup.
	hasTimescaleDB bool
}

// NewProvider creates a new PostgreSQL provider instance.
func NewProvider(config *Config, logger *zap.Logger) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("postgresql config is nil")
	}
	return &Provider{
		config: config,
		logger: logger.Named("pg-provider"),
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "postgresql"
}

// Start initializes the PG client, runs migrations, and starts writers.
func (p *Provider) Start(ctx context.Context) error {
	p.logger.Info("Starting PostgreSQL provider")

	// Ensure target database exists (auto-create if missing)
	if err := EnsureDatabase(ctx, p.config.DSN, p.logger); err != nil {
		p.logger.Warn("Failed to ensure database exists (will try connecting anyway)", zap.Error(err))
	}

	// Initialize PG client (connection pool)
	client, err := NewClient(p.config, p.logger)
	if err != nil {
		return fmt.Errorf("failed to create PG client: %w", err)
	}
	p.client = client

	// Verify connectivity
	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping PG: %w", err)
	}
	p.logger.Info("PostgreSQL connection established")

	// Run schema migrations
	migrator := NewMigrator(p.config.DSN, p.logger)
	if err := migrator.Up(); err != nil {
		return fmt.Errorf("failed to run schema migrations: %w", err)
	}

	// Check for TimescaleDB availability
	if p.config.UseTimescaleDB {
		hasTS, err := p.client.HasTimescaleDB(ctx)
		if err != nil {
			p.logger.Warn("Failed to check TimescaleDB availability", zap.Error(err))
		} else {
			p.hasTimescaleDB = hasTS
			if hasTS {
				p.logger.Info("TimescaleDB detected, enabling hypertable features")
				if err := p.setupTimescaleDB(ctx); err != nil {
					p.logger.Warn("Failed to setup TimescaleDB hypertables (falling back to native partitions)", zap.Error(err))
					p.hasTimescaleDB = false
				}
			} else {
				p.logger.Info("TimescaleDB not available, using native PG partitions")
			}
		}
	}

	// Ensure initial partitions exist
	if err := p.ensurePartitions(ctx); err != nil {
		p.logger.Warn("Failed to create initial partitions", zap.Error(err))
	}

	// Initialize admin
	p.admin = NewAdmin(p.client, p.config, p.logger, p.hasTimescaleDB)

	// Initialize writers
	p.traceWriter = NewTraceWriter(p.client, p.config, p.logger)
	p.metricWriter = NewMetricWriter(p.client, p.config, p.logger)
	p.logWriter = NewLogWriter(p.client, p.config, p.logger)

	// Initialize readers
	p.traceReader = NewTraceReader(p.client, p.config, p.logger)
	p.metricReader = NewMetricReader(p.client, p.config, p.logger)
	p.logReader = NewLogReader(p.client, p.config, p.logger)

	// Start background flush loops
	p.traceWriter.Start()
	p.metricWriter.Start()
	p.logWriter.Start()

	p.logger.Info("PostgreSQL provider started successfully")
	return nil
}

// Shutdown stops background workers and closes connections.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.logger.Info("Shutting down PostgreSQL provider")

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

	if p.client != nil {
		p.client.Close()
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// HealthCheck verifies PG connectivity and status.
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
	if err := p.client.Ping(ctx); err != nil {
		return false, fmt.Sprintf("PG ping failed: %v", err), nil
	}

	version, err := p.client.GetVersion(ctx)
	if err != nil {
		return false, fmt.Sprintf("PG version check failed: %v", err), nil
	}

	details := map[string]any{
		"version":        version,
		"has_timescaledb": p.hasTimescaleDB,
	}

	return true, "connected", details
}

// WriteTraces writes trace data to PostgreSQL.
func (p *Provider) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	return p.traceWriter.WriteTraces(ctx, td)
}

// WriteSpans writes pre-converted StoredSpan documents to PostgreSQL.
func (p *Provider) WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error {
	return p.traceWriter.WriteSpans(ctx, spans)
}

// WriteLogRecords writes pre-converted StoredLogRecord documents.
func (p *Provider) WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error {
	return p.logWriter.WriteLogRecords(ctx, records)
}

// WriteMetricPoints writes pre-converted StoredMetricDataPoint documents.
func (p *Provider) WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error {
	return p.metricWriter.WriteMetricPoints(ctx, points)
}

// WriteMetrics writes metric data to PostgreSQL.
func (p *Provider) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	return p.metricWriter.WriteMetrics(ctx, md)
}

// WriteLogs writes log data to PostgreSQL.
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

// GetConfig returns the provider configuration.
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

// setupTimescaleDB converts the metrics table to a hypertable for efficient time-series queries.
func (p *Provider) setupTimescaleDB(ctx context.Context) error {
	// Only convert metrics table to hypertable (most benefit from time-series optimization)
	_, err := p.client.Exec(ctx, fmt.Sprintf(
		"SELECT create_hypertable('%s', 'timestamp', if_not_exists => true, migrate_data => true)",
		p.config.Metrics.TableName,
	))
	return err
}

// ensurePartitions creates partitions for the current and next period.
func (p *Provider) ensurePartitions(ctx context.Context) error {
	now := time.Now().UTC()

	tables := []struct {
		name     string
		interval time.Duration
	}{
		{p.config.Traces.TableName, p.config.Traces.PartitionInterval},
		{p.config.Metrics.TableName, p.config.Metrics.PartitionInterval},
		{p.config.Logs.TableName, p.config.Logs.PartitionInterval},
	}

	for _, t := range tables {
		// Skip if using TimescaleDB for metrics (auto-managed)
		if p.hasTimescaleDB && t.name == p.config.Metrics.TableName {
			continue
		}

		if err := p.createPartition(ctx, t.name, t.interval, now); err != nil {
			p.logger.Warn("Failed to create current partition",
				zap.String("table", t.name),
				zap.Error(err),
			)
		}
		// Also create next period's partition
		if err := p.createPartition(ctx, t.name, t.interval, now.Add(t.interval)); err != nil {
			p.logger.Warn("Failed to create next partition",
				zap.String("table", t.name),
				zap.Error(err),
			)
		}
	}
	return nil
}

// createPartition creates a time-range partition for the given table and time.
func (p *Provider) createPartition(ctx context.Context, tableName string, interval time.Duration, t time.Time) error {
	// Truncate to partition interval boundary
	start := t.Truncate(interval)
	end := start.Add(interval)

	partName := fmt.Sprintf("%s_p%s", tableName, start.Format("20060102"))

	sql := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		partName,
		tableName,
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
	)

	_, err := p.client.Exec(ctx, sql)
	if err != nil {
		// Ignore "already exists" errors
		return nil
	}
	p.logger.Debug("Created partition",
		zap.String("partition", partName),
		zap.Time("from", start),
		zap.Time("to", end),
	)
	return nil
}
