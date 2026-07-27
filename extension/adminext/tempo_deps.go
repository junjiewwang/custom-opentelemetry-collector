// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// tempoHandlers holds the dependencies required by the Tempo-compatible API
// handlers, decoupling them from the *Extension god-object. Either reader may
// be nil; the router only registers the routes a given reader supports.
//
// This is the P1 dependency-injection seam: handlers are methods on
// *tempoHandlers and can be unit-tested with fake readers (see
// tempo_handler_fake_test.go) instead of requiring a full Extension + real
// storage.
type tempoHandlers struct {
	traceReader  observabilitystorageext.TraceReader
	metricReader observabilitystorageext.MetricReader
	logger       *zap.Logger
}

// newTempoHandlers wires a tempoHandlers from an Extension's storage readers.
func newTempoHandlers(e *Extension) *tempoHandlers {
	return &tempoHandlers{
		traceReader:  e.storageTraceReader,
		metricReader: e.storageMetricReader,
		logger:       e.logger,
	}
}

// writeJSON writes a JSON response. Delegates to the shared free helper so the
// implementation is not duplicated between *Extension and *tempoHandlers.
func (h *tempoHandlers) writeJSON(w http.ResponseWriter, status int, data any) {
	writeJSONResp(h.logger, w, status, data)
}

// writeError writes an error response.
func (h *tempoHandlers) writeError(w http.ResponseWriter, status int, message string) {
	writeErrorResp(h.logger, w, status, message)
}
