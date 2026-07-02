// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// OTel Standard Types — aligned with OTLP JSON Protobuf Encoding
//
// Field names use camelCase to match OTLP JSON format:
// https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding
// ═══════════════════════════════════════════════════

// KeyValue represents an OTel attribute key-value pair.
// Aligned with opentelemetry.proto.common.v1.KeyValue.
type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

// AnyValue represents a typed attribute value.
// Aligned with opentelemetry.proto.common.v1.AnyValue.
type AnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	IntValue    *int64      `json:"intValue,omitempty"`
	DoubleValue *float64    `json:"doubleValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	ArrayValue  *ArrayValue `json:"arrayValue,omitempty"`
	KvlistValue *KvlistValue `json:"kvlistValue,omitempty"`
	BytesValue  *string     `json:"bytesValue,omitempty"` // base64 encoded
}

// ArrayValue holds a list of AnyValue.
type ArrayValue struct {
	Values []AnyValue `json:"values"`
}

// KvlistValue holds a list of KeyValue pairs (nested map).
type KvlistValue struct {
	Values []KeyValue `json:"values"`
}

// ═══════════════════════════════════════════════════
// Span Enums — aligned with OTLP proto enums
// ═══════════════════════════════════════════════════

// SpanKind represents the type of a span.
// Values match opentelemetry.proto.trace.v1.Span.SpanKind.
type SpanKind string

const (
	SpanKindUnspecified SpanKind = "SPAN_KIND_UNSPECIFIED"
	SpanKindInternal    SpanKind = "SPAN_KIND_INTERNAL"
	SpanKindServer      SpanKind = "SPAN_KIND_SERVER"
	SpanKindClient      SpanKind = "SPAN_KIND_CLIENT"
	SpanKindProducer    SpanKind = "SPAN_KIND_PRODUCER"
	SpanKindConsumer    SpanKind = "SPAN_KIND_CONSUMER"
)

// StatusCode represents the status of a span.
// Values match opentelemetry.proto.trace.v1.Status.StatusCode.
type StatusCode string

const (
	StatusCodeUnset StatusCode = "STATUS_CODE_UNSET"
	StatusCodeOk    StatusCode = "STATUS_CODE_OK"
	StatusCodeError StatusCode = "STATUS_CODE_ERROR"
)

// SpanStatus represents the status of a span operation.
// Aligned with opentelemetry.proto.trace.v1.Status.
type SpanStatus struct {
	Code    StatusCode `json:"code"`
	Message string     `json:"message,omitempty"`
}

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
// Trace Types — aligned with OTLP Trace model
// ═══════════════════════════════════════════════════

// TraceQuery holds parameters for searching traces.
type TraceQuery struct {
	AppID         string            `json:"appId,omitempty"`
	ServiceName   string            `json:"service,omitempty"`
	OperationName string            `json:"operation,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	MinDuration   time.Duration     `json:"minDuration,omitempty"`
	MaxDuration   time.Duration     `json:"maxDuration,omitempty"`
	TimeRange     TimeRange         `json:"timeRange"`
	Limit         int               `json:"limit,omitempty"`
	Offset        int               `json:"offset,omitempty"`
}

// TraceSearchResult holds the result of a trace search.
type TraceSearchResult struct {
	Traces []Trace `json:"traces"`
	Total  int64   `json:"total"`
}

// Trace represents a complete trace with all its spans.
type Trace struct {
	TraceID string `json:"traceId"`
	Spans   []Span `json:"spans"`
	// Derived fields (pre-computed by backend for UI convenience)
	DurationNano    string `json:"durationNano"`          // total duration in nanoseconds (string for precision)
	SpanCount       int    `json:"spanCount"`
	ServiceCount    int    `json:"serviceCount"`
	RootServiceName string `json:"rootServiceName,omitempty"`
	RootSpanName    string `json:"rootSpanName,omitempty"`
}

