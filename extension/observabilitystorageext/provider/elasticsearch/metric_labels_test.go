package elasticsearch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// OTel attributes are typed, so a metric label can arrive as a JSON number or
// bool (http.response.status_code=200, rpc.grpc.status_code=0). Decoding
// straight into map[string]string makes encoding/json fail on the FIRST such
// key and abandon the whole document — the caller then sees an empty sample and
// loses bucket_counts/explicit_bounds with it.
//
// That is why histogram_quantile and the heatmap returned nothing for every
// http.* and rpc.client.* metric while the bucket data sat intact in ES:
// 11 of 26 histogram metrics were silently unreadable.
func TestMetricLabels_TolerateNonStringScalars(t *testing.T) {
	var got metricLabels
	src := `{"http_route":"/api","http_response_status_code":200,
	         "success":true,"ratio":1.5,"absent":null}`
	require.NoError(t, json.Unmarshal([]byte(src), &got))

	assert.Equal(t, metricLabels{
		"http_route":                "/api",
		"http_response_status_code": "200",
		"success":                   "true",
		"ratio":                     "1.5",
		"absent":                    "",
	}, got)
}

func TestMetricLabels_RejectsNonObject(t *testing.T) {
	var got metricLabels
	assert.Error(t, json.Unmarshal([]byte(`"not an object"`), &got))
}

// The regression that matters: a numeric label must not cost us the histogram
// payload sitting next to it in the same document.
func TestHitToSample_NumericLabelKeepsBucketData(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}

	hit := SearchHit{
		ID: "doc-1",
		Source: json.RawMessage(`{
			"timeUnixMilli": 1786073219737,
			"value": 131.49,
			"labels": {"http_route":"/market","http_response_status_code":200},
			"bucket_counts": [50635,2410,1388],
			"explicit_bounds": [0.005,0.01]
		}`),
	}

	s := r.hitToSample(hit)

	assert.Equal(t, int64(1786073219737), s.TimestampMs)
	assert.InDelta(t, 131.49, s.Value, 1e-9)
	assert.Equal(t, []int64{50635, 2410, 1388}, s.BucketCounts,
		"bucket_counts must survive a numeric label")
	assert.Equal(t, []float64{0.005, 0.01}, s.Bounds,
		"explicit_bounds must survive a numeric label")
	assert.Equal(t, map[string]string{
		"http_route":                "/market",
		"http_response_status_code": "200",
	}, s.Labels)
}

func TestHitToDataPoint_NumericLabelKeepsBucketData(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}

	hit := SearchHit{
		ID: "doc-2",
		Source: json.RawMessage(`{
			"timeUnixMilli": 1786073219737,
			"value": 7,
			"labels": {"rpc_grpc_status_code":0},
			"bucket_counts": [1,2],
			"explicit_bounds": [5,10]
		}`),
	}

	dp := r.hitToDataPoint(hit)

	assert.Equal(t, []int64{1, 2}, dp.BucketCounts)
	assert.Equal(t, []float64{5, 10}, dp.ExplicitBounds)
	assert.Equal(t, map[string]string{"rpc_grpc_status_code": "0"}, dp.Labels)
}

