// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// ═══════════════════════════════════════════════════
// Provider — 统一门面接口
// ═══════════════════════════════════════════════════

// Provider is the unified interface for observability data storage.
// It exposes writers, readers, and admin operations for all signal types.
// Implementations include Elasticsearch, PostgreSQL, MongoDB, and Hybrid providers.
type Provider interface {
	// Name returns the provider implementation name (e.g., "elasticsearch").
	Name() string

	// Capabilities returns which signal types are supported for read/write.
	Capabilities() Capabilities

	// TraceWriter returns the writer for trace data.
	TraceWriter() TraceWriter

	// MetricWriter returns the writer for metric data.
	MetricWriter() MetricWriter

	// LogWriter returns the writer for log data.
	LogWriter() LogWriter

	// TraceReader returns the reader for trace queries.
	TraceReader() TraceReader

	// MetricReader returns the reader for metric queries.
	MetricReader() MetricReader

	// LogReader returns the reader for log queries.
	LogReader() LogReader

	// Admin returns the storage administration interface.
	Admin() StorageAdmin

	// Start initializes connections and prepares the provider for use.
	Start(ctx context.Context) error

	// Shutdown gracefully closes connections and flushes pending data.
	Shutdown(ctx context.Context) error

	// HealthCheck verifies the provider's backend connectivity.
	HealthCheck(ctx context.Context) (*HealthStatus, error)
}

// Capabilities describes which signal types a provider supports.
type Capabilities struct {
	Trace  SignalCapability
	Metric SignalCapability
	Log    SignalCapability
	Admin  bool
}

// SignalCapability describes read/write support for a single signal type.
type SignalCapability struct {
	Write bool
	Read  bool
}

// ═══════════════════════════════════════════════════
// Writer — 写入接口 (由 Exporter 调用)
// ═══════════════════════════════════════════════════

// TraceWriter writes trace data to the storage backend.
type TraceWriter interface {
	// WriteTraces writes a batch of traces. The implementation should
	// handle internal buffering and bulk operations.
	WriteTraces(ctx context.Context, td ptrace.Traces) error

	// Flush forces any buffered data to be written to the backend.
	Flush(ctx context.Context) error
}

// MetricWriter writes metric data to the storage backend.
type MetricWriter interface {
	// WriteMetrics writes a batch of metrics.
	WriteMetrics(ctx context.Context, md pmetric.Metrics) error

	// Flush forces any buffered data to be written to the backend.
	Flush(ctx context.Context) error
}

// LogWriter writes log data to the storage backend.
type LogWriter interface {
	// WriteLogs writes a batch of logs.
	WriteLogs(ctx context.Context, ld plog.Logs) error

	// Flush forces any buffered data to be written to the backend.
	Flush(ctx context.Context) error
}

// ═══════════════════════════════════════════════════
// Reader — 查询接口 (由 adminext API Handler 调用)
// ═══════════════════════════════════════════════════

// TraceReader queries trace data from the storage backend.
type TraceReader interface {
	// SearchTraces searches for traces matching the query parameters.
	SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error)

	// GetTrace retrieves a single trace by its trace ID.
	GetTrace(ctx context.Context, traceID string) (*Trace, error)

	// GetServices returns all service names within the time range.
	GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error)

	// GetOperations returns operations for a given service.
	GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error)

	// GetDependencies returns service-to-service dependencies for the service map.
	GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error)
}

// MetricReader queries metric data from the storage backend.
type MetricReader interface {
	// Query executes an instant metric query.
	Query(ctx context.Context, query MetricQuery) (*MetricResult, error)

	// QueryRange executes a range metric query.
	QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error)

	// ListMetricNames returns all available metric names.
	ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error)

	// ListLabelNames returns all label names.
	ListLabelNames(ctx context.Context, timeRange TimeRange) ([]string, error)

	// ListLabelValues returns values for a specific label.
	ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error)
}

// LogReader queries log data from the storage backend.
type LogReader interface {
	// SearchLogs searches for logs matching the query parameters.
	SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error)

	// GetLogContext retrieves surrounding log lines for context.
	GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error)

	// ListLogFields returns available log fields for filtering.
	ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error)

	// GetLogStats returns log statistics (counts, severity distribution, etc.).
	GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error)
}

// ═══════════════════════════════════════════════════
// Admin — 管理接口
// ═══════════════════════════════════════════════════

// StorageAdmin provides administrative operations for the storage backend.
type StorageAdmin interface {
	// GetStatus returns the current storage health and statistics.
	GetStatus(ctx context.Context) (*StorageStatus, error)

	// InitSchema creates or updates the storage schema (index templates, tables, etc.).
	InitSchema(ctx context.Context) error

	// GetRetention returns current retention policies per signal type.
	GetRetention(ctx context.Context) (map[SignalType]RetentionPolicy, error)

	// SetRetention updates the retention policy for a signal type.
	SetRetention(ctx context.Context, signal SignalType, policy RetentionPolicy) error

	// Purge removes data older than the specified time for the given signal type.
	Purge(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error)

	// PurgeByApp removes data for a specific app older than the specified time.
	PurgeByApp(ctx context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error)

	// GetDiskUsage returns storage space usage information.
	GetDiskUsage(ctx context.Context) (*DiskUsage, error)
}

// ═══════════════════════════════════════════════════
// HealthStatus
// ═══════════════════════════════════════════════════

// HealthStatus represents the health of the storage backend.
type HealthStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
	// Details holds provider-specific health information.
	Details map[string]any `json:"details,omitempty"`
}
