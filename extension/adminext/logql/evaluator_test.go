// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"testing"
	"time"
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
