// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import "time"

// TraceMetricsQuery is the input for a TraceQL metrics query.
type TraceMetricsQuery struct {
	// AppID for tenant isolation.
	AppID string

	// Filters from the span filter part of the TraceQL query.
	ServiceName   string
	OperationName string
	Tags          map[string]string
	TagsOr        [][]map[string]string
	SpanKind      string
	Status        string
	IsRoot        bool
	MinDuration   time.Duration
	MaxDuration   time.Duration

	// Time range for the query.
	TimeRange TimeRange

	// Step is the bucket interval for the date histogram.
	Step time.Duration

	// Metrics function configuration.
	Function    string   // "rate", "quantile_over_time", "histogram_over_time"
	Field       string   // intrinsic field, e.g. "duration"
	Percentiles []float64 // for quantile_over_time
	ByLabels    []string   // group-by labels
	Sample      bool       // sample hint (ignored in basic impl)

	// ── Negation / Existence / Regex filters (Sprint 2) ──
	TagsNot    map[string]string // != value conditions → ES must_not + term
	TagsExists []string          // != nil conditions → ES exists query
	TagsRegex  map[string]string // =~ regex conditions → ES regexp query
}

// TraceMetricsSeries is a single time series result from a metrics query.
type TraceMetricsSeries struct {
	Labels map[string]string    `json:"metric,omitempty"` // Prometheus-style labels
	Values []TraceMetricsPoint  `json:"values"`
}

// TraceMetricsPoint is a single time-value pair.
type TraceMetricsPoint struct {
	TimestampMs int64   `json:"t"` // milliseconds since epoch
	Value       float64 `json:"v"`
}

// TraceMetricsResult holds the result of a TraceQL metrics query.
type TraceMetricsResult struct {
	Series []TraceMetricsSeries `json:"series"`
}
