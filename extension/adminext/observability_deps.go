// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/adminext/observability"
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

// obsLegacyHandlers holds dependencies for the legacy proxy-mode observability
// API (observability_handler.go), decoupled from *Extension (P1 DI seam).
type obsLegacyHandlers struct {
	traceReader  observability.TraceReader
	metricReader observability.MetricReader
	logger       *zap.Logger
}

func newObsLegacyHandlers(e *Extension) *obsLegacyHandlers {
	return &obsLegacyHandlers{
		traceReader:  e.traceReader,
		metricReader: e.metricReader,
		logger:       e.logger,
	}
}

func (h *obsLegacyHandlers) writeError(w http.ResponseWriter, status int, message string) {
	writeErrorResp(h.logger, w, status, message)
}

// writeRawJSON writes raw JSON bytes with a status code (legacy proxy mode).
func (h *obsLegacyHandlers) writeRawJSON(w http.ResponseWriter, statusCode int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if statusCode > 0 {
		w.WriteHeader(statusCode)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if _, err := w.Write(body); err != nil {
		h.logger.Warn("Error writing response", zap.String("error", err.Error()))
	}
}

func (h *obsLegacyHandlers) writeRawJSONBytes(w http.ResponseWriter, body []byte) {
	h.writeRawJSON(w, http.StatusOK, body)
}