// Dynamically mapped metric labels are typed: a string value becomes
// text+keyword, a numeric one becomes long, a bool becomes boolean. The
// breakdown value picker (/label/{name}/values) must return every value
// regardless of type.
//
// The values are read from the stored `labels` object on sampled documents
// (like ListLabelNames / QueryFlat), NOT from an ES `terms` aggregation on
// `labels.<key>.keyword` — the latter returned empty buckets / illegal_argument
// for string labels in the running indices, blanking the picker even though the
// values were present on the documents. The metricLabels decoder normalizes
// numeric/bool attributes to their string form, so one path covers all types.
func TestListLabelValues_RawDocExtraction(t *testing.T) {
	resp := &SearchResponse{}
	resp.Hits.Hits = []SearchHit{
		rawLabelDoc(t, `{"http_route":"/api","http_response_status_code":200}`),
		rawLabelDoc(t, `{"http_route":"/health","http_response_status_code":401}`),
		rawLabelDoc(t, `{"http_route":"/api"}`), // duplicate → deduped
	}

	t.Run("string label", func(t *testing.T) {
		fs := &fakeSearcher{Responses: []any{resp}}
		r := &MetricReader{searcher: fs, config: &Config{Metrics: IndexConfig{IndexPrefix: "metrics"}}}
		got, err := r.ListLabelValues(context.Background(), "http_route", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"/api", "/health"}, got)
		// single raw-doc request, no ES terms aggregation
		require.Len(t, fs.Requests, 1)
		assert.Nil(t, fs.Requests[0].Aggregations)
	})

	t.Run("numeric label normalizes to string", func(t *testing.T) {
		fs := &fakeSearcher{Responses: []any{resp}}
		r := &MetricReader{searcher: fs, config: &Config{Metrics: IndexConfig{IndexPrefix: "metrics"}}}
		got, err := r.ListLabelValues(context.Background(), "http_response_status_code", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"200", "401"}, got)
	})

	t.Run("label absent yields nothing", func(t *testing.T) {
		fs := &fakeSearcher{Responses: []any{resp}}
		r := &MetricReader{searcher: fs, config: &Config{Metrics: IndexConfig{IndexPrefix: "metrics"}}}
		got, err := r.ListLabelValues(context.Background(), "absent", TimeRange{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// service_name is stored in the TOP-LEVEL serviceName field for metrics whose
// only service identifier is the resource attribute service.name (e.g.
// db.client.connections.*, kafka.consumer.* from a Java agent). The query
// layer must promote it to the service_name label, otherwise Metrics Drilldown
// — which breaks every metric down by service_name by default — returns 0
// series and the page shows "no data" even though the samples exist.
func TestHitToSample_PromotesServiceName(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	hit := SearchHit{ID: "doc-3", Source: json.RawMessage(`{
		"timeUnixMilli": 1786073219737,
		"value": 5,
		"serviceName": "test-java",
		"labels": {"pool_name": "DruidDataSource-1"}
	}`)}
	s := r.hitToSample(hit)
	assert.Equal(t, "test-java", s.Labels["service_name"])
	assert.Equal(t, "DruidDataSource-1", s.Labels["pool_name"])
}

func TestHitToDataPoint_PromotesServiceName(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	hit := SearchHit{ID: "doc-4", Source: json.RawMessage(`{
		"timeUnixMilli": 1786073219737,
		"value": 5,
		"serviceName": "test-java",
		"labels": {"pool_name": "DruidDataSource-1"}
	}`)}
	dp := r.hitToDataPoint(hit)
	assert.Equal(t, "test-java", dp.Labels["service_name"])
	assert.Equal(t, "DruidDataSource-1", dp.Labels["pool_name"])
}

// A data-point label already named service_name (e.g. the spanmetrics
// connector) must win; the resource serviceName must not overwrite it.
func TestMergeServiceName_DataPointLabelWins(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	hit := SearchHit{ID: "doc-5", Source: json.RawMessage(`{
		"timeUnixMilli": 1786073219737,
		"value": 5,
		"serviceName": "resource-service",
		"labels": {"service_name": "datapoint-service"}
	}`)}
	s := r.hitToSample(hit)
	assert.Equal(t, "datapoint-service", s.Labels["service_name"])
}

// When the document has no serviceName field, no service_name label is injected,
// preserving prior behavior for metrics that genuinely carry no service.
func TestHitToSample_NoServiceNameWhenAbsent(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	hit := SearchHit{ID: "doc-6", Source: json.RawMessage(`{
		"timeUnixMilli": 1786073219737,
		"value": 5,
		"labels": {"pool_name": "DruidDataSource-1"}
	}`)}
	s := r.hitToSample(hit)
	_, ok := s.Labels["service_name"]
	assert.False(t, ok, "service_name must not be injected when absent")
}

// ListLabelValuesForMetric must surface service_name values from the top-level
// serviceName field, so the breakdown picker offers them for metrics that lack
// service_name inside labels.
func TestListLabelValuesForMetric_ServiceNameFromTopLevel(t *testing.T) {
	resp := &SearchResponse{}
	resp.Hits.Hits = []SearchHit{
		{Source: json.RawMessage(`{"serviceName":"test-java","labels":{"pool_name":"p1"}}`)},
		{Source: json.RawMessage(`{"serviceName":"test-java","labels":{"pool_name":"p2"}}`)},
		{Source: json.RawMessage(`{"serviceName":"other-svc","labels":{"pool_name":"p3"}}`)},
	}
	fs := &fakeSearcher{Responses: []any{resp}}
	r := &MetricReader{searcher: fs, config: &Config{Metrics: IndexConfig{IndexPrefix: "metrics"}}}
	got, err := r.ListLabelValuesForMetric(context.Background(), "service_name", "db.client.connections.idle.max", TimeRange{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"test-java", "other-svc"}, got)
}
