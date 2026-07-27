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

// TestInfluxHandler_Ping proves influxHandlers is dependency-injected: built
// directly without *Extension, handleInfluxDBPing returns the InfluxDB version
// headers + 204.
func TestInfluxHandler_Ping(t *testing.T) {
	h := &influxHandlers{logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/influxdb/ping", nil)
	rr := httptest.NewRecorder()
	h.handleInfluxDBPing(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "1.8.10-compatible", rr.Header().Get("X-Influxdb-Version"))
}
