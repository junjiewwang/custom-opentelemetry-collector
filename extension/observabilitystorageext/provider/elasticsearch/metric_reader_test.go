// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBuildLabelComboAgg_SingleKey(t *testing.T) {
	r := &MetricReader{}
	agg := r.buildLabelComboAgg([]string{"client"})

	terms, ok := agg["terms"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "labels.client", terms["field"])
	assert.Equal(t, 100, terms["size"])

	_, hasNext := agg["aggs"]
	assert.False(t, hasNext, "single key should not have sub-agg")
}

func TestBuildLabelComboAgg_MultiKey(t *testing.T) {
	r := &MetricReader{}
	agg := r.buildLabelComboAgg([]string{"client", "server"})

	terms := agg["terms"].(map[string]any)
	assert.Equal(t, "labels.client", terms["field"])

	sub, ok := agg["aggs"].(map[string]any)
	assert.True(t, ok, "multi key should have sub-agg")

	next := sub["next"].(map[string]any)
	nextTerms := next["terms"].(map[string]any)
	assert.Equal(t, "labels.server", nextTerms["field"])
}

func TestBuildLabelComboAgg_Empty(t *testing.T) {
	r := &MetricReader{}
	agg := r.buildLabelComboAgg([]string{})
	assert.Nil(t, agg)
}

func TestFlattenLabelCombos_SingleKey(t *testing.T) {
	r := &MetricReader{}

	// Simulate ES response with buckets.
	mockResp := map[string]any{
		"combo_root": map[string]any{
			"buckets": []any{
				map[string]any{"key": "svc-a", "doc_count": 10},
				map[string]any{"key": "svc-b", "doc_count": 5},
			},
		},
	}

	combos := r.flattenLabelCombos(mockResp, []string{"client"})
	assert.Len(t, combos, 2)
	assert.Equal(t, map[string]string{"client": "svc-a"}, combos[0])
	assert.Equal(t, map[string]string{"client": "svc-b"}, combos[1])
}

func TestFlattenLabelCombos_MultiKey(t *testing.T) {
	r := &MetricReader{}

	mockResp := map[string]any{
		"combo_root": map[string]any{
			"buckets": []any{
				map[string]any{
					"key":       "svc-a",
					"doc_count": 10,
					"next": map[string]any{
						"buckets": []any{
							map[string]any{"key": "http", "doc_count": 7},
							map[string]any{"key": "grpc", "doc_count": 3},
						},
					},
				},
				map[string]any{
					"key":       "svc-b",
					"doc_count": 5,
					"next": map[string]any{
						"buckets": []any{
							map[string]any{"key": "messaging_system", "doc_count": 5},
						},
					},
				},
			},
		},
	}

	combos := r.flattenLabelCombos(mockResp, []string{"client", "connection_type"})
	assert.Len(t, combos, 3)

	// Verify all combinations.
	found := map[string]bool{}
	for _, c := range combos {
		key := c["client"] + "/" + c["connection_type"]
		found[key] = true
	}
	assert.True(t, found["svc-a/http"])
	assert.True(t, found["svc-a/grpc"])
	assert.True(t, found["svc-b/messaging_system"])
}

func TestFlattenLabelCombos_Empty(t *testing.T) {
	r := &MetricReader{}

	combos := r.flattenLabelCombos(map[string]any{}, []string{"client"})
	assert.Nil(t, combos)
}

func TestFlattenLabelCombos_NoBuckets(t *testing.T) {
	r := &MetricReader{}

	combos := r.flattenLabelCombos(
		map[string]any{"combo_root": map[string]any{}},
		[]string{"client"},
	)
	assert.Nil(t, combos)
}