// Span represents a single span within a trace.
// Core fields aligned with opentelemetry.proto.trace.v1.Span.
type Span struct {
	// ═══ OTel Standard Fields ═══
	TraceID            string     `json:"traceId"`
	SpanID             string     `json:"spanId"`
	ParentSpanID       string     `json:"parentSpanId,omitempty"`
	TraceState         string     `json:"traceState,omitempty"`
	Name               string     `json:"name"`                    // operation name (OTel uses "name")
	Kind               SpanKind   `json:"kind"`
	StartTimeUnixNano  string     `json:"startTimeUnixNano"`       // nanosecond Unix timestamp as string
	EndTimeUnixNano    string     `json:"endTimeUnixNano"`
	Attributes         []KeyValue `json:"attributes,omitempty"`
	Events             []SpanEvent `json:"events,omitempty"`
	Links              []SpanLink  `json:"links,omitempty"`
	Status             SpanStatus  `json:"status"`
	// ═══ Derived Fields (for UI convenience) ═══
	ServiceName        string     `json:"serviceName"`             // extracted from resource["service.name"]
	DurationNano       string     `json:"durationNano"`            // endTime - startTime
	Resource           []KeyValue `json:"resource,omitempty"`      // resource attributes
}

// SpanEvent represents a timestamped event on a span.
// Aligned with opentelemetry.proto.trace.v1.Span.Event.
type SpanEvent struct {
	TimeUnixNano string     `json:"timeUnixNano"`
	Name         string     `json:"name"`
	Attributes   []KeyValue `json:"attributes,omitempty"`
}

// SpanLink represents a link to another span.
// Aligned with opentelemetry.proto.trace.v1.Span.Link.
type SpanLink struct {
	TraceID    string     `json:"traceId"`
	SpanID     string     `json:"spanId"`
	TraceState string     `json:"traceState,omitempty"`
	Attributes []KeyValue `json:"attributes,omitempty"`
}

// Service represents a service discovered in the system.
type Service struct {
	Name      string `json:"name"`
	SpanCount int64  `json:"spanCount,omitempty"`
}

// Operation represents an operation within a service.
type Operation struct {
	Name     string   `json:"name"`
	SpanKind SpanKind `json:"spanKind,omitempty"`
}

// Dependency represents a dependency between two services.
type Dependency struct {
	Parent    string `json:"parent"`
	Child     string `json:"child"`
	CallCount int64  `json:"callCount"`
}

// ═══════════════════════════════════════════════════
// Metric Types
// ═══════════════════════════════════════════════════

// MetricQuery holds parameters for an instant metric query.
type MetricQuery struct {
	AppID       string            `json:"appId,omitempty"`
	MetricName  string            `json:"metric"`
	Labels      map[string]string `json:"labels,omitempty"`
	ServiceName string            `json:"service,omitempty"`
	Time        time.Time         `json:"time"`
}

// MetricRangeQuery holds parameters for a range metric query.
type MetricRangeQuery struct {
	AppID       string            `json:"appId,omitempty"`
	MetricName  string            `json:"metric"`
	Labels      map[string]string `json:"labels,omitempty"`
	ServiceName string            `json:"service,omitempty"`
	TimeRange   TimeRange         `json:"timeRange"`
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
	Metric       string            `json:"metric,omitempty"`
	Labels       map[string]string `json:"labels"`
	Value        float64           `json:"value"`
	TimeUnixNano string            `json:"timeUnixNano"`
}

// MetricSeries is a series of metric values over time.
type MetricSeries struct {
	Metric string              `json:"metric,omitempty"`
	Labels map[string]string   `json:"labels"`
	Values []MetricTimeValue   `json:"values"`
}

// MetricTimeValue is a single time-value pair in a metric series.
type MetricTimeValue struct {
	TimeUnixNano string  `json:"timeUnixNano"`
	Value        float64 `json:"value"`
}

// ═══════════════════════════════════════════════════
// Log Types — aligned with OTLP Log model
// ═══════════════════════════════════════════════════

