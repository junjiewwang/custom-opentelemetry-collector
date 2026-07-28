// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvaluate_RegexFilterGoesToRegexFields(t *testing.T) {
	// Verify regex filters (|~, !~) route to RegexFilters/NotRegexFields
	// instead of the general Query field (which uses ES match query).
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
			},
		},
		LineFilters: []LineFilter{
			{Type: FilterRegex, Pattern: "(?i)order"},
			{Type: FilterNotRegex, Pattern: "debug"},
		},
		Start: time.Unix(0, 1784792466051000000),
		End:   time.Unix(0, 1784793366051000000),
		Limit: 100,
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	// Query field must be empty — no contains filters, only regex filters.
	if result.Query != "" {
		t.Errorf("expected empty Query field, got %q — regex filters should not go into Query", result.Query)
	}

	// Regex filters must be present.
	if len(result.RegexFilters) != 1 {
		t.Errorf("expected 1 RegexFilter, got %d", len(result.RegexFilters))
	} else if result.RegexFilters[0] != "(?i)order" {
		t.Errorf("expected RegexFilter '(?i)order', got %q", result.RegexFilters[0])
	}

	// NotRegexFilter must be present.
	if len(result.NotRegexFilters) != 1 {
		t.Errorf("expected 1 NotRegexFilter, got %d", len(result.NotRegexFilters))
	} else if result.NotRegexFilters[0] != "debug" {
		t.Errorf("expected NotRegexFilter 'debug', got %q", result.NotRegexFilters[0])
	}
}

func TestEvaluate_ContainsFilterStillGoesToQuery(t *testing.T) {
	// Verify contains filters (|=) still route to Query field.
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
			},
		},
		LineFilters: []LineFilter{
			{Type: FilterContains, Pattern: "error"},
		},
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	if result.Query != `"error"` {
		t.Errorf("expected Query to be %q, got %q", `"error"`, result.Query)
	}
	if len(result.RegexFilters) != 0 {
		t.Errorf("expected 0 RegexFilters, got %d", len(result.RegexFilters))
	}
}

func TestEvaluate_MixedFilters(t *testing.T) {
	// Verify mixed contains + regex filters handle correctly.
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
			},
		},
		LineFilters: []LineFilter{
			{Type: FilterContains, Pattern: "error"},
			{Type: FilterRegex, Pattern: "(?i)order"},
			{Type: FilterContains, Pattern: "timeout"},
			{Type: FilterNotRegex, Pattern: "debug"},
		},
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	// Query should contain both contains filters (space-separated).
	expectedQuery := `"error" "timeout"`
	if result.Query != expectedQuery {
		t.Errorf("expected Query %q, got %q", expectedQuery, result.Query)
	}

	// Regex filters.
	if len(result.RegexFilters) != 1 {
		t.Errorf("expected 1 RegexFilter, got %d", len(result.RegexFilters))
	}
	if len(result.NotRegexFilters) != 1 {
		t.Errorf("expected 1 NotRegexFilter, got %d", len(result.NotRegexFilters))
	}
}

func TestEvaluate_NoRegexFilter_FieldsEmpty(t *testing.T) {
	// Verify no regression: without regex filters, new fields stay empty.
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
			},
		},
		LineFilters: []LineFilter{
			{Type: FilterContains, Pattern: "error"},
		},
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	if result.RegexFilters != nil {
		t.Errorf("expected nil RegexFilters when no regex filters, got %v", result.RegexFilters)
	}
	if result.NotRegexFilters != nil {
		t.Errorf("expected nil NotRegexFilters, got %v", result.NotRegexFilters)
	}
}

func TestEvaluate_EmptyPatternSkipped(t *testing.T) {
	// Empty pattern (=="=" in Loki) means match-all and should be skipped.
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
			},
		},
		LineFilters: []LineFilter{
			{Type: FilterRegex, Pattern: ""},
			{Type: FilterContains, Pattern: ""},
		},
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	if result.Query != "" {
		t.Errorf("expected empty Query for empty patterns, got %q", result.Query)
	}
	if len(result.RegexFilters) != 0 {
		t.Errorf("expected 0 RegexFilters for empty pattern, got %d", len(result.RegexFilters))
	}
}

