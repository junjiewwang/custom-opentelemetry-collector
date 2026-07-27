// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestLokiHandler_NoReader proves lokiHandlers is dependency-injected: built
// directly without *Extension, it reports service_unavailable when no log reader
// is wired.
func TestLokiHandler_NoReader(t *testing.T) {
	h := &lokiHandlers{logReader: nil, logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/labels", nil)
	rr := httptest.NewRecorder()
	h.handleLokiLabels(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), "log storage not available")
}
