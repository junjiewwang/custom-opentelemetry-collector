// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// stubFlatReader implements QueryFlat (returns a fixed sample list) and QueryRange
// (returns an empty result), so it can back both the bare-metric flat path and
// the aggregated range path without panicking on unimplemented methods.
type stubFlatReader struct {
	observabilitystorageext.MetricReader
	samples []observabilitystorageext.MetricSample
	err     error
}

func (r *stubFlatReader) QueryFlat(_ context.Context, _ observabilitystorageext.MetricFlatQuery) (*observabilitystorageext.MetricFlatResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]observabilitystorageext.MetricSample, len(r.samples))
	copy(out, r.samples)
	return &observabilitystorageext.MetricFlatResult{Samples: out}, nil
}

func (r *stubFlatReader) QueryRange(_ context.Context, _ observabilitystorageext.MetricRangeQuery) (*observabilitystorageext.MetricRangeResult, error) {
	return &observabilitystorageext.MetricRangeResult{}, nil
}

func newRangeRequest(t *testing.T, query string, t0 time.Time, dur, step time.Duration) *http.Request {
	t.Helper()
	start := strconv.FormatInt(t0.Unix(), 10)
	end := strconv.FormatInt(t0.Add(dur).Unix(), 10)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/query_range?query="+query+"&start="+start+"&end="+end+"&step="+step.String(), nil)
	_ = req.ParseForm()
	return req
}

func decodeRangeMatrix(t *testing.T, body string) []map[string]any {
	t.Helper()
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string           `json:"resultType"`
			Result     []map[string]any `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "matrix", resp.Data.ResultType)
	return resp.Data.Result
}

// TestHandlePromQueryRange_BareMetricReturnsPerSeriesLabels proves a bare metric
// selector returns one series per distinct label set with full labels, not a
// single label-less collapsed series.
func TestHandlePromQueryRange_BareMetricReturnsPerSeriesLabels(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)

	// Two distinct label sets, two samples each within their step buckets.
	r := &stubFlatReader{samples: []observabilitystorageext.MetricSample{
		{TimestampMs: t0.UnixMilli(), Value: 10, Labels: map[string]string{"client": "a", "server": "x"}},
		{TimestampMs: t0.Add(30 * time.Second).UnixMilli(), Value: 12, Labels: map[string]string{"client": "a", "server": "x"}},
		{TimestampMs: t0.UnixMilli(), Value: 20, Labels: map[string]string{"client": "b", "server": "x"}},
		{TimestampMs: t0.Add(30 * time.Second).UnixMilli(), Value: 22, Labels: map[string]string{"client": "b", "server": "x"}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "traces_service_graph_request_total", t0, 60*time.Second, 30*time.Second))

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 2, "two distinct label sets → two series")

	// Each series carries __name__ + its dimension labels (NOT label-less).
	for _, s := range matrix {
		metric := s["metric"].(map[string]any)
		assert.Equal(t, "traces_service_graph_request_total", metric["__name__"])
		client, ok := metric["client"]
		require.True(t, ok, "client label must be present")
		assert.Contains(t, []string{"a", "b"}, client)
		assert.Equal(t, "x", metric["server"])
	}
}

func TestHandlePromQueryRange_BareMetricStepAveraging(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)

	// One series; two samples in the first step bucket (10, 20 → avg 15), one in the second (30).
	r := &stubFlatReader{samples: []observabilitystorageext.MetricSample{
		{TimestampMs: t0.UnixMilli(), Value: 10, Labels: map[string]string{"job": "a"}},
		{TimestampMs: t0.Add(5 * time.Second).UnixMilli(), Value: 20, Labels: map[string]string{"job": "a"}},
		{TimestampMs: t0.Add(30 * time.Second).UnixMilli(), Value: 30, Labels: map[string]string{"job": "a"}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "m", t0, 60*time.Second, 30*time.Second))
	require.Equal(t, http.StatusOK, rr.Code)

	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 1)
	values := matrix[0]["values"].([]any)
	require.Len(t, values, 2)
	// formatPromValue emits strings; parse to float.
	v0, _ := strconv.ParseFloat(values[0].([]any)[1].(string), 64)
	v1, _ := strconv.ParseFloat(values[1].([]any)[1].(string), 64)
	assert.InDelta(t, 15.0, v0, 1e-9)
	assert.InDelta(t, 30.0, v1, 1e-9)
}

// TestHandlePromQueryRange_ExplicitAggregationSkipsBarePath confirms that an
// explicit sum(metric) (no by) does NOT take the bare-metric flat path: with a
// stub that returns per-series samples only from QueryFlat (QueryRange returns
// empty), sum(m) yields an empty matrix, not the per-series data.
func TestHandlePromQueryRange_ExplicitAggregationSkipsBarePath(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)
	r := &stubFlatReader{samples: []observabilitystorageext.MetricSample{
		{TimestampMs: t0.UnixMilli(), Value: 1, Labels: map[string]string{"job": "a"}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "sum(m)", t0, 60*time.Second, 30*time.Second))
	require.Equal(t, http.StatusOK, rr.Code)

	matrix := decodeRangeMatrix(t, rr.Body.String())
	assert.Empty(t, matrix, "explicit aggregation uses QueryRange (empty here), not the flat bare path")
}
