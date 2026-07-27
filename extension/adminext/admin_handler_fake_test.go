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

// TestAdminHandler_Health proves adminHandlers is dependency-injected: built
// directly without *Extension, handleHealth returns 200 {"status":"ok"}.
func TestAdminHandler_Health(t *testing.T) {
	h := &adminHandlers{logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	h.handleHealth(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"status":"ok"`)
}
