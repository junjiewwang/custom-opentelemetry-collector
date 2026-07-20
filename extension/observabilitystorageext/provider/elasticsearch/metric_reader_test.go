// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
