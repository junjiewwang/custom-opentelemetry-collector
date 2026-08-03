// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// stubListNamesReader is a MetricReader that only implements ListMetricNames,
// returning a fixed list. All other interface methods are nil via embedding
// (not exercised by the label-values handler).
type stubListNamesReader struct {
	observabilitystorageext.MetricReader
	names []string
}

func (r *stubListNamesReader) ListMetricNames(_ context.Context, _ observabilitystorageext.TimeRange) ([]string, error) {
	return r.names, nil
}

// newLabelValuesRequest builds a GET /label/__name__/values request with the
// given query and the chi {labelName} route param injected.
func newLabelValuesRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/label/__name__/values"+query, nil)
	_ = req.ParseForm()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("labelName", "__name__")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func decodePromLabelList(t *testing.T, body string) []string {
	t.Helper()
	var resp struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	assert.Equal(t, "success", resp.Status)
	return resp.Data
}

func TestHandlePromLabelValues_NoMatchReturnsAll(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubListNamesReader{names: []string{"alpha", "beta", "bufferpool_wait_total"}},
		logger:       zap.NewNop(),
	}
	rr := httptest.NewRecorder()
	h.handlePromLabelValues(rr, newLabelValuesRequest(t, ""))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"alpha", "beta", "bufferpool_wait_total"}, decodePromLabelList(t, rr.Body.String()))
}

func TestHandlePromLabelValues_RegexMatchFilters(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubListNamesReader{names: []string{"alpha", "beta", "bufferpool_wait_total", "pool_size"}},
		logger:       zap.NewNop(),
	}
	rr := httptest.NewRecorder()
	h.handlePromLabelValues(rr, newLabelValuesRequest(t, `?match[]={__name__=~".*pool.*"}`))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"bufferpool_wait_total", "pool_size"}, decodePromLabelList(t, rr.Body.String()))
}

func TestHandlePromLabelValues_AnchoredRegexOnlyMatchesExact(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubListNamesReader{names: []string{"pool", "bufferpool_wait_total"}},
		logger:       zap.NewNop(),
	}
	rr := httptest.NewRecorder()
	// Prometheus =~ is fully anchored: "pool" matches only exactly "pool".
	h.handlePromLabelValues(rr, newLabelValuesRequest(t, `?match[]={__name__=~"pool"}`))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"pool"}, decodePromLabelList(t, rr.Body.String()))
}

func TestHandlePromLabelValues_ExactMatch(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubListNamesReader{names: []string{"alpha", "beta", "gamma"}},
		logger:       zap.NewNop(),
	}
	rr := httptest.NewRecorder()
	h.handlePromLabelValues(rr, newLabelValuesRequest(t, `?match[]={__name__="beta"}`))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"beta"}, decodePromLabelList(t, rr.Body.String()))
}

func TestHandlePromLabelValues_SelectorWithoutNameReturnsAll(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubListNamesReader{names: []string{"a", "b"}},
		logger:       zap.NewNop(),
	}
	rr := httptest.NewRecorder()
	h.handlePromLabelValues(rr, newLabelValuesRequest(t, `?match[]={job="x"}`))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"a", "b"}, decodePromLabelList(t, rr.Body.String()))
}