func TestBuildMetricFilter_LabelMatch_TermsTranslation(t *testing.T) {
	r := &MetricReader{}

	// Simulate: span_name=~"value1|value2|value3" (common Grafana pattern)
	result := r.buildMetricFilter(
		"traces_spanmetrics_latency",
		"customcol",
		map[string]string{"service.name": "my-service"},
		map[string]string{"span.name": `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|POST /api/v2/prometheus/api/v1/query`},
		TimeRange{},
	)

	// Should have no post-filters (all patterns translatable).
	assert.Empty(t, result.PostFilters, "simple alternation should be fully handled at ES level")

	// Query should be a bool with must clauses.
	boolQ, ok := result.Query["bool"].(map[string]any)
	assert.True(t, ok)
	must, ok := boolQ["must"].([]map[string]any)
	assert.True(t, ok)
	assert.NotEmpty(t, must)
}

func TestBuildMetricFilter_LabelMatch_UnsupportedRegex(t *testing.T) {
	r := &MetricReader{}

	// Complex regex that can't be translated.
	result := r.buildMetricFilter(
		"traces_spanmetrics_latency",
		"",
		nil,
		map[string]string{"span.name": `opentelemetry.*Export`},
		TimeRange{},
	)

	// Should have post-filters.
	assert.NotEmpty(t, result.PostFilters)
	// Key translated by SanitizeMetricKey: span.name → span_name
	assert.Equal(t, `opentelemetry.*Export`, result.PostFilters["span_name"])
}

func TestBuildMetricFilter_LabelMatch_SingleLiteral(t *testing.T) {
	r := &MetricReader{}

	// Single literal value (contains escaped dots).
	result := r.buildMetricFilter(
		"traces_spanmetrics_latency",
		"",
		nil,
		map[string]string{"span.name": `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`},
		TimeRange{},
	)

	// Should have no post-filters.
	assert.Empty(t, result.PostFilters)
}

func TestPostFilterSamples(t *testing.T) {
	samples := []MetricSample{
		{Labels: map[string]string{"span.name": "opentelemetry.proto.collector.logs.v1.LogsService/Export"}, TimestampMs: 1},
		{Labels: map[string]string{"span.name": "opentelemetry.proto.collector.trace.v1.TraceService/Export"}, TimestampMs: 2},
		{Labels: map[string]string{"span.name": "POST /api/v2/prometheus/api/v1/query"}, TimestampMs: 3},
		{Labels: map[string]string{"span.name": "unrelated-span"}, TimestampMs: 4},
	}

	t.Run("empty post-filters passes all through", func(t *testing.T) {
		result := postFilterSamples(samples, nil)
		assert.Len(t, result, 4)
	})

	t.Run("regex with alternation pattern", func(t *testing.T) {
		// This simulates what happens when a complex regex pattern is used.
		result := postFilterSamples(samples, map[string]string{
			"span.name": `opentelemetry\.proto\.collector.*Export`,
		})
		assert.Len(t, result, 2)
		assert.Equal(t, int64(1), result[0].TimestampMs)
		assert.Equal(t, int64(2), result[1].TimestampMs)
	})

	t.Run("no match", func(t *testing.T) {
		result := postFilterSamples(samples, map[string]string{
			"span.name": `nonexistent.*pattern`,
		})
		assert.Len(t, result, 0)
	})
}

func TestPostFilterDataPoints(t *testing.T) {
	data := []MetricDataPoint{
		{Labels: map[string]string{"service.name": "alpha"}, Value: 1.0},
		{Labels: map[string]string{"service.name": "beta"}, Value: 2.0},
		{Labels: map[string]string{"service.name": "gamma"}, Value: 3.0},
	}

	t.Run("filter by simple alternation", func(t *testing.T) {
		result := postFilterDataPoints(data, map[string]string{
			"service.name": "alpha|beta",
		})
		assert.Len(t, result, 2)
		assert.Equal(t, 1.0, result[0].Value)
		assert.Equal(t, 2.0, result[1].Value)
	})

	t.Run("empty filter passes all", func(t *testing.T) {
		result := postFilterDataPoints(data, nil)
		assert.Len(t, result, 3)
	})
}

