// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslatePromQLRegex(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected RegexTranslation
	}{
		{
			name:    "empty pattern",
			pattern: "",
			expected: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: "",
			},
		},
		{
			name:    "simple literal without dots",
			pattern: "POST /api/v2/prometheus/api/v1/query",
			expected: RegexTranslation{
				Strategy:      StrategyTerm,
				Values:        []string{"POST /api/v2/prometheus/api/v1/query"},
				OriginalRegex: "POST /api/v2/prometheus/api/v1/query",
			},
		},
		{
			name:    "literal with escaped dots",
			pattern: `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`,
			expected: RegexTranslation{
				Strategy:      StrategyTerm,
				Values:        []string{"opentelemetry.proto.collector.logs.v1.LogsService/Export"},
				OriginalRegex: `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`,
			},
		},
		{
			name:    "alternation of literals",
			pattern: `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export|POST /api/v2/prometheus/api/v1/query`,
			expected: RegexTranslation{
				Strategy: StrategyTerms,
				Values: []string{
					"opentelemetry.proto.collector.logs.v1.LogsService/Export",
					"opentelemetry.proto.collector.trace.v1.TraceService/Export",
					"POST /api/v2/prometheus/api/v1/query",
				},
				OriginalRegex: `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export|POST /api/v2/prometheus/api/v1/query`,
			},
		},
		{
			name:    "prefix pattern",
			pattern: `opentelemetry\.proto\.collector.*`,
			expected: RegexTranslation{
				Strategy:      StrategyPrefix,
				Prefix:        "opentelemetry.proto.collector",
				OriginalRegex: `opentelemetry\.proto\.collector.*`,
			},
		},
		{
			name:    "complex regex with character class",
			pattern: `[a-z]+\.service`,
			expected: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: `[a-z]+\.service`,
			},
		},
		{
			name:    "complex regex with dot-star in middle",
			pattern: `opentelemetry.*Export`,
			expected: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: `opentelemetry.*Export`,
			},
		},
		{
			name:    "complex regex with quantifier",
			pattern: `service\.name+`,
			expected: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: `service\.name+`,
			},
		},
		{
			name:    "alternation with one complex alternative",
			pattern: `simple_value|complex.*pattern`,
			expected: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: `simple_value|complex.*pattern`,
			},
		},
		{
			name:    "simple value without any special chars",
			pattern: "my-service-name",
			expected: RegexTranslation{
				Strategy:      StrategyTerm,
				Values:        []string{"my-service-name"},
				OriginalRegex: "my-service-name",
			},
		},
		{
			name:    "alternation of simple values",
			pattern: "service-a|service-b|service-c",
			expected: RegexTranslation{
				Strategy:      StrategyTerms,
				Values:        []string{"service-a", "service-b", "service-c"},
				OriginalRegex: "service-a|service-b|service-c",
			},
		},
		{
			name:    "value containing forward slash",
			pattern: `grpc\.health\.v1\.Health/Check`,
			expected: RegexTranslation{
				Strategy:      StrategyTerm,
				Values:        []string{"grpc.health.v1.Health/Check"},
				OriginalRegex: `grpc\.health\.v1\.Health/Check`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TranslatePromQLRegex(tt.pattern)
			assert.Equal(t, tt.expected.Strategy, result.Strategy, "strategy mismatch")
			assert.Equal(t, tt.expected.OriginalRegex, result.OriginalRegex, "original regex mismatch")
			if tt.expected.Values != nil {
				assert.Equal(t, tt.expected.Values, result.Values, "values mismatch")
			}
			if tt.expected.Prefix != "" {
				assert.Equal(t, tt.expected.Prefix, result.Prefix, "prefix mismatch")
			}
		})
	}
}

