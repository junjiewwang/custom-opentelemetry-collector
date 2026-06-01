// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
)

// ObservabilityStorage is the extension that manages the observability data storage provider.
// It holds a single ES Provider instance and exposes Writer/Admin interfaces to other components.
type ObservabilityStorage struct {
	config   *Config
	logger   *zap.Logger
	provider *elasticsearch.Provider
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
func (e *ObservabilityStorage) Start(ctx context.Context, _ component.Host) error {
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
	return nil
}

// Shutdown gracefully stops the storage provider.
func (e *ObservabilityStorage) Shutdown(ctx context.Context) error {
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
func (e *ObservabilityStorage) GetProvider() *elasticsearch.Provider {
	return e.provider
}

// GetStorageAdmin returns the StorageAdmin interface.
func (e *ObservabilityStorage) GetStorageAdmin() StorageAdmin {
	if e.provider == nil || e.provider.Admin() == nil {
		return nil
	}
	return &storageAdminAdapter{inner: e.provider.Admin(), config: e.config}
}

// GetTraceReader returns the TraceReader interface.
func (e *ObservabilityStorage) GetTraceReader() TraceReader {
	if e.provider == nil || e.provider.TraceReader() == nil {
		return nil
	}
	return &traceReaderAdapter{inner: e.provider.TraceReader()}
}

// GetMetricReader returns the MetricReader interface.
func (e *ObservabilityStorage) GetMetricReader() MetricReader {
	if e.provider == nil || e.provider.MetricReader() == nil {
		return nil
	}
	return &metricReaderAdapter{inner: e.provider.MetricReader()}
}

// GetLogReader returns the LogReader interface.
func (e *ObservabilityStorage) GetLogReader() LogReader {
	if e.provider == nil || e.provider.LogReader() == nil {
		return nil
	}
	return &logReaderAdapter{inner: e.provider.LogReader()}
}

// createProvider creates the appropriate provider based on configuration.
func (e *ObservabilityStorage) createProvider() (*elasticsearch.Provider, error) {
	switch e.config.Type {
	case "elasticsearch":
		esCfg := e.convertESConfig()
		return elasticsearch.NewProvider(esCfg, e.logger)
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
