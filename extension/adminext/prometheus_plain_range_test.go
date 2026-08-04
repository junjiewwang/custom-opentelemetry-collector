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

// stubRangeReader backs the bare-metric /query_range path: ListLabelNames
// discovers label keys, QueryRange returns per-series buckets.
type stubRangeReader struct {
	observabilitystorageext.MetricReader
	labelNames []string
	series     []observabilitystorageext.MetricSeries
	err        error
}

func (r *stubRangeReader) ListLabelNames(_ context.Context, _ observabilitystorageext.TimeRange, _ string) ([]string, error) {
	return r.labelNames, nil
}

func (r *stubRangeReader) QueryRange(_ context.Context, q observabilitystorageext.MetricRangeQuery) (*observabilitystorageext.MetricRangeResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]observabilitystorageext.MetricSeries, len(r.series))
	copy(out, r.series)
	return &observabilitystorageext.MetricRangeResult{Data: out}, nil
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
// selector returns one series per distinct label set with full labels (not a
// single collapsed label-less series), via the QueryRange composite path.
func TestHandlePromQueryRange_BareMetricReturnsPerSeriesLabels(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)
	r := &stubRangeReader{
		labelNames: []string{"client", "server", "connection_type"},
		series: []observabilitystorageext.MetricSeries{
			{Labels: map[string]string{"client": "a", "server": "x", "connection_type": "grpc"}},
			{Labels: map[string]string{"client": "b", "server": "x", "connection_type": "grpc"}},
		},
	}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "traces_service_graph_request_total", t0, 60*time.Second, 30*time.Second))

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 2, "two distinct label sets → two series")

	for _, s := range matrix {
		metric := s["metric"].(map[string]any)
		assert.Equal(t, "traces_service_graph_request_total", metric["__name__"])
		// Full dimension labels present (not label-less).
		assert.Contains(t, []string{"a", "b"}, metric["client"])
		assert.Equal(t, "x", metric["server"])
		assert.Equal(t, "grpc", metric["connection_type"])
	}
}

// TestHandlePromQueryRange_BareMetricUsesQueryRangeNotFlat proves the bare-metric
// path no longer routes through QueryFlat (which truncated at MaxDocs). A stub
// that only implements QueryRange (QueryFlat returns nil) must still return data.
func TestHandlePromQueryRange_BareMetricUsesQueryRangeNotFlat(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)
	r := &stubRangeReader{
		labelNames: []string{"job"},
		series: []observabilitystorageext.MetricSeries{
			{Labels: map[string]string{"job": "a"}},
		},
	}
	// QueryFlat is intentionally NOT implemented (nil via embedding) — if the
	// bare path tried to use it, it would panic/nil-deref. QueryRange returns data.
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "m", t0, 60*time.Second, 30*time.Second))
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 1)
	assert.Equal(t, "a", matrix[0]["metric"].(map[string]any)["job"])
}

// TestHandlePromQueryRange_BareMetricNoLabelsCollapsesToOneSeries proves that when
// a metric has no dimension labels, the bare path degrades to a single series
// (empty GroupBy → simple date_histogram), rather than erroring.
func TestHandlePromQueryRange_BareMetricNoLabelsCollapsesToOneSeries(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)
	r := &stubRangeReader{
		labelNames: nil, // no labels discovered
		series: []observabilitystorageext.MetricSeries{
			{Labels: map[string]string{}},
		},
	}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "m", t0, 60*time.Second, 30*time.Second))
	require.Equal(t, http.StatusOK, rr.Code)

	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 1, "no labels → single series (graceful fallback)")
}

// TestHandlePromQueryRange_ExplicitAggregationStillCollapses confirms explicit
// sum(metric) (no by) still uses QueryRange without auto-GroupBy discovery:
// the bare-metric branch is skipped because expr.Aggregation != "".
func TestHandlePromQueryRange_ExplicitAggregationSkipsAutoGroupBy(t *testing.T) {
	t0 := time.UnixMilli(1_000_000_000_000)
	r := &stubRangeReader{
		labelNames: []string{"job"}, // should NOT be used for sum(m)
		series:     []observabilitystorageext.MetricSeries{{Labels: map[string]string{}}},
	}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromQueryRange(rr, newRangeRequest(t, "sum(m)", t0, 60*time.Second, 30*time.Second))
	require.Equal(t, http.StatusOK, rr.Code)
	matrix := decodeRangeMatrix(t, rr.Body.String())
	require.Len(t, matrix, 1, "sum(m) with no by → single aggregated series")
}