func TestBuildESClauseFromRegex(t *testing.T) {
	tests := []struct {
		name        string
		field       string
		translation RegexTranslation
		expected    map[string]any
		isNil       bool
	}{
		{
			name:  "term strategy",
			field: "labels.span.name",
			translation: RegexTranslation{
				Strategy: StrategyTerm,
				Values:   []string{"POST /api/v1/query"},
			},
			expected: map[string]any{
				"term": map[string]any{"labels.span.name": "POST /api/v1/query"},
			},
		},
		{
			name:  "terms strategy",
			field: "labels.service.name",
			translation: RegexTranslation{
				Strategy: StrategyTerms,
				Values:   []string{"service-a", "service-b", "service-c"},
			},
			expected: map[string]any{
				"terms": map[string]any{"labels.service.name": []string{"service-a", "service-b", "service-c"}},
			},
		},
		{
			name:  "prefix strategy",
			field: "labels.span.name",
			translation: RegexTranslation{
				Strategy: StrategyPrefix,
				Prefix:   "opentelemetry.proto",
			},
			expected: map[string]any{
				"prefix": map[string]any{"labels.span.name": "opentelemetry.proto"},
			},
		},
		{
			name:  "unsupported strategy returns nil",
			field: "labels.span.name",
			translation: RegexTranslation{
				Strategy:      StrategyUnsupported,
				OriginalRegex: "complex.*regex",
			},
			isNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildESClauseFromRegex(tt.field, tt.translation)
			if tt.isNil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestPostFilterByRegex(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		pattern string
		match   bool
	}{
		{
			name:    "exact match",
			value:   "opentelemetry.proto.collector.logs.v1.LogsService/Export",
			pattern: `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`,
			match:   true,
		},
		{
			name:    "alternation match first",
			value:   "service-a",
			pattern: "service-a|service-b",
			match:   true,
		},
		{
			name:    "alternation match second",
			value:   "service-b",
			pattern: "service-a|service-b",
			match:   true,
		},
		{
			name:    "alternation no match",
			value:   "service-c",
			pattern: "service-a|service-b",
			match:   false,
		},
		{
			name:    "wildcard match",
			value:   "opentelemetry.proto.collector.logs.v1.LogsService/Export",
			pattern: `opentelemetry\.proto\.collector.*`,
			match:   true,
		},
		{
			name:    "partial value does not match (anchored)",
			value:   "prefix-opentelemetry.proto",
			pattern: `opentelemetry\.proto`,
			match:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PostFilterByRegex(tt.value, tt.pattern)
			assert.Equal(t, tt.match, result)
		})
	}
}

func TestSplitUnescapedPipe(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "no pipe",
			pattern:  "simple",
			expected: []string{"simple"},
		},
		{
			name:     "single pipe",
			pattern:  "a|b",
			expected: []string{"a", "b"},
		},
		{
			name:     "multiple pipes",
			pattern:  "a|b|c|d",
			expected: []string{"a", "b", "c", "d"},
		},
		{
			name:     "escaped backslash before pipe",
			pattern:  `a\.b|c\.d`,
			expected: []string{`a\.b`, `c\.d`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitUnescapedPipe(tt.pattern)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsLiteralWithEscapedDots(t *testing.T) {
	tests := []struct {
		pattern  string
		expected bool
	}{
		{"simple", true},
		{`simple\.value`, true},
		{`a\.b\.c`, true},
		{"POST /api/v1/query", true},
		{`contains\ddigit`, false},  // \d is not \.
		{"has*star", false},         // * is metachar
		{"has+plus", false},         // + is metachar
		{"has.dot", false},          // unescaped dot is metachar
		{"has[bracket", false},      // [ is metachar
		{`has\.escaped\.dots`, true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			assert.Equal(t, tt.expected, isLiteralWithEscapedDots(tt.pattern))
		})
	}
}

func TestUnescapePromQLRegex(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`simple`, "simple"},
		{`opentelemetry\.proto`, "opentelemetry.proto"},
		{`a\.b\.c\.d`, "a.b.c.d"},
		{`no-escape-needed`, "no-escape-needed"},
		{`slash/path`, "slash/path"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, unescapePromQLRegex(tt.input))
		})
	}
}