func TestPostFilterSeries(t *testing.T) {
	series := []MetricSeries{
		{Labels: map[string]string{"span.name": "GET /users"}},
		{Labels: map[string]string{"span.name": "POST /orders"}},
		{Labels: map[string]string{"span.name": "DELETE /items"}},
	}

	t.Run("filter keeps matching series", func(t *testing.T) {
		result := postFilterSeries(series, map[string]string{
			"span.name": "GET.*|POST.*",
		})
		assert.Len(t, result, 2)
	})

	t.Run("empty filter passes all", func(t *testing.T) {
		result := postFilterSeries(series, nil)
		assert.Len(t, result, 3)
	})
}

func TestPostFilterRawSeries(t *testing.T) {
	series := []MetricRawSeries{
		{Labels: map[string]string{"service.name": "web-frontend"}},
		{Labels: map[string]string{"service.name": "api-gateway"}},
		{Labels: map[string]string{"service.name": "db-proxy"}},
	}

	t.Run("filter by prefix pattern", func(t *testing.T) {
		result := postFilterRawSeries(series, map[string]string{
			"service.name": "web.*|api.*",
		})
		assert.Len(t, result, 2)
	})
}

func TestListLabelNamesQueryWithMetricFilter(t *testing.T) {
	r := &MetricReader{}

	now := time.Now()
	tr := TimeRange{Start: now, End: now.Add(time.Hour)}

	t.Run("without metric name", func(t *testing.T) {
		req := &SearchRequest{
			Query:  r.timeRangeQuery(tr),
			Size:   100,
			Source: []string{FieldMetricLabels},
		}
		_, hasBool := req.Query["bool"]
		assert.False(t, hasBool, "no metric filter → no bool wrapper")
	})

	t.Run("with metric name", func(t *testing.T) {
		req := &SearchRequest{
			Query:  r.timeRangeQuery(tr),
			Size:   100,
			Source: []string{FieldMetricLabels},
		}
		metricName := "test_metric"
		req.Query = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					req.Query,
					{"term": map[string]any{FieldName: metricName}},
				},
			},
		}
		boolQ := req.Query["bool"].(map[string]any)
		mustList := boolQ["must"].([]map[string]any)
		assert.Len(t, mustList, 2)
		termQ := mustList[1]["term"].(map[string]any)
		assert.Equal(t, metricName, termQ[FieldName])
	})
}

// ── Query (instant) per-series dedup tests ──────────────────────────────

// metricDoc is a minimal metric document for building canned search hits.
type metricDoc struct {
	TimeUnixMilli int64             `json:"timeUnixMilli"`
	Value         float64           `json:"value"`
	Labels        map[string]string `json:"labels"`
}

// metricHits builds SearchHits from docs (in the order given — caller controls
// newest-first ordering to mimic ES desc sort).
func metricHits(docs ...metricDoc) []SearchHit {
	hits := make([]SearchHit, 0, len(docs))
	for i, d := range docs {
		src, _ := json.Marshal(d)
		hits = append(hits, SearchHit{ID: string(rune('a' + i)), Source: src})
	}
	return hits
}

func newTestMetricReaderForQuery(s Searcher) *MetricReader {
	return &MetricReader{
		searcher: s,
		config:   &Config{Metrics: IndexConfig{IndexPrefix: "otel-metrics"}},
		logger:   zap.NewNop(),
	}
}

// TestQuery_DedupSameLabelsKeepsLatest verifies that multiple docs with the
// same label set collapse to one data point — the newest (first in desc order).
func TestQuery_DedupSameLabelsKeepsLatest(t *testing.T) {
	hits := metricHits(
		metricDoc{TimeUnixMilli: 3000, Value: 0.9, Labels: map[string]string{"cpu": "cpu0"}},
		metricDoc{TimeUnixMilli: 2000, Value: 0.5, Labels: map[string]string{"cpu": "cpu0"}},
		metricDoc{TimeUnixMilli: 1000, Value: 0.1, Labels: map[string]string{"cpu": "cpu0"}},
	)
	fake := &fakeSearcher{
		Responses: []any{&SearchResponse{}},
	}
	fake.Responses[0].(*SearchResponse).Hits.Hits = hits
	r := newTestMetricReaderForQuery(fake)

	res, err := r.Query(t.Context(), MetricQuery{MetricName: "system.cpu.usage"})
	require.NoError(t, err)
	require.Len(t, res.Data, 1, "same-label docs collapse to one point")
	assert.Equal(t, 0.9, res.Data[0].Value, "should keep the newest value")
	assert.Equal(t, time.UnixMilli(3000), res.Data[0].Time)
}

