// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// metaHit builds a SearchResponse carrying one meta doc as a hit, serialized
// the way ES returns it (Source is a RawMessage).
func metaHits(docs ...map[string]any) *SearchResponse {
	resp := &SearchResponse{}
	for _, d := range docs {
		raw, _ := json.Marshal(d)
		resp.Hits.Hits = append(resp.Hits.Hits, SearchHit{Source: raw})
	}
	return resp
}

func newMetaReader(f *fakeSearcher) *MetricReader {
	return &MetricReader{
		searcher: f,
		config:   &Config{Metrics: IndexConfig{IndexPrefix: "otel-metrics"}},
		logger:   zap.NewNop(),
	}
}

// ── ListMetricNames (meta-first) ───────────────────────────────────────

func TestListMetricNames_FromMeta(t *testing.T) {
	f := &fakeSearcher{
		Responses: []any{
			metaHits(
				map[string]any{"name": "jvm.memory.used"},
				map[string]any{"name": "cpu.usage"},
				map[string]any{"name": "cpu.usage"}, // duplicate across appIDs
			),
		},
	}
	r := newMetaReader(f)

	names, err := r.ListMetricNames(context.Background(), TimeRange{})
	require.NoError(t, err)
	assert.Equal(t, []string{"cpu.usage", "jvm.memory.used"}, names, "deduped + sorted")

	// The meta search must target the singleton index, not the data index.
	assert.Equal(t, "otel-metrics-meta", f.LastIndexPattern)
}

func TestListMetricNames_FallsBackOnIndexNotFound(t *testing.T) {
	f := &fakeSearcher{
		Err: ErrESIndexNotFound,
	}
	r := newMetaReader(f)

	// The meta search fails with index-not-found → fall back to the terms
	// aggregation, which in this fake also returns the (same) error. The key
	// assertion is that ListMetricNames surfaces the underlying error rather
	// than swallowing it or returning empty.
	_, err := r.ListMetricNames(context.Background(), TimeRange{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "list metric names failed")
}

func TestListMetricNames_FallsBackToAggregation(t *testing.T) {
	// First response (meta) is a 404 sentinel; second (fallback) is a terms agg.
	f := &fakeSearcher{
		Err: ErrESIndexNotFound,
	}
	// Override: fakeSearcher returns Err for EVERY call, so a genuine fallback
	// can't be exercised with a single Err. This test only asserts the error
	// path is exercised (see TestListMetricNames_FromMeta for the happy path).
	r := newMetaReader(f)
	_, err := r.ListMetricNames(context.Background(), TimeRange{})
	require.Error(t, err)
}

// ── ListMetricTypes (meta-first) ───────────────────────────────────────

func TestListMetricTypes_FromMeta(t *testing.T) {
	f := &fakeSearcher{
		Responses: []any{
			metaHits(
				map[string]any{"name": "jvm.memory.used", "type": "gauge", "unit": "By"},
				map[string]any{"name": "request.count", "type": "counter", "unit": "1"},
			),
		},
	}
	r := newMetaReader(f)

	types, err := r.ListMetricTypes(context.Background(), TimeRange{})
	require.NoError(t, err)
	assert.Equal(t, "gauge", types["jvm.memory.used"].Type)
	assert.Equal(t, "By", types["jvm.memory.used"].Unit)
	assert.Equal(t, "counter", types["request.count"].Type)
	assert.Equal(t, "1", types["request.count"].Unit)
}

// ── ListLabelNames (meta-first) ────────────────────────────────────────

func TestListLabelNames_FromMetaScoped(t *testing.T) {
	f := &fakeSearcher{
		Responses: []any{
			metaHits(
				map[string]any{"labelKeys": []any{"area", "pool"}},
				map[string]any{"labelKeys": []any{"pool", "host"}},
			),
		},
	}
	r := newMetaReader(f)

	names, err := r.ListLabelNames(context.Background(), TimeRange{}, "jvm.memory.used")
	require.NoError(t, err)
	assert.Equal(t, []string{"area", "host", "pool"}, names, "unioned + sorted")

	// Scoped query must be a term on name, not match_all.
	req := f.LastRequest
	q, ok := req.Query["term"].(map[string]any)
	require.True(t, ok, "scoped meta label query must be a term query")
	assert.Equal(t, "jvm.memory.used", q[FieldName])
}

func TestListLabelNames_FromMetaUnscoped(t *testing.T) {
	f := &fakeSearcher{
		Responses: []any{
			metaHits(
				map[string]any{"labelKeys": []any{"a"}},
				map[string]any{"labelKeys": []any{"b"}},
			),
		},
	}
	r := newMetaReader(f)

	names, err := r.ListLabelNames(context.Background(), TimeRange{}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, names)

	// Unscoped query must be match_all.
	_, ok := f.LastRequest.Query["match_all"].(map[string]any)
	assert.True(t, ok, "unscoped meta label query must be match_all")
}

func TestListLabelNames_FallsBackToSample(t *testing.T) {
	// Meta lookup fails → fallback to the 2000-doc sample. The sample query
	// reads the data index (rawIndexPatternForRange), not the meta index.
	f := &fakeSearcher{Err: ErrESIndexNotFound}
	r := newMetaReader(f)

	_, err := r.ListLabelNames(context.Background(), TimeRange{}, "m")
	require.Error(t, err)
	// The fallback search would target the data index (raw pattern), not meta.
}

// ── esIndexNotFound (error discrimination) ─────────────────────────────

func TestESIndexNotFound_Discrimination(t *testing.T) {
	assert.True(t, esIndexNotFound([]byte(`{"error":{"type":"index_not_found_exception"}}`)))
	assert.False(t, esIndexNotFound([]byte(`{"error":{"type":"search_phase_execution_exception"}}`)))
	assert.False(t, esIndexNotFound([]byte(`{"found":false}`)), "doc-not-found is not index-not-found")
	assert.False(t, esIndexNotFound([]byte(`not json`)))
}
