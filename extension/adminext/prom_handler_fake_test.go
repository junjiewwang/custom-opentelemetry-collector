// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// TestPromHandler_NoReader proves promHandlers is dependency-injected: a
// promHandlers built directly (no *Extension, no storage) returns
// service_unavailable when no metric reader is wired.
func TestPromHandler_NoReader(t *testing.T) {
	h := &promHandlers{metricReader: nil, logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/prometheus/api/v1/query?query=up", nil)
	rr := httptest.NewRecorder()
	h.handlePromQuery(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), "metric reader not available")
}

// TestPromHandler_MissingQuery verifies request validation on the injected handler.
func TestPromHandler_MissingQuery(t *testing.T) {
	// metricReader non-nil but query param absent → bad_data.
	h := &promHandlers{metricReader: nilFakeMetricReader{}, logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/prometheus/api/v1/query", nil)
	rr := httptest.NewRecorder()
	h.handlePromQuery(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "'query' is required")
}

// nilFakeMetricReader embeds the interface (nil) so only overridden methods are
// usable; here we only need a non-nil reader to pass the availability guard.
type nilFakeMetricReader struct {
	observabilitystorageext.MetricReader
}
