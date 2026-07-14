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