// LogQuery holds parameters for searching logs.
type LogQuery struct {
	AppID       string            `json:"appId,omitempty"`
	Query       string            `json:"query,omitempty"`
	ServiceName string            `json:"service,omitempty"`
	Severity    []string          `json:"severity,omitempty"`
	TraceID     string            `json:"traceId,omitempty"`
	SpanID      string            `json:"spanId,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	TimeRange   TimeRange         `json:"timeRange"`
	Limit       int               `json:"limit,omitempty"`
	Offset      int               `json:"offset,omitempty"`
}

// LogSearchResult holds the result of a log search.
type LogSearchResult struct {
	Logs  []LogRecord `json:"logs"`
	Total int64       `json:"total"`
}

// LogRecord represents a single log entry.
// Aligned with opentelemetry.proto.logs.v1.LogRecord.
type LogRecord struct {
	ID                   string     `json:"id"`
	TimeUnixNano         string     `json:"timeUnixNano"`
	ObservedTimeUnixNano string     `json:"observedTimeUnixNano,omitempty"`
	TraceID              string     `json:"traceId,omitempty"`
	SpanID               string     `json:"spanId,omitempty"`
	SeverityNumber       int32      `json:"severityNumber"`
	SeverityText         string     `json:"severityText"`
	Body                 string     `json:"body"`
	Attributes           []KeyValue `json:"attributes,omitempty"`
	Resource             []KeyValue `json:"resource,omitempty"`
	// Derived fields
	ServiceName          string     `json:"serviceName"`
	AppID                string     `json:"appId,omitempty"`
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
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

// LogStatsQuery holds parameters for log statistics queries.
type LogStatsQuery struct {
	AppID       string    `json:"appId,omitempty"`
	ServiceName string    `json:"service,omitempty"`
	TimeRange   TimeRange `json:"timeRange"`
	GroupBy     string    `json:"groupBy,omitempty"`
}

// LogStats holds log statistics results.
type LogStats struct {
	TotalCount     int64            `json:"totalCount"`
	SeverityCounts map[string]int64 `json:"severityCounts,omitempty"`
	ServiceCounts  map[string]int64 `json:"serviceCounts,omitempty"`
	TimeHistogram  []TimeBucket     `json:"timeHistogram,omitempty"`
}

// TimeBucket represents a count within a time interval.
type TimeBucket struct {
	TimeUnixNano string `json:"timeUnixNano"`
	Count        int64  `json:"count"`
}

// ═══════════════════════════════════════════════════
// Admin Types
// ═══════════════════════════════════════════════════

// RetentionDuration wraps time.Duration with JSON marshaling as Go duration string (e.g. "720h").
type RetentionDuration time.Duration

// MarshalJSON serializes the duration as a Go duration string (e.g. "720h0m0s").
func (d RetentionDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON deserializes a Go duration string ("720h") into RetentionDuration.
func (d *RetentionDuration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = RetentionDuration(dur)
	return nil
}

// RetentionPolicy defines the data retention for a signal type.
type RetentionPolicy struct {
	Duration RetentionDuration `json:"duration"`
}

// StorageStatus holds the overall status of the storage backend.
type StorageStatus struct {
	Provider string         `json:"provider"`
	Healthy  bool           `json:"healthy"`
	Version  string         `json:"version,omitempty"`
	Indices  []IndexInfo    `json:"indices,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// IndexInfo holds information about a storage index/table.
type IndexInfo struct {
	Name      string     `json:"name"`
	DocsCount int64      `json:"docsCount"`
	SizeBytes int64      `json:"sizeBytes"`
	Signal    SignalType  `json:"signal"`
}

// PurgeResult holds the result of a data purge operation.
type PurgeResult struct {
	DeletedCount int64  `json:"deletedCount"`
	FreedBytes   int64  `json:"freedBytes,omitempty"`
	Message      string `json:"message,omitempty"`
}

// DiskUsage holds storage space usage information.
type DiskUsage struct {
	TotalBytes     int64               `json:"totalBytes"`
	UsedBytes      int64               `json:"usedBytes"`
	AvailableBytes int64               `json:"availableBytes"`
	BySignal       map[SignalType]int64 `json:"bySignal,omitempty"`
	ByApp          map[string]int64     `json:"byApp,omitempty"`
}

// ═══════════════════════════════════════════════════
// Daily Storage Types (aliased from storedmodel to avoid circular imports with ES)
// ═══════════════════════════════════════════════════

type DailyStorageRequest = storedmodel.DailyStorageRequest
type DailyStoragePoint = storedmodel.DailyStoragePoint
type DailyStorageResponse = storedmodel.DailyStorageResponse

// ═══════════════════════════════════════════════════
// Helper Functions — for constructing OTel types
// ═══════════════════════════════════════════════════

// NewStringValue creates an AnyValue holding a string.
func NewStringValue(s string) AnyValue {
	return AnyValue{StringValue: &s}
}

// NewIntValue creates an AnyValue holding an int64.
func NewIntValue(i int64) AnyValue {
	return AnyValue{IntValue: &i}
}

// NewDoubleValue creates an AnyValue holding a float64.
func NewDoubleValue(f float64) AnyValue {
	return AnyValue{DoubleValue: &f}
}

// NewBoolValue creates an AnyValue holding a bool.
func NewBoolValue(b bool) AnyValue {
	return AnyValue{BoolValue: &b}
}

// MapToKeyValues converts a map[string]any to a slice of KeyValue.
// This is the bridge between the internal map representation
// used in ES storage and the OTel-standard KeyValue format.
func MapToKeyValues(m map[string]any) []KeyValue {
	if m == nil {
		return nil
	}
	kvs := make([]KeyValue, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, KeyValue{Key: k, Value: anyToAnyValue(v)})
	}
	return kvs
}

