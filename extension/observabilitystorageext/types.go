// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import "time"

// ═══════════════════════════════════════════════════
// Common Types
// ═══════════════════════════════════════════════════

// SignalType identifies the type of observability signal.
type SignalType string

const (
	SignalTrace  SignalType = "trace"
	SignalMetric SignalType = "metric"
	SignalLog    SignalType = "log"
)

// TimeRange defines a time window for queries.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// ═══════════════════════════════════════════════════
// Trace Types
// ═══════════════════════════════════════════════════

// TraceQuery holds parameters for searching traces.
type TraceQuery struct {
	ServiceName   string        `json:"service_name,omitempty"`
	OperationName string        `json:"operation_name,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	MinDuration   time.Duration `json:"min_duration,omitempty"`
	MaxDuration   time.Duration `json:"max_duration,omitempty"`
	TimeRange     TimeRange     `json:"time_range"`
	Limit         int           `json:"limit,omitempty"`
	Offset        int           `json:"offset,omitempty"`
}

// TraceSearchResult holds the result of a trace search.
type TraceSearchResult struct {
	Traces []Trace `json:"traces"`
	Total  int64   `json:"total"`
}

// Trace represents a complete trace with all its spans.
type Trace struct {
	TraceID  string `json:"trace_id"`
	Spans    []Span `json:"spans"`
	Duration int64  `json:"duration_us"` // total duration in microseconds
}

// Span represents a single span within a trace.
type Span struct {
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	OperationName string         `json:"operation_name"`
	ServiceName   string         `json:"service_name"`
	SpanKind      string         `json:"span_kind"`
	StatusCode    string         `json:"status_code"`
	StatusMessage string         `json:"status_message,omitempty"`
	StartTime     time.Time      `json:"start_time"`
	EndTime       time.Time      `json:"end_time"`
	DurationUS    int64          `json:"duration_us"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Resource      map[string]any `json:"resource,omitempty"`
	Events        []SpanEvent    `json:"events,omitempty"`
	Links         []SpanLink     `json:"links,omitempty"`
}

// SpanEvent represents an event annotation on a span.
type SpanEvent struct {
	Name       string         `json:"name"`
	Timestamp  time.Time      `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// SpanLink represents a link to another span.
type SpanLink struct {
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

// Service represents a service in the system.
type Service struct {
	Name string `json:"name"`
}

// Operation represents an operation within a service.
type Operation struct {
	Name     string `json:"name"`
	SpanKind string `json:"span_kind,omitempty"`
}

// Dependency represents a dependency between two services.
type Dependency struct {
	Parent    string `json:"parent"`
	Child     string `json:"child"`
	CallCount int64  `json:"call_count"`
}

// ═══════════════════════════════════════════════════
// Metric Types
// ═══════════════════════════════════════════════════

// MetricQuery holds parameters for an instant metric query.
type MetricQuery struct {
	MetricName  string            `json:"metric_name"`
	Labels      map[string]string `json:"labels,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
	Time        time.Time         `json:"time"`
}

// MetricRangeQuery holds parameters for a range metric query.
type MetricRangeQuery struct {
	MetricName  string            `json:"metric_name"`
	Labels      map[string]string `json:"labels,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
	TimeRange   TimeRange         `json:"time_range"`
	Step        time.Duration     `json:"step"`
}

// MetricResult holds the result of an instant metric query.
type MetricResult struct {
	Data []MetricDataPoint `json:"data"`
}

// MetricRangeResult holds the result of a range metric query.
type MetricRangeResult struct {
	Data []MetricSeries `json:"data"`
}

// MetricDataPoint is a single metric value at a point in time.
type MetricDataPoint struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
	Time   time.Time         `json:"time"`
}

// MetricSeries is a series of metric values over time.
type MetricSeries struct {
	Labels map[string]string  `json:"labels"`
	Values []MetricDataPoint  `json:"values"`
}

// ═══════════════════════════════════════════════════
// Log Types
// ═══════════════════════════════════════════════════

// LogQuery holds parameters for searching logs.
type LogQuery struct {
	Query       string            `json:"query,omitempty"`        // full-text search
	ServiceName string            `json:"service_name,omitempty"`
	Severity    []string          `json:"severity,omitempty"`     // e.g. ["ERROR", "WARN"]
	TraceID     string            `json:"trace_id,omitempty"`
	SpanID      string            `json:"span_id,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	TimeRange   TimeRange         `json:"time_range"`
	Limit       int               `json:"limit,omitempty"`
	Offset      int               `json:"offset,omitempty"`
}

