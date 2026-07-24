// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import "time"

// SignalSpec describes the ES storage configuration for one signal type.
// It is the single source of truth for per-signal dispatch that was
// previously spread across switch statements in purge_shared.go, purger.go,
// and usage.go. Adding a new signal means appending one entry here.
type SignalSpec struct {
	// Signal is the canonical signal name ("trace" | "metric" | "log").
	Signal string
	// Prefix returns the configured ES index prefix for this signal.
	Prefix func(*Config) string
	// DateFormat returns the configured index date format for this signal.
	DateFormat func(*Config) string
	// TimeField is the canonical ES timestamp field (a Field* constant) used
	// for time-range queries and delete_by_query.
	TimeField string
	// BoundNano selects the timestamp-bound serialization: true → UnixNano
	// (for long nanosecond fields); false → UnixMilli (for epoch_millis date fields).
	BoundNano bool
}

// signalSpecs is the table of all supported signals. The TimeField values must
// match the index template mappings in admin.go (enforced by
// field_consistency_test.go).
var signalSpecs = []SignalSpec{
	{
		Signal:     "trace",
		Prefix:     func(c *Config) string { return c.Traces.IndexPrefix },
		DateFormat: func(c *Config) string { return c.Traces.IndexDateFormat },
		TimeField:  FieldStartTimeUnixNano,
		BoundNano:  true,
	},
	{
		Signal:     "metric",
		Prefix:     func(c *Config) string { return c.Metrics.IndexPrefix },
		DateFormat: func(c *Config) string { return c.Metrics.IndexDateFormat },
		TimeField:  FieldMetricTimeUnixMilli,
		BoundNano:  false,
	},
	{
		Signal:     "log",
		Prefix:     func(c *Config) string { return c.Logs.IndexPrefix },
		DateFormat: func(c *Config) string { return c.Logs.IndexDateFormat },
		TimeField:  FieldLogTimeUnixNano,
		BoundNano:  true,
	},
}

// specFor returns the SignalSpec for the given signal string, or ok=false.
func specFor(signal string) (SignalSpec, bool) {
	for _, s := range signalSpecs {
		if s.Signal == signal {
			return s, true
		}
	}
	return SignalSpec{}, false
}

// signalPrefix returns the configured ES index prefix for the signal, or "" if
// the signal is unknown.
func signalPrefix(c *Config, signal string) string {
	if s, ok := specFor(signal); ok && c != nil {
		return s.Prefix(c)
	}
	return ""
}

// signalDateFormat returns the configured index date format for the signal, or
// "" if unknown.
func signalDateFormat(c *Config, signal string) string {
	if s, ok := specFor(signal); ok && c != nil {
		return s.DateFormat(c)
	}
	return ""
}

// signalTimestampField returns the ES timestamp field used for time-range
// delete_by_query on the given signal. The values match the index templates
// in admin.go (enforced by field_consistency_test.go):
//   - trace  → startTimeUnixNano (long, nanoseconds)
//   - metric → timeUnixMilli     (date, epoch_millis)
//   - log    → timeUnixNano      (long, nanoseconds)
//
// All three are numeric, so callers must use an integer bound (see
// signalTimestampBound), not an RFC3339 string — a string bound against a long
// field is a type mismatch that ES silently matches nothing for.
func signalTimestampField(signal string) string {
	if s, ok := specFor(signal); ok {
		return s.TimeField
	}
	return FieldMetricTimeUnixMilli
}

// signalTimestampBound returns the integer comparison value for a time-range
// delete_by_query, matching the ES field type returned by signalTimestampField:
// trace/log → UnixNano (long nanos); metric → UnixMilli (epoch_millis).
func signalTimestampBound(signal string, before time.Time) any {
	if s, ok := specFor(signal); ok && !s.BoundNano {
		return before.UnixMilli()
	}
	return before.UnixNano() // trace, log, and unknown use nanoseconds
}