// anyToAnyValue converts a Go interface{} to an OTel AnyValue.
func anyToAnyValue(v any) AnyValue {
	if v == nil {
		s := ""
		return AnyValue{StringValue: &s}
	}
	switch val := v.(type) {
	case string:
		return AnyValue{StringValue: &val}
	case int:
		i := int64(val)
		return AnyValue{IntValue: &i}
	case int32:
		i := int64(val)
		return AnyValue{IntValue: &i}
	case int64:
		return AnyValue{IntValue: &val}
	case float32:
		f := float64(val)
		return AnyValue{DoubleValue: &f}
	case float64:
		return AnyValue{DoubleValue: &val}
	case bool:
		return AnyValue{BoolValue: &val}
	case []any:
		values := make([]AnyValue, len(val))
		for i, item := range val {
			values[i] = anyToAnyValue(item)
		}
		return AnyValue{ArrayValue: &ArrayValue{Values: values}}
	case map[string]any:
		kvs := MapToKeyValues(val)
		return AnyValue{KvlistValue: &KvlistValue{Values: kvs}}
	default:
		s := fmt.Sprintf("%v", val)
		return AnyValue{StringValue: &s}
	}
}

// TimeToUnixNano converts a time.Time to a nanosecond string (OTel standard).
func TimeToUnixNano(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

// NormalizeSpanKind converts various span kind strings to OTel standard enum values.
func NormalizeSpanKind(kind string) SpanKind {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "INTERNAL", "SPAN_KIND_INTERNAL", "1":
		return SpanKindInternal
	case "SERVER", "SPAN_KIND_SERVER", "2":
		return SpanKindServer
	case "CLIENT", "SPAN_KIND_CLIENT", "3":
		return SpanKindClient
	case "PRODUCER", "SPAN_KIND_PRODUCER", "4":
		return SpanKindProducer
	case "CONSUMER", "SPAN_KIND_CONSUMER", "5":
		return SpanKindConsumer
	default:
		return SpanKindUnspecified
	}
}

// NormalizeStatusCode converts various status code strings to OTel standard enum values.
func NormalizeStatusCode(code string) StatusCode {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "OK", "STATUS_CODE_OK", "1":
		return StatusCodeOk
	case "ERROR", "STATUS_CODE_ERROR", "2":
		return StatusCodeError
	default:
		return StatusCodeUnset
	}
}
