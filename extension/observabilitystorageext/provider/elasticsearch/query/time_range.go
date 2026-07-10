// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// TimeUnit — 时间精度枚举
// ═══════════════════════════════════════════════════

// TimeUnit represents the time precision used in ES field storage.
type TimeUnit int

const (
	// UnitNano indicates nanosecond precision (used for trace/log long fields).
	UnitNano TimeUnit = iota
	// UnitMilli indicates millisecond precision (used for metric date fields with epoch_millis).
	UnitMilli
)

// ═══════════════════════════════════════════════════
// 核心通用函数（接受 TimeUnit 参数）
// ═══════════════════════════════════════════════════

// TimeRangeQueryWithUnit returns a range query on the given timestamp field
// using the specified time unit. Always includes both gte and lte bounds.
//
//	ES: {"range": {field: {"gte": value, "lte": value}}}
func TimeRangeQueryWithUnit(field string, tr storedmodel.TimeRange, unit TimeUnit) map[string]any {
	conv := timeConverter(unit)
	return map[string]any{
		"range": map[string]any{
			field: map[string]any{
				"gte": conv(tr.Start),
				"lte": conv(tr.End),
			},
		},
	}
}

// TimeRangeFilterWithUnit returns a time range filter clause for use in bool.must
// using the specified time unit. If both Start and End are zero, returns {"match_all": {}}.
//
//	ES: {"range": {field: {"gte": value, "lte": value}}}
func TimeRangeFilterWithUnit(field string, tr storedmodel.TimeRange, unit TimeUnit) map[string]any {
	conv := timeConverter(unit)
	filter := map[string]any{}
	if !tr.Start.IsZero() {
		filter["gte"] = conv(tr.Start)
	}
	if !tr.End.IsZero() {
		filter["lte"] = conv(tr.End)
	}
	if len(filter) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"range": map[string]any{field: filter},
	}
}

// ═══════════════════════════════════════════════════
// 便捷函数（向后兼容 + 语义明确）
// ═══════════════════════════════════════════════════

// TimeRangeQuery returns a nanosecond-precision range query.
// For trace/log fields stored as long (epoch_nanos).
// Deprecated: Prefer TimeRangeQueryWithUnit for new code.
func TimeRangeQuery(field string, tr storedmodel.TimeRange) map[string]any {
	return TimeRangeQueryWithUnit(field, tr, UnitNano)
}

// TimeRangeFilter returns a nanosecond-precision time range filter.
// For trace/log fields stored as long (epoch_nanos).
// Deprecated: Prefer TimeRangeFilterWithUnit for new code.
func TimeRangeFilter(field string, tr storedmodel.TimeRange) map[string]any {
	return TimeRangeFilterWithUnit(field, tr, UnitNano)
}

// TimeRangeQueryMilli returns a millisecond-precision range query.
// For metric fields stored as ES date type with epoch_millis format.
func TimeRangeQueryMilli(field string, tr storedmodel.TimeRange) map[string]any {
	return TimeRangeQueryWithUnit(field, tr, UnitMilli)
}

// TimeRangeFilterMilli returns a millisecond-precision time range filter.
// For metric fields stored as ES date type with epoch_millis format.
func TimeRangeFilterMilli(field string, tr storedmodel.TimeRange) map[string]any {
	return TimeRangeFilterWithUnit(field, tr, UnitMilli)
}

// ═══════════════════════════════════════════════════
// 内部实现
// ═══════════════════════════════════════════════════

// timeConverter returns a function that converts time.Time to int64
// in the specified unit.
func timeConverter(unit TimeUnit) func(time.Time) int64 {
	switch unit {
	case UnitMilli:
		return func(t time.Time) int64 { return t.UnixMilli() }
	default: // UnitNano
		return func(t time.Time) int64 { return t.UnixNano() }
	}
}