func TestEvaluate_LabelsMapping(t *testing.T) {
	// Verify all label matcher types are correctly mapped.
	lq := &LogQLQuery{
		StreamSelector: StreamSelector{
			Matchers: []LabelMatcher{
				{Name: "service_name", Type: MatchEqual, Value: "my-app"},
				{Name: "level", Type: MatchNotEqual, Value: "debug"},
				{Name: "env", Type: MatchRegex, Value: "prod.*"},
				{Name: "host", Type: MatchNotRegex, Value: "test.*"},
			},
		},
		Start: time.Unix(0, 1784792466051000000),
		End:   time.Unix(0, 1784793366051000000),
	}

	ev := &Evaluator{}
	result := ev.Evaluate(lq)

	// Labels (exact match).
	if v, ok := result.Labels["service_name"]; !ok || v != "my-app" {
		t.Errorf("labels: expected service_name=my-app, got %v", result.Labels)
	}

	// LabelNot.
	if v, ok := result.LabelNot["level"]; !ok || v != "debug" {
		t.Errorf("LabelNot: expected level=debug, got %v", result.LabelNot)
	}

	// LabelMatch (regex).
	if v, ok := result.LabelMatch["env"]; !ok || v != "prod.*" {
		t.Errorf("LabelMatch: expected env=prod.*, got %v", result.LabelMatch)
	}

	// LabelNotMatch.
	if v, ok := result.LabelNotMatch["host"]; !ok || v != "test.*" {
		t.Errorf("LabelNotMatch: expected host=test.*, got %v", result.LabelNotMatch)
	}

}

// TestEvaluate_LabelFormatContainsTranslation verifies that label_format with
// {{ contains "VALUE" __line__ }} template is translated to a FilterContains
// line filter (ES match_phrase) in the output LogQuery.Query — but ONLY when
// the same branch also has a PipelineLabelFilter for that label (matching the
// Grafana trace-to-logs pattern: | label_format X=... | X="true").
// Without the corresponding label filter, the label_format is a shared-prefix
// artifact from OR splitting and must NOT add a body constraint.
func TestEvaluate_LabelFormatContainsTranslation(t *testing.T) {
	tests := []struct {
		name      string
		pipeline  []PipelineStage
		wantQuery string
		wantEmpty bool
	}{
		{
			name: "contains template + label filter → match_phrase",
			pipeline: []PipelineStage{
				{Type: PipelineLabelFormat, LabelFormat: &LabelMatcher{
					Name:  "log_line_contains_trace_id",
					Value: `{{ contains "1895de2b356e8bc281505bb48d142396" __line__ }}`,
				}},
				{Type: PipelineLabelFilter, LabelFilter: &LabelMatcher{
					Name: "log_line_contains_trace_id", Type: MatchEqual, Value: "true",
				}},
			},
			wantQuery: `"1895de2b356e8bc281505bb48d142396"`,
		},
		{
			name: "contains template WITHOUT label filter (OR branch artifact) → not translated",
			pipeline: []PipelineStage{
				{Type: PipelineLabelFormat, LabelFormat: &LabelMatcher{
					Name:  "log_line_contains_trace_id",
					Value: `{{ contains "1895de2b356e8bc281505bb48d142396" __line__ }}`,
				}},
				// NO PipelineLabelFilter for log_line_contains_trace_id — this is
				// the OR branch that filters by trace_id instead.
				{Type: PipelineLabelFilter, LabelFilter: &LabelMatcher{
					Name: "trace_id", Type: MatchEqual, Value: "1895de2b356e8bc281505bb48d142396",
				}},
			},
			wantEmpty: true,
		},
		{
			name: "non-template label_format → not translated",
			pipeline: []PipelineStage{
				{Type: PipelineLabelFormat, LabelFormat: &LabelMatcher{
					Name: "renamed", Value: "original",
				}},
			},
			wantEmpty: true,
		},
		{
			name: "contains template with extra spaces + label filter",
			pipeline: []PipelineStage{
				{Type: PipelineLabelFormat, LabelFormat: &LabelMatcher{
					Name:  "found",
					Value: `{{ contains  "abc123"  __line__  }}`,
				}},
				{Type: PipelineLabelFilter, LabelFilter: &LabelMatcher{
					Name: "found", Type: MatchEqual, Value: "true",
				}},
			},
			wantQuery: `"abc123"`,
		},
		{
			name:      "empty pipeline",
			pipeline:  nil,
			wantEmpty: true,
		},
	}

	ev := &Evaluator{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lq := &LogQLQuery{
				StreamSelector: StreamSelector{
					Matchers: []LabelMatcher{{Name: "service_name", Type: MatchEqual, Value: "test"}},
				},
				Pipeline: tt.pipeline,
			}
			result := ev.Evaluate(lq)
			if tt.wantEmpty {
				assert.Empty(t, result.Query)
			} else {
				assert.Contains(t, result.Query, tt.wantQuery)
			}
		})
	}
}

// TestExtractContainsValue tests the template pattern extraction.
func TestExtractContainsValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{{ contains "abc123" __line__ }}`, "abc123"},
		{`{{ contains  "abc123"  __line__  }}`, "abc123"},   // extra spaces
		{`{{contains "abc123" __line__}}`, "abc123"},         // no spaces
		{`not a template`, ""},
		{`{{ upper "abc" }}`, ""},
		{``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractContainsValue(tt.input))
		})
	}
}
