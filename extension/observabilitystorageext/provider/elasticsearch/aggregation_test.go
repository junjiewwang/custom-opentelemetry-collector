// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"testing"
)

func TestGetAggregation_Valid(t *testing.T) {
	tests := []struct {
		name    string
		aggName string
	}{
		{"avg", "avg"},
		{"sum", "sum"},
		{"max", "max"},
		{"min", "min"},
		{"count", "count"},
		{"last", "last"},
		{"first", "first"},
		{"p50", "p50"},
		{"p90", "p90"},
		{"p95", "p95"},
		{"p99", "p99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg, err := GetAggregation(tt.aggName)
			if err != nil {
				t.Fatalf("GetAggregation(%q) error: %v", tt.aggName, err)
			}
			if agg == nil {
				t.Fatalf("GetAggregation(%q) returned nil", tt.aggName)
			}
		})
	}
}

func TestGetAggregation_Default(t *testing.T) {
	agg, err := GetAggregation("")
	if err != nil {
		t.Fatalf("GetAggregation(\"\") error: %v", err)
	}
	if agg.Name != "avg" {
		t.Fatalf("expected default 'avg', got %q", agg.Name)
	}
}

func TestGetAggregation_Invalid(t *testing.T) {
	_, err := GetAggregation("unknown_func")
	if err == nil {
		t.Fatal("expected error for unknown aggregation")
	}
}

func TestGetAggregation_AllProduceBuildOutput(t *testing.T) {
	for name := range aggregationRegistry {
		agg, err := GetAggregation(name)
		if err != nil {
			t.Fatalf("GetAggregation(%q) error: %v", name, err)
		}
		buildResult := agg.Build("value")
		if buildResult == nil || len(buildResult) == 0 {
			t.Errorf("%s.Build() returned empty/nil result", name)
		}
	}
}

func TestParseSimpleValue(t *testing.T) {
	// avg aggregation result: {"value": 42.5}
	raw := json.RawMessage(`{"value": 42.5}`)
	v := parseSimpleValue(raw)
	if v == nil {
		t.Fatal("expected non-nil value")
	}
	if *v != 42.5 {
		t.Fatalf("expected 42.5, got %v", *v)
	}
}

func TestParseSimpleValue_Null(t *testing.T) {
	raw := json.RawMessage(`{"value": null}`)
	v := parseSimpleValue(raw)
	if v != nil {
		t.Fatalf("expected nil for null value, got %v", *v)
	}
}

func TestParseSimpleValue_Empty(t *testing.T) {
	raw := json.RawMessage(`{}`)
	v := parseSimpleValue(raw)
	if v != nil {
		t.Fatalf("expected nil for empty object, got %v", *v)
	}
}

func TestParseTopHitsValue(t *testing.T) {
	parser := parseTopHitsValue()
	raw := json.RawMessage(`{"hits":{"hits":[{"_source":{"value": 99.9}}]}}`)
	v := parser(raw)
	if v == nil {
		t.Fatal("expected non-nil value")
	}
	if *v != 99.9 {
		t.Fatalf("expected 99.9, got %v", *v)
	}
}

func TestParseTopHitsValue_Empty(t *testing.T) {
	parser := parseTopHitsValue()
	raw := json.RawMessage(`{"hits":{"hits":[]}}`)
	v := parser(raw)
	if v != nil {
		t.Fatalf("expected nil for empty hits, got %v", *v)
	}
}

func TestParsePercentileValue(t *testing.T) {
	// percentiles result: {"values": {"50.0": 10.5, "95.0": 100.0}}
	raw := json.RawMessage(`{"values":{"50.0":10.5,"95.0":100.0}}`)
	v := parsePercentileValue(raw, 95)
	if v == nil {
		t.Fatal("expected non-nil value")
	}
	if *v != 100.0 {
		t.Fatalf("expected 100.0, got %v", *v)
	}
}

func TestParsePercentileValue_NotFound(t *testing.T) {
	raw := json.RawMessage(`{"values":{"50.0":10.5}}`)
	v := parsePercentileValue(raw, 95)
	if v != nil {
		t.Fatalf("expected nil for missing percentile, got %v", *v)
	}
}

func TestValidAggregations(t *testing.T) {
	// Assert on the contents, not a count: a bare number tells whoever adds an
	// aggregation nothing about what broke, and says nothing about whether the
	// right ones are registered.
	got := make(map[string]bool)
	for _, n := range ValidAggregations() {
		got[n] = true
	}
	for _, want := range []string{
		"avg", "sum", "max", "min", "count", "last", "first",
		"p50", "p90", "p95", "p99",
		"stddev", "stdvar",
	} {
		if !got[want] {
			t.Errorf("aggregation %q is not registered", want)
		}
	}
}

func TestBuildPercentile(t *testing.T) {
	builder := buildPercentile(95)
	result := builder("value")

	percentiles, ok := result["percentiles"]
	if !ok {
		t.Fatal("expected percentiles key")
	}
	pMap := percentiles.(map[string]any)
	field := pMap["field"].(string)
	if field != "value" {
		t.Fatalf("expected field 'value', got %q", field)
	}
	percents := pMap["percents"].([]float64)
	if len(percents) != 1 || percents[0] != 95.0 {
		t.Fatalf("expected [95.0], got %v", percents)
	}
}

func TestParseExtendedStatsField(t *testing.T) {
	raw := json.RawMessage(`{"count":3,"avg":4,"variance":2.6666666666666665,"std_deviation":1.632993161855452}`)

	if v := parseExtendedStatsField("std_deviation")(raw); v == nil || *v != 1.632993161855452 {
		t.Fatalf("std_deviation: got %v", v)
	}
	if v := parseExtendedStatsField("variance")(raw); v == nil || *v != 2.6666666666666665 {
		t.Fatalf("variance: got %v", v)
	}
	// ES omits the stats fields for an empty bucket; that must read as "no
	// value" rather than a real zero.
	if v := parseExtendedStatsField("std_deviation")(json.RawMessage(`{"count":0}`)); v != nil {
		t.Fatalf("expected nil for absent field, got %v", *v)
	}
	if v := parseExtendedStatsField("std_deviation")(json.RawMessage(`{"std_deviation":null}`)); v != nil {
		t.Fatalf("expected nil for null field, got %v", *v)
	}
	if v := parseExtendedStatsField("std_deviation")(json.RawMessage(`not json`)); v != nil {
		t.Fatalf("expected nil for malformed json, got %v", *v)
	}
}

func TestStddevStdvarBuildExtendedStats(t *testing.T) {
	for _, name := range []string{"stddev", "stdvar"} {
		agg, err := GetAggregation(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		built := agg.Build("value")
		if _, ok := built["extended_stats"]; !ok {
			t.Fatalf("%s must build an extended_stats aggregation, got %v", name, built)
		}
	}
}
