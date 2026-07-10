// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

func TestTimeRangeFilterWithUnit_Nano(t *testing.T) {
	tr := storedmodel.TimeRange{
		Start: time.Unix(1000, 500000000),
		End:   time.Unix(2000, 600000000),
	}
	got := TimeRangeFilterWithUnit("startTimeUnixNano", tr, UnitNano)

	rangeClause, ok := got["range"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'range' key, got %v", got)
	}
	fieldClause, ok := rangeClause["startTimeUnixNano"].(map[string]any)
	if !ok {
		t.Fatalf("expected field key, got %v", rangeClause)
	}

	if fieldClause["gte"] != tr.Start.UnixNano() {
		t.Errorf("gte: got %v, want %v", fieldClause["gte"], tr.Start.UnixNano())
	}
	if fieldClause["lte"] != tr.End.UnixNano() {
		t.Errorf("lte: got %v, want %v", fieldClause["lte"], tr.End.UnixNano())
	}
}

func TestTimeRangeFilterWithUnit_Milli(t *testing.T) {
	tr := storedmodel.TimeRange{
		Start: time.Unix(1000, 0),
		End:   time.Unix(2000, 0),
	}
	got := TimeRangeFilterWithUnit("timeUnixMilli", tr, UnitMilli)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["timeUnixMilli"].(map[string]any)

	if fieldClause["gte"] != tr.Start.UnixMilli() {
		t.Errorf("gte: got %v, want %v", fieldClause["gte"], tr.Start.UnixMilli())
	}
	if fieldClause["lte"] != tr.End.UnixMilli() {
		t.Errorf("lte: got %v, want %v", fieldClause["lte"], tr.End.UnixMilli())
	}
}

func TestTimeRangeFilter_ZeroValues(t *testing.T) {
	tests := []struct {
		name    string
		tr      storedmodel.TimeRange
		unit    TimeUnit
		wantKey string
	}{
		{"both zero (nano)", storedmodel.TimeRange{}, UnitNano, "match_all"},
		{"both zero (milli)", storedmodel.TimeRange{}, UnitMilli, "match_all"},
		{"only start (milli)", storedmodel.TimeRange{Start: time.Unix(1000, 0)}, UnitMilli, "range"},
		{"only end (milli)", storedmodel.TimeRange{End: time.Unix(2000, 0)}, UnitMilli, "range"},
		{"both set (milli)", storedmodel.TimeRange{Start: time.Unix(1000, 0), End: time.Unix(2000, 0)}, UnitMilli, "range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TimeRangeFilterWithUnit("field", tt.tr, tt.unit)
			if _, ok := got[tt.wantKey]; !ok {
				t.Errorf("expected key %q in result, got %v", tt.wantKey, got)
			}
		})
	}
}

func TestTimeRangeFilter_OnlyGte(t *testing.T) {
	tr := storedmodel.TimeRange{Start: time.Unix(1000, 0)}
	got := TimeRangeFilterMilli("timeUnixMilli", tr)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["timeUnixMilli"].(map[string]any)

	if _, ok := fieldClause["gte"]; !ok {
		t.Error("expected gte to be present")
	}
	if _, ok := fieldClause["lte"]; ok {
		t.Error("expected lte to be absent when End is zero")
	}
}

func TestTimeRangeFilter_OnlyLte(t *testing.T) {
	tr := storedmodel.TimeRange{End: time.Unix(2000, 0)}
	got := TimeRangeFilterMilli("timeUnixMilli", tr)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["timeUnixMilli"].(map[string]any)

	if _, ok := fieldClause["lte"]; !ok {
		t.Error("expected lte to be present")
	}
	if _, ok := fieldClause["gte"]; ok {
		t.Error("expected gte to be absent when Start is zero")
	}
}

func TestTimeRangeQuery_AlwaysHasBothBounds(t *testing.T) {
	tr := storedmodel.TimeRange{
		Start: time.Unix(1000, 0),
		End:   time.Unix(2000, 0),
	}
	got := TimeRangeQueryMilli("timeUnixMilli", tr)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["timeUnixMilli"].(map[string]any)

	if _, ok := fieldClause["gte"]; !ok {
		t.Error("TimeRangeQuery must always have gte")
	}
	if _, ok := fieldClause["lte"]; !ok {
		t.Error("TimeRangeQuery must always have lte")
	}
}

