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

// TestObsV2Handler_NoAdmin proves obsV2Handlers is dependency-injected: built
// directly without *Extension, it returns service_unavailable when no storage
// admin is wired.
func TestObsV2Handler_NoAdmin(t *testing.T) {
	h := &obsV2Handlers{logger: zap.NewNop()} // admin == nil

	req := httptest.NewRequest(http.MethodGet, "/api/v2/observability/admin/status", nil)
	rr := httptest.NewRecorder()
	h.handleStorageStatus(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), "Storage admin not available")
}
