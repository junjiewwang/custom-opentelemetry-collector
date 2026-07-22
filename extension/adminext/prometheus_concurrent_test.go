// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitPipeLiterals(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "simple pipe alternation",
			pattern:  "GET order-service-route|POST /api/v1",
			expected: []string{"GET order-service-route", "POST /api/v1"},
		},
		{
			name:     "escaped dots in gRPC methods",
			pattern:  `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export`,
			expected: []string{"opentelemetry.proto.collector.logs.v1.LogsService/Export", "opentelemetry.proto.collector.trace.v1.TraceService/Export"},
		},
		{
			name:     "mixed literal and escaped dots",
			pattern:  `GET order-service-route|user\.UserService/GetAllUserInfo|market\.MarketService/GetAllProductInfo`,
			expected: []string{"GET order-service-route", "user.UserService/GetAllUserInfo", "market.MarketService/GetAllProductInfo"},
		},
		{
			name:     "single value - no split",
			pattern:  `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`,
			expected: nil,
		},
		{
			name:     "contains regex metachar - no split",
			pattern:  `foo.*bar|baz`,
			expected: nil,
		},
		{
			name:     "contains unescaped dot - no split",
			pattern:  `foo.bar|baz`,
			expected: nil,
		},
		{
			name:     "contains bracket - no split",
			pattern:  `[a-z]+|baz`,
			expected: nil,
		},
		{
			name:     "five gRPC methods",
			pattern:  `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export|GET order-service-route|user\.UserService/GetAllUserInfo|market\.MarketService/GetAllProductInfo`,
			expected: []string{"opentelemetry.proto.collector.logs.v1.LogsService/Export", "opentelemetry.proto.collector.trace.v1.TraceService/Export", "GET order-service-route", "user.UserService/GetAllUserInfo", "market.MarketService/GetAllProductInfo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitPipeLiterals(tt.pattern)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindSplitCandidate(t *testing.T) {
	tests := []struct {
		name       string
		labelMatch map[string]string
		minTerms   int
		expectKey  string
		expectLen  int
	}{
		{
			name: "finds splittable regex",
			labelMatch: map[string]string{
				"span_name": `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|GET order-service-route`,
			},
			minTerms:  2,
			expectKey: "span_name",
			expectLen: 2,
		},
		{
			name: "no splittable regex - single value",
			labelMatch: map[string]string{
				"span_name": `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`,
			},
			minTerms:  2,
			expectKey: "",
			expectLen: 0,
		},
		{
			name: "no splittable regex - contains regex",
			labelMatch: map[string]string{
				"span_name": `foo.*bar|baz`,
			},
			minTerms:  2,
			expectKey: "",
			expectLen: 0,
		},
		{
			name:       "nil labelMatch",
			labelMatch: nil,
			minTerms:   2,
			expectKey:  "",
			expectLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := findSplitCandidate(tt.labelMatch, tt.minTerms)
			if tt.expectLen == 0 {
				assert.Nil(t, candidate)
			} else {
				require.NotNil(t, candidate)
				assert.Equal(t, tt.expectKey, candidate.Key)
				assert.Equal(t, tt.expectLen, len(candidate.Values))
			}
		})
	}
}

func TestCloneLabelMatchWithSingleTerm(t *testing.T) {
	labelMatch := map[string]string{
		"span_name":   "foo|bar|baz",
		"http_method": "GET|POST",
	}
	result := cloneLabelMatchWithSingleTerm(labelMatch, "span_name", "foo")

	// Original should not be modified.
	assert.Len(t, labelMatch, 2)
	assert.Equal(t, "foo|bar|baz", labelMatch["span_name"])

	// Result should have the single term value for span_name.
	assert.Equal(t, "foo", result["span_name"])
	assert.Equal(t, "GET|POST", result["http_method"])
}

func TestCloneLabelsWithTerm(t *testing.T) {
	labels := map[string]string{"service_name": "my-svc", "status_code": "STATUS_CODE_ERROR"}
	result := cloneLabelsWithTerm(labels, "span_name", "GET /api")

	// Original should not be modified.
	assert.Len(t, labels, 2)

	// Result should have the new key.
	assert.Equal(t, "GET /api", result["span_name"])
	assert.Equal(t, "my-svc", result["service_name"])
	assert.Equal(t, "STATUS_CODE_ERROR", result["status_code"])
}

func TestCloneLabelMatchWithout(t *testing.T) {
	tests := []struct {
		name       string
		labelMatch map[string]string
		removeKey  string
		expected   map[string]string
	}{
		{
			name:       "remove only key - returns nil",
			labelMatch: map[string]string{"span_name": "foo|bar"},
			removeKey:  "span_name",
			expected:   nil,
		},
		{
			name:       "remove one of two keys",
			labelMatch: map[string]string{"span_name": "foo|bar", "http_method": "GET|POST"},
			removeKey:  "span_name",
			expected:   map[string]string{"http_method": "GET|POST"},
		},
		{
			name:       "nil input",
			labelMatch: nil,
			removeKey:  "span_name",
			expected:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cloneLabelMatchWithout(tt.labelMatch, tt.removeKey)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsLiteralOrEscapedDots(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"GET order-service-route", true},
		{`opentelemetry\.proto\.collector`, true},
		{"foo.bar", false},           // unescaped dot
		{"foo*bar", false},           // regex metachar
		{"foo[0-9]", false},          // bracket
		{`user\.UserService/Get`, true},
		{"simple-string", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, isLiteralOrEscapedDots(tt.input))
		})
	}
}

func TestSplitUnescapedPipeLocal(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple pipe",
			input:    "a|b|c",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "escaped pipe (not split)",
			input:    `a\|b|c`,
			expected: []string{`a\|b`, "c"},
		},
		{
			name:     "no pipe",
			input:    "abc",
			expected: []string{"abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, splitUnescapedPipeLocal(tt.input))
		})
	}
}
