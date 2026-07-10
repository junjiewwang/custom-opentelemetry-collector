// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// Reader Types — local definitions to avoid circular import
// with the parent package (observabilitystorageext).
//
// These mirror the types in observabilitystorageext/types.go.
// The extension layer converts between these local types and
// the public interface types when exposing Reader to other components.
// ═══════════════════════════════════════════════════

// TimeRange aliases the unified storedmodel definition.
type TimeRange = storedmodel.TimeRange

// TraceQuery aliases the unified storedmodel definition.
type TraceQuery = storedmodel.TraceQuery

// TraceSearchResult holds the result of a trace search.
type TraceSearchResult struct {
	Traces []Trace
	Total  int64
}

// Trace represents a complete trace with all its spans.
type Trace struct {
	TraceID  string
	Spans    []Span
	Duration int64 // total duration in microseconds
}

// Span represents a single span within a trace.
type Span struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	OperationName string
	ServiceName   string
	SpanKind      string
	StatusCode    string
	StatusMessage string
	StartTime     time.Time
	EndTime       time.Time
	DurationUS    int64
	Attributes    map[string]any
	Resource      map[string]any
	Events        []SpanEvent
	Links         []SpanLink
}

// SpanEvent represents an event annotation on a span.
type SpanEvent struct {
	Name       string
	Timestamp  time.Time
	Attributes map[string]any
}

// SpanLink represents a link to another span.
type SpanLink struct {
	TraceID string
	SpanID  string
}

// Service represents a service in the system.
type Service struct {
	Name string
}

// Operation represents an operation within a service.
type Operation struct {
	Name     string
	SpanKind string
}

// Dependency represents a dependency between two services.
type Dependency struct {
	Parent    string
	Child     string
	CallCount int64
}

// ── Log Query Types ────────────────────────────────

// LogQuery holds parameters for searching logs.
type LogQuery struct {
	AppID       string            // required: identifies which app's data to query
	Query       string            // full-text search
	ServiceName string
	Severity    []string          // e.g. ["ERROR", "WARN"]
	TraceID     string
	SpanID      string
	Attributes  map[string]string
	TimeRange   TimeRange
	Limit       int
	Offset      int
}

// LogSearchResult holds the result of a log search.
type LogSearchResult struct {
	Logs  []LogRecord
	Total int64
}

// LogRecord represents a single log entry.
type LogRecord struct {
	ID             string
	Timestamp      time.Time
	ObservedTime   time.Time
	TraceID        string
	SpanID         string
	Severity       string
	SeverityNumber int32
	Body           string
	ServiceName    string
	AppID          string
	Attributes     map[string]any
	Resource       map[string]any
}

// LogContext holds surrounding log lines for context viewing.
type LogContext struct {
	Before []LogRecord
	Target LogRecord
	After  []LogRecord
}

// LogField describes an available field for log filtering.
type LogField struct {
	Name  string
	Type  string // "keyword", "text", "number"
	Count int64  // approximate number of logs with this field
}

// LogStatsQuery holds parameters for log statistics queries.
type LogStatsQuery struct {
	AppID       string // required: identifies which app's data to query
	ServiceName string
	TimeRange   TimeRange
	GroupBy     string // e.g. "severity", "service_name"
}

// LogStats holds log statistics results.
type LogStats struct {
	TotalCount     int64
	SeverityCounts map[string]int64
	ServiceCounts  map[string]int64
	TimeHistogram  []TimeBucket
}

// TimeBucket represents a count within a time interval.
type TimeBucket struct {
	Time  time.Time
	Count int64
}

// ── Metric Query Types ─────────────────────────────

// MetricQuery holds parameters for an instant metric query.
type MetricQuery struct {
	AppID       string // required: identifies which app's data to query
	MetricName  string
	Labels      map[string]string
	ServiceName string
	Time        time.Time
}

// MetricRangeQuery holds parameters for a range metric query.
// Semantically aligned with Grafana InfluxDB Query Builder:
//
//	SELECT <aggregation>("value") FROM <MetricName>
//	WHERE <Labels>/<LabelMatch> AND time >= start AND time <= end
//	GROUP BY time(<Step>), <GroupBy...>
//	FILL(<Fill>) SLIMIT <SeriesLimit> LIMIT <Limit>
type MetricRangeQuery struct {
	AppID       string // required: identifies which app's data to query
	MetricName  string
	Labels      map[string]string // WHERE tag = 'value'
	LabelMatch  map[string]string // WHERE tag =~ /regex/
	ServiceName string
	TimeRange   TimeRange
	Aggregation string   // SELECT <func>, default "avg"
	Step        time.Duration
	GroupBy     []string // GROUP BY "tag1", "tag2"
	Fill        string   // FILL(strategy), default "null"
	Limit       int      // LIMIT (data points), default 10000
	SeriesLimit int      // SLIMIT (series count), default 100
}

// MetricResult holds the result of an instant metric query.
type MetricResult struct {
	Data []MetricDataPoint
}

// MetricRangeResult holds the result of a range metric query.
type MetricRangeResult struct {
	Data []MetricSeries
}

// MetricDataPoint is a single metric value at a point in time.
type MetricDataPoint struct {
	Labels map[string]string
	Value  float64
	Time   time.Time
}

// MetricSeries is a series of metric values over time.
type MetricSeries struct {
	Labels map[string]string
	Values []MetricDataPoint
}
