// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// influxHandlers holds the dependencies required by the InfluxDB v1-compatible
// API handlers, decoupling them from *Extension (P1 dependency-injection seam).
type influxHandlers struct {
	metricReader observabilitystorageext.MetricReader
	logger       *zap.Logger
}

func newInfluxHandlers(e *Extension) *influxHandlers {
	return &influxHandlers{
		metricReader: e.storageMetricReader,
		logger:       e.logger,
	}
}

// writeJSON delegates to the shared free helper (same implementation as
// *Extension.writeJSON and *tempoHandlers.writeJSON).
func (h *influxHandlers) writeJSON(w http.ResponseWriter, status int, data any) {
	writeJSONResp(h.logger, w, status, data)
}
