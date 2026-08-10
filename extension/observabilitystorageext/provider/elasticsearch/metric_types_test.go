package elasticsearch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ListMetricTypes must report the type of the NEWEST data point, not the most
// frequent one.
//
// When a metric is re-typed — as every non-monotonic Sum was, on being
// corrected from counter to gauge — the old documents outnumber the new ones
// for as long as retention holds them. Picking by frequency served the stale
// type indefinitely, so Grafana Metrics Drilldown kept wrapping gauges like
// jvm.thread.count in rate(). Grafana sends no start/end to /metadata, so the
// query spans all history and the stale majority always wins.
func TestListMetricTypes_UsesNewestNotMostFrequent(t *testing.T) {
	// One bucket per metric, each carrying the newest hit's _source.type.
	aggResponse := `{
      "buckets": [
        {"key":"jvm.thread.count","doc_count":1000,
         "metric_type":{"hits":{"hits":[{"_source":{"type":"gauge"}}]}}},
        {"key":"kafka.consumer.commit_total","doc_count":500,
         "metric_type":{"hits":{"hits":[{"_source":{"type":"counter"}}]}}},
        {"key":"http.server.request.duration","doc_count":300,
         "metric_type":{"hits":{"hits":[{"_source":{"type":"histogram"}}]}}},
        {"key":"no.type.recorded","doc_count":1,
         "metric_type":{"hits":{"hits":[]}}}
      ]
    }`

	fake := &fakeSearcher{
		Responses: []any{&SearchResponse{
			Aggregations: map[string]json.RawMessage{
				"metric_names": json.RawMessage(aggResponse),
			},
		}},
	}
	r := &MetricReader{searcher: fake, config: &Config{}, logger: zap.NewNop()}

	got, err := r.ListMetricTypes(context.Background(), TimeRange{})
	require.NoError(t, err)

	assert.Equal(t, map[string]storedmodel.MetricMeta{
		"jvm.thread.count":             {Type: "gauge"},
		"kafka.consumer.commit_total":  {Type: "counter"},
		"http.server.request.duration": {Type: "histogram"},
		"no.type.recorded":             {},
	}, got)

	// The sub-aggregation must sort by time descending, otherwise "newest" is
	// whatever ES happens to return first.
	sub := fake.LastRequest.Aggregations["metric_names"].(map[string]any)["aggs"].(map[string]any)
	topHits, ok := sub["metric_type"].(map[string]any)["top_hits"].(map[string]any)
	require.True(t, ok, "metric_type must be a top_hits aggregation, not terms")

	sort := topHits["sort"].([]map[string]any)
	require.Len(t, sort, 1)
	order := sort[0][FieldMetricTimeUnixMilli].(map[string]any)["order"]
	assert.Equal(t, "desc", order, "must take the newest document")
	assert.Equal(t, 1, topHits["size"])
}
