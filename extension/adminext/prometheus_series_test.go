// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// stubQueryReader records Query calls and returns a canned result each time.
type stubQueryReader struct {
	observabilitystorageext.MetricReader
	queries []observabilitystorageext.MetricQuery
	result  *observabilitystorageext.MetricResult
	err     error
}

func (r *stubQueryReader) Query(_ context.Context, q observabilitystorageext.MetricQuery) (*observabilitystorageext.MetricResult, error) {
	r.queries = append(r.queries, q)
	if r.err != nil {
		return nil, r.err
	}
	if r.result == nil {
		return &observabilitystorageext.MetricResult{}, nil
	}
	// Return a fresh copy of Data so callers can't mutate shared state.
	out := *r.result
	return &out, nil
}

func newSeriesRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/series"+query, nil)
	_ = req.ParseForm()
	return req
}

func decodePromSeries(t *testing.T, body string) []map[string]string {
	t.Helper()
	var resp struct {
		Status string              `json:"status"`
		Data   []map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	assert.Equal(t, "success", resp.Status)
	return resp.Data
}

func TestHandlePromSeries_MissingMatch_400(t *testing.T) {
	h := &promHandlers{metricReader: &stubQueryReader{}, logger: zap.NewNop()}
	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, ""))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePromSeries_ExactNameRoutesToMetricName(t *testing.T) {
	r := &stubQueryReader{result: &observabilitystorageext.MetricResult{
		Data: []observabilitystorageext.MetricDataPoint{{Metric: "m1", Labels: map[string]string{"job": "a"}}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, `?match[]={__name__="m1"}`))
	assert.Equal(t, http.StatusOK, rr.Code)

	require.Len(t, r.queries, 1, "exact __name__ → single query")
	assert.Equal(t, "m1", r.queries[0].MetricName)
	assert.Empty(t, r.queries[0].Labels, "__name__ stripped from labels")

	series := decodePromSeries(t, rr.Body.String())
	require.Len(t, series, 1)
	assert.Equal(t, "m1", series[0][PromLabelName])
	assert.Equal(t, "a", series[0]["job"])
}

func TestHandlePromSeries_RegexNameExtractsLiteral(t *testing.T) {
	r := &stubQueryReader{result: &observabilitystorageext.MetricResult{
		Data: []observabilitystorageext.MetricDataPoint{{Metric: "m1"}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, `?match[]={__name__=~".*m1.*"}`))
	assert.Equal(t, http.StatusOK, rr.Code)

	require.Len(t, r.queries, 1)
	assert.Equal(t, "m1", r.queries[0].MetricName, "regex __name__ reduced to literal MetricName")
}

func TestHandlePromSeries_NonNameRegexLabelPassedThrough(t *testing.T) {
	// Regression: {job=~"x.*"} previously dropped the LabelMatch entirely.
	r := &stubQueryReader{result: &observabilitystorageext.MetricResult{
		Data: []observabilitystorageext.MetricDataPoint{{Metric: "m1", Labels: map[string]string{"job": "x1"}}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, `?match[]={job=~"x.*"}`))
	assert.Equal(t, http.StatusOK, rr.Code)

	require.Len(t, r.queries, 1)
	assert.Empty(t, r.queries[0].MetricName, "no __name__ → name-agnostic query")
	assert.Equal(t, map[string]string{"job": "x.*"}, r.queries[0].LabelMatch, "regex label must reach the storage layer")
}

func TestHandlePromSeries_AlternationQueriesEachName(t *testing.T) {
	r := &stubQueryReader{result: &observabilitystorageext.MetricResult{
		Data: []observabilitystorageext.MetricDataPoint{{Metric: "m1"}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, `?match[]={__name__=~"m1|m2"}`))
	assert.Equal(t, http.StatusOK, rr.Code)

	require.Len(t, r.queries, 2, "alternation → one query per name")
	assert.Equal(t, "m1", r.queries[0].MetricName)
	assert.Equal(t, "m2", r.queries[1].MetricName)
}

func TestHandlePromSeries_DedupsAcrossSelectors(t *testing.T) {
	// Same series returned for two OR'd selectors → deduped to one.
	r := &stubQueryReader{result: &observabilitystorageext.MetricResult{
		Data: []observabilitystorageext.MetricDataPoint{{Metric: "m1", Labels: map[string]string{"job": "a"}}},
	}}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	rr := httptest.NewRecorder()
	h.handlePromSeries(rr, newSeriesRequest(t, `?match[]={__name__="m1"}&match[]={__name__=~"m1"}`))
	assert.Equal(t, http.StatusOK, rr.Code)

	series := decodePromSeries(t, rr.Body.String())
	assert.Len(t, series, 1, "overlapping series across selectors must be deduped")
}
