// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// obsV2Handlers holds dependencies for the unified-storage observability API
// (observability_handler_v2.go), decoupled from *Extension (P1 DI seam).
type obsV2Handlers struct {
	traceReader  observabilitystorageext.TraceReader
	metricReader observabilitystorageext.MetricReader
	logReader    observabilitystorageext.LogReader
	admin        observabilitystorageext.StorageAdmin
	storage      *observabilitystorageext.ObservabilityStorage
	logger       *zap.Logger
}

func newObsV2Handlers(e *Extension) *obsV2Handlers {
	return &obsV2Handlers{
		traceReader:  e.storageTraceReader,
		metricReader: e.storageMetricReader,
		logReader:    e.storageLogReader,
		admin:        e.storageAdmin,
		storage:      e.observabilityStorage,
		logger:       e.logger,
	}
}

func (h *obsV2Handlers) writeJSON(w http.ResponseWriter, status int, data any) {
	writeJSONResp(h.logger, w, status, data)
}

func (h *obsV2Handlers) writeError(w http.ResponseWriter, status int, message string) {
	writeErrorResp(h.logger, w, status, message)
}
