// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildTraceSearchQuery_IsRoot verifies that IsRoot=true produces the
// correct ES query: a bool.should with must_not{exists} OR term("0000000000000000").
func TestBuildTraceSearchQuery_IsRoot(t *testing.T) {
	r := &TraceReader{} // buildTraceSearchQuery only uses the tq parameter
	now := time.Now()

	query := TraceQuery{
		IsRoot: true,
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)

	// Marshal to JSON for inspection
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain must_not + exists for new data (field absent)
	assert.Contains(t, queryStr, `"must_not"`)
	assert.Contains(t, queryStr, `"exists"`)
	assert.Contains(t, queryStr, `"field":"parentSpanId"`)

	// Should contain term match for historical data (zero-value string)
	assert.Contains(t, queryStr, `"term"`)
	assert.Contains(t, queryStr, `"parentSpanId":"0000000000000000"`)

	// Should contain should with minimum_should_match
	assert.Contains(t, queryStr, `"should"`)
	assert.Contains(t, queryStr, `"minimum_should_match"`)
}

// TestBuildTraceSearchQuery_IsRoot_StructureCheck verifies the exact DSL structure
// for root span filtering.
func TestBuildTraceSearchQuery_IsRoot_StructureCheck(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		IsRoot: true,
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)

	// Navigate the query structure: bool.must[1] should be the IsRoot clause
	boolQ, ok := esQuery["bool"].(map[string]any)
	require.True(t, ok, "top-level should be bool query")

	mustClauses, ok := boolQ["must"].([]map[string]any)
	require.True(t, ok, "should have must clauses")
	require.GreaterOrEqual(t, len(mustClauses), 2, "should have at least time range + isRoot")

	// Find the should clause (IsRoot filter)
	var rootClause map[string]any
	for _, clause := range mustClauses {
		if boolInner, hasBool := clause["bool"]; hasBool {
			if innerMap, ok := boolInner.(map[string]any); ok {
				if _, hasShould := innerMap["should"]; hasShould {
					rootClause = innerMap
					break
				}
			}
		}
	}
	require.NotNil(t, rootClause, "should find a bool.should clause for IsRoot")

	// Verify minimum_should_match = 1
	assert.Equal(t, 1, rootClause["minimum_should_match"])

	// Verify the should array has 2 sub-clauses
	shouldArr, ok := rootClause["should"].([]map[string]any)
	require.True(t, ok)
	assert.Len(t, shouldArr, 2, "should have 2 alternatives: must_not(exists) and term(zero)")
}

// TestBuildTraceSearchQuery_NotRoot verifies that IsRoot=false does NOT add root filtering.
func TestBuildTraceSearchQuery_NotRoot(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		IsRoot: false,
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)

	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should NOT contain parentSpanId filtering
	assert.NotContains(t, queryStr, `"parentSpanId"`)
}

// ═══════════════════════════════════════════════════
// Sprint 2: TagsNot / TagsExists / TagsRegex Tests
// ═══════════════════════════════════════════════════

func TestBuildTraceSearchQuery_TagsNot(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		TagsNot: map[string]string{
			"name":    "foo",
			"http.method": "OPTIONS",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain must_not clauses
	assert.Contains(t, queryStr, `"must_not"`)
	// Intrinsic name → FieldName
	assert.Contains(t, queryStr, `"name":"foo"`)
	// Custom attribute → attributes.xxx
	assert.Contains(t, queryStr, `"attributes.http.method":"OPTIONS"`)
}

func TestBuildTraceSearchQuery_TagsExists(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		TagsExists: []string{"service.name", "http.method"},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain exists queries
	assert.Contains(t, queryStr, `"exists"`)
	// Intrinsic service.name → serviceName
	assert.Contains(t, queryStr, `"field":"serviceName"`)
	// Custom attribute → should check attributes and resource
	assert.Contains(t, queryStr, `"should"`)
	assert.Contains(t, queryStr, `"attributes.http.method"`)
	assert.Contains(t, queryStr, `"resource.http.method"`)
}

func TestBuildTraceSearchQuery_TagsRegex(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		TagsRegex: map[string]string{
			"name":        "^api",
			"http.method": "GET|POST",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain regexp queries
	assert.Contains(t, queryStr, `"regexp"`)
	// Intrinsic name → FieldName regexp
	assert.Contains(t, queryStr, `"name":{"value":"^api"`)
	// Custom attribute → attributes.http.method regexp
	assert.Contains(t, queryStr, `"attributes.http.method":{"value":"GET|POST"`)
}

// ═══════════════════════════════════════════════════
// Sprint 3: status.message nested intrinsic
// ═══════════════════════════════════════════════════

func TestBuildTraceSearchQuery_StatusMessage(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		Tags: map[string]string{
			"status.message": "timeout",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should use intrinsic field path, not attributes.*
	assert.Contains(t, queryStr, `"status.message"`)
	assert.NotContains(t, queryStr, `"attributes.status.message"`)
	// status.message is a text field: must use "match" query, not "term".
	assert.Contains(t, queryStr, `"match"`)
}

func TestBuildTraceSearchQuery_StatusMessageNot(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		TagsNot: map[string]string{
			"status.message": "timeout",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// TagsNot with status.message should also use "match" (not "term").
	assert.Contains(t, queryStr, `"status.message"`)
	assert.Contains(t, queryStr, `"match"`)
}

func TestBuildTraceSearchQuery_StatusMessageExists(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		TagsExists: []string{"status.message"},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should generate exists on status.message, not attributes.status.message
	assert.Contains(t, queryStr, `"field":"status.message"`)
}

func TestBuildTraceSearchQuery_RootName(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		RootName: "GET /api",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain:
	//   - term query on "name" field for root span name match
	//   - must_not + exists on "parentSpanId" to identify root span
	assert.Contains(t, queryStr, `"name"`)
	assert.Contains(t, queryStr, `"GET /api"`)
	assert.Contains(t, queryStr, `"parentSpanId"`)
	assert.Contains(t, queryStr, `"must_not"`)
	// Should NOT use attributes.* path（不是通用 tag 路径）
	assert.NotContains(t, queryStr, `"attributes.rootName"`)
	assert.NotContains(t, queryStr, `"resource.rootName"`)
}

func TestBuildTraceSearchQuery_RootService(t *testing.T) {
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		RootService: "gateway-service",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should contain:
	//   - term query on "serviceName" field for root span service match
	//   - must_not + exists on "parentSpanId" to identify root span
	assert.Contains(t, queryStr, `"serviceName"`)
	assert.Contains(t, queryStr, `"gateway-service"`)
	assert.Contains(t, queryStr, `"parentSpanId"`)
	assert.Contains(t, queryStr, `"must_not"`)
	// Should NOT use attributes.* path
	assert.NotContains(t, queryStr, `"attributes.rootService"`)
}

func TestBuildTraceSearchQuery_RootName_NotMixedIntoTags(t *testing.T) {
	// Verify that RootName is NOT mixed into the generic Tags path.
	r := &TraceReader{}
	now := time.Now()

	query := TraceQuery{
		RootName: "GET /api",
		Tags: map[string]string{
			"http.method": "GET",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	esQuery := r.buildTraceSearchQuery(query)
	raw, err := json.Marshal(esQuery)
	require.NoError(t, err)

	queryStr := string(raw)

	// RootName should generate its own composite bool query.
	assert.Contains(t, queryStr, `"parentSpanId"`)
	// Tags should still work independently (http.method resolved to attributes.http.method).
	assert.Contains(t, queryStr, `"attributes.http.method"`)
	assert.Contains(t, queryStr, `"GET"`)
}
