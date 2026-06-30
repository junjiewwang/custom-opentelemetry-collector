// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// TimeRangeQuery returns a range query on the given timestamp field.
// Uses int64 nanosecond values (aligned with storedmodel long fields).
//
//	ES: {"range": {field: {"gte": startNanos, "lte": endNanos}}}
func TimeRangeQuery(field string, tr storedmodel.TimeRange) map[string]any {
	return map[string]any{
		"range": map[string]any{
			field: map[string]any{
				"gte": tr.Start.UnixNano(),
				"lte": tr.End.UnixNano(),
			},
		},
	}
}

// TimeRangeFilter returns a time range filter clause for use in bool.must.
// If both Start and End are zero, returns {"match_all": {}}.
// Uses int64 nanosecond values.
//
//	ES: {"range": {field: {"gte": startNanos, "lte": endNanos}}}
func TimeRangeFilter(field string, tr storedmodel.TimeRange) map[string]any {
	filter := map[string]any{}
	if !tr.Start.IsZero() {
		filter["gte"] = tr.Start.UnixNano()
	}
	if !tr.End.IsZero() {
		filter["lte"] = tr.End.UnixNano()
	}
	if len(filter) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"range": map[string]any{field: filter},
	}
}