func TestBackwardCompatibility_TimeRangeFilter(t *testing.T) {
	tr := storedmodel.TimeRange{
		Start: time.Unix(1000, 123456789),
		End:   time.Unix(2000, 987654321),
	}
	got := TimeRangeFilter("startTimeUnixNano", tr)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["startTimeUnixNano"].(map[string]any)

	if fieldClause["gte"] != tr.Start.UnixNano() {
		t.Errorf("backward compat broken: gte got %v, want %v", fieldClause["gte"], tr.Start.UnixNano())
	}
	if fieldClause["lte"] != tr.End.UnixNano() {
		t.Errorf("backward compat broken: lte got %v, want %v", fieldClause["lte"], tr.End.UnixNano())
	}
}

func TestBackwardCompatibility_TimeRangeQuery(t *testing.T) {
	tr := storedmodel.TimeRange{
		Start: time.Unix(1000, 123456789),
		End:   time.Unix(2000, 987654321),
	}
	got := TimeRangeQuery("startTimeUnixNano", tr)

	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["startTimeUnixNano"].(map[string]any)

	if fieldClause["gte"] != tr.Start.UnixNano() {
		t.Errorf("backward compat broken: gte got %v, want %v", fieldClause["gte"], tr.Start.UnixNano())
	}
}

func TestTimeConverter_NanoPrecision(t *testing.T) {
	ts := time.Date(2026, 7, 10, 12, 0, 0, 123456789, time.UTC)
	cnv := timeConverter(UnitNano)
	got := cnv(ts)
	want := ts.UnixNano()

	if got != want {
		t.Errorf("nano converter: got %v, want %v", got, want)
	}
}

func TestTimeConverter_MilliPrecision(t *testing.T) {
	ts := time.Date(2026, 7, 10, 12, 0, 0, 123456789, time.UTC)
	cnv := timeConverter(UnitMilli)
	got := cnv(ts)
	want := ts.UnixMilli()

	if got != want {
		t.Errorf("milli converter: got %v, want %v", got, want)
	}
}

func TestMilliVsNanoValueRange(t *testing.T) {
	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	nanoVal := timeConverter(UnitNano)(ts)
	milliVal := timeConverter(UnitMilli)(ts)

	// Nano value must be roughly 10^6 times the milli value (within rounding)
	// Expected: nanoVal ~= milliVal * 1_000_000
	diff := nanoVal - milliVal*1_000_000
	if diff < 0 {
		diff = -diff
	}
	if diff > 1_000_000 {
		t.Errorf("precision mismatch: nano=%d, milli=%d, diff=%d", nanoVal, milliVal, diff)
	}
}

func TestTimeRangeFilterMilli_DoesNotLeakNano(t *testing.T) {
	// Regression test: metric timeUnixMilli field must not receive nanosecond values.
	// A nanosecond value like 1790000000001234567 (~year 57000) when interpreted
	// as epoch_millis would match zero documents.
	tr := storedmodel.TimeRange{
		Start: time.Unix(1750000000, 0), // ~July 2025
		End:   time.Unix(1760000000, 0), // ~Oct 2025
	}
	got := TimeRangeFilterMilli("timeUnixMilli", tr)
	rangeClause := got["range"].(map[string]any)
	fieldClause := rangeClause["timeUnixMilli"].(map[string]any)

	startVal := fieldClause["gte"].(int64)

	// startVal should be ~1,750,000,000,000 (13 digits, milliseconds)
	// NOT ~1,750,000,000,000,000,000 (19 digits, nanoseconds that cause the bug)
	if startVal < 1_000_000_000_000 || startVal > 9_999_999_999_999 {
		t.Errorf("suspicious value range for epoch_millis: got %d "+
			"(expected ~13 digit epoch_millis value, not 19 digit nanosecond)", startVal)
	}
	if startVal != tr.Start.UnixMilli() {
		t.Errorf("value mismatch: got %v, want %v", startVal, tr.Start.UnixMilli())
	}
}