// TestQuery_DistinctLabelsKeepsAll verifies docs with different label sets
// each produce a data point (no over-dedup).
func TestQuery_DistinctLabelsKeepsAll(t *testing.T) {
	hits := metricHits(
		metricDoc{TimeUnixMilli: 3000, Value: 0.9, Labels: map[string]string{"cpu": "cpu0"}},
		metricDoc{TimeUnixMilli: 3000, Value: 0.4, Labels: map[string]string{"cpu": "cpu1"}},
		metricDoc{TimeUnixMilli: 2000, Value: 0.1, Labels: map[string]string{"cpu": "cpu0"}}, // older dup of cpu0
	)
	fake := &fakeSearcher{
		Responses: []any{&SearchResponse{}},
	}
	fake.Responses[0].(*SearchResponse).Hits.Hits = hits
	r := newTestMetricReaderForQuery(fake)

	res, err := r.Query(t.Context(), MetricQuery{MetricName: "system.cpu.usage"})
	require.NoError(t, err)
	require.Len(t, res.Data, 2, "two distinct label sets → two points")
	// Newest per series kept: cpu0→0.9, cpu1→0.4.
	vals := map[float64]string{}
	for _, dp := range res.Data {
		vals[dp.Value] = dp.Labels["cpu"]
	}
	assert.Equal(t, "cpu0", vals[0.9])
	assert.Equal(t, "cpu1", vals[0.4])
}

// TestQuery_EmptyResult verifies no hits → empty result, no error.
func TestQuery_EmptyResult(t *testing.T) {
	fake := &fakeSearcher{
		Responses: []any{&SearchResponse{}}, // no hits
	}
	r := newTestMetricReaderForQuery(fake)

	res, err := r.Query(t.Context(), MetricQuery{MetricName: "no.such.metric"})
	require.NoError(t, err)
	assert.Empty(t, res.Data)
}

// TestQuery_EmptyLabelsTreatedAsOneSeries verifies docs with no labels all
// dedupe to a single series (labelsKey returns "" for empty labels).
func TestQuery_EmptyLabelsTreatedAsOneSeries(t *testing.T) {
	hits := metricHits(
		metricDoc{TimeUnixMilli: 3000, Value: 9.0, Labels: nil},
		metricDoc{TimeUnixMilli: 2000, Value: 1.0, Labels: map[string]string{}},
	)
	fake := &fakeSearcher{
		Responses: []any{&SearchResponse{}},
	}
	fake.Responses[0].(*SearchResponse).Hits.Hits = hits
	r := newTestMetricReaderForQuery(fake)

	res, err := r.Query(t.Context(), MetricQuery{MetricName: "gauge"})
	require.NoError(t, err)
	require.Len(t, res.Data, 1, "nil and empty labels are the same series")
	assert.Equal(t, 9.0, res.Data[0].Value, "keeps newest")
}

// TestLabelsKey verifies the dedup key is order-independent and stable.
func TestLabelsKey(t *testing.T) {
	assert.Equal(t, "", labelsKey(nil))
	assert.Equal(t, "", labelsKey(map[string]string{}))
	// Same labels, different map iteration order → same key.
	a := labelsKey(map[string]string{"cpu": "cpu0", "method": "GET"})
	b := labelsKey(map[string]string{"method": "GET", "cpu": "cpu0"})
	assert.Equal(t, a, b)
	assert.Equal(t, "cpu=cpu0,method=GET", a)
}