// LogSearchResult holds the result of a log search.
type LogSearchResult struct {
	Logs  []LogRecord `json:"logs"`
	Total int64       `json:"total"`
}

// LogRecord represents a single log entry.
type LogRecord struct {
	ID             string         `json:"id"`
	Timestamp      time.Time      `json:"timestamp"`
	ObservedTime   time.Time      `json:"observed_time,omitempty"`
	TraceID        string         `json:"trace_id,omitempty"`
	SpanID         string         `json:"span_id,omitempty"`
	Severity       string         `json:"severity"`
	SeverityNumber int32          `json:"severity_number"`
	Body           string         `json:"body"`
	ServiceName    string         `json:"service_name"`
	AppID          string         `json:"app_id,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	Resource       map[string]any `json:"resource,omitempty"`
}

// LogContext holds surrounding log lines for context viewing.
type LogContext struct {
	Before []LogRecord `json:"before"`
	Target LogRecord   `json:"target"`
	After  []LogRecord `json:"after"`
}

// LogField describes an available field for log filtering.
type LogField struct {
	Name  string `json:"name"`
	Type  string `json:"type"`  // "keyword", "text", "number"
	Count int64  `json:"count"` // approximate number of logs with this field
}

// LogStatsQuery holds parameters for log statistics queries.
type LogStatsQuery struct {
	ServiceName string    `json:"service_name,omitempty"`
	TimeRange   TimeRange `json:"time_range"`
	GroupBy     string    `json:"group_by,omitempty"` // e.g. "severity", "service_name"
}

// LogStats holds log statistics results.
type LogStats struct {
	TotalCount       int64            `json:"total_count"`
	SeverityCounts   map[string]int64 `json:"severity_counts,omitempty"`
	ServiceCounts    map[string]int64 `json:"service_counts,omitempty"`
	TimeHistogram    []TimeBucket     `json:"time_histogram,omitempty"`
}

// TimeBucket represents a count within a time interval.
type TimeBucket struct {
	Time  time.Time `json:"time"`
	Count int64     `json:"count"`
}

// ═══════════════════════════════════════════════════
// Admin Types
// ═══════════════════════════════════════════════════

// RetentionPolicy defines the data retention for a signal type.
type RetentionPolicy struct {
	Duration time.Duration `json:"duration"`
}

// StorageStatus holds the overall status of the storage backend.
type StorageStatus struct {
	Provider    string         `json:"provider"`
	Healthy     bool           `json:"healthy"`
	Version     string         `json:"version,omitempty"`
	Indices     []IndexInfo    `json:"indices,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// IndexInfo holds information about a storage index/table.
type IndexInfo struct {
	Name       string `json:"name"`
	DocsCount  int64  `json:"docs_count"`
	SizeBytes  int64  `json:"size_bytes"`
	Signal     SignalType `json:"signal"`
}

// PurgeResult holds the result of a data purge operation.
type PurgeResult struct {
	DeletedCount int64  `json:"deleted_count"`
	FreedBytes   int64  `json:"freed_bytes,omitempty"`
	Message      string `json:"message,omitempty"`
}

// DiskUsage holds storage space usage information.
type DiskUsage struct {
	TotalBytes     int64            `json:"total_bytes"`
	UsedBytes      int64            `json:"used_bytes"`
	AvailableBytes int64            `json:"available_bytes"`
	BySignal       map[SignalType]int64 `json:"by_signal,omitempty"`
}
