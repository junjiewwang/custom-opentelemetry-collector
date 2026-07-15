// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package unitconv provides pure-function unit normalization for Tempo metrics.
//
// Tempo protocol requires duration values in SECONDS (Grafana uses setUnit('s')).
// However, different data sources store duration in different units:
//   - ES TraceReader (durationNano field): nanoseconds
//   - MetricReader (duration_milliseconds): milliseconds
//
// This package provides a single, testable set of functions to normalize any
// duration value to seconds, regardless of source unit. All conversion logic
// lives here — callers simply declare the source unit and get seconds back.
//
// Design principles:
//   - Pure functions: no side effects, no I/O, no logger — 100% unit-testable.
//   - Single Responsibility: only knows about unit conversion, nothing about ES or HTTP.
//   - Open/Closed: add new units by adding a DurationSourceUnit constant + switch case,
//     without modifying existing callers.
//   - Zero dependencies: this package imports nothing external; safe from circular imports.
package unitconv

// DurationSourceUnit identifies the unit of a raw duration value from storage.
type DurationSourceUnit int

const (
	// DurationUnitNone indicates the value has no duration semantics (e.g., rate counts).
	// No conversion is applied.
	DurationUnitNone DurationSourceUnit = iota

	// DurationUnitNanoseconds indicates the value is in nanoseconds (1e-9 seconds).
	// Used by ES TraceReader's durationNano field.
	DurationUnitNanoseconds

	// DurationUnitMilliseconds indicates the value is in milliseconds (1e-3 seconds).
	// Used by MetricReader's duration_milliseconds pre-aggregated metric.
	DurationUnitMilliseconds

	// DurationUnitSeconds indicates the value is already in seconds.
	// No conversion is applied.
	DurationUnitSeconds
)

// ToSeconds converts a raw duration value from the given source unit to seconds
// (the Tempo protocol standard unit for duration).
//
// This is a pure function with no side effects.
func ToSeconds(value float64, sourceUnit DurationSourceUnit) float64 {
	switch sourceUnit {
	case DurationUnitNanoseconds:
		return value / 1e9
	case DurationUnitMilliseconds:
		return value / 1e3
	case DurationUnitSeconds, DurationUnitNone:
		return value
	default:
		return value
	}
}

// NormalizeSlice normalizes a slice of duration values in-place.
// This is a convenience function for batch conversion (e.g., MetricReader results).
func NormalizeSlice(values []float64, sourceUnit DurationSourceUnit) {
	if sourceUnit == DurationUnitNone || sourceUnit == DurationUnitSeconds {
		return // fast path: no conversion needed
	}
	for i := range values {
		values[i] = ToSeconds(values[i], sourceUnit)
	}
}

// IsDurationFunction returns true if the given TraceQL function operates on
// duration values and requires unit normalization.
//
// Functions that return duration values:
//   - quantile_over_time(duration, ...) — percentile of span duration
//   - histogram_over_time(duration)     — average duration in histogram buckets
//
// Functions that do NOT return duration values:
//   - rate()           — returns spans/second (already unit-correct)
//   - count_over_time  — returns count (no unit)
func IsDurationFunction(function, field string) bool {
	if field != "duration" {
		return false
	}
	switch function {
	case "quantile_over_time", "histogram_over_time":
		return true
	default:
		return false
	}
}

// SourceUnitForTraceReader returns the DurationSourceUnit for data coming from
// the TraceReader path (real-time ES aggregation on raw span data).
//
// The ES durationNano field stores values in nanoseconds.
func SourceUnitForTraceReader(function, field string) DurationSourceUnit {
	if IsDurationFunction(function, field) {
		return DurationUnitNanoseconds
	}
	return DurationUnitNone
}

// SourceUnitForMetricReader returns the DurationSourceUnit for data coming from
// the MetricReader path (pre-aggregated spanmetrics data).
//
// The duration_milliseconds metric stores values in milliseconds.
func SourceUnitForMetricReader(function, field string) DurationSourceUnit {
	if IsDurationFunction(function, field) {
		return DurationUnitMilliseconds
	}
	return DurationUnitNone
}
