// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// fakeTraceReader is a minimal TraceReader fake for unit-testing tempoHandlers
// without *Extension or real storage. It embeds the interface (nil) so only the
// overridden methods are usable; this is the P1b testability seam — before the
// refactor, handlers were *Extension methods and could not be tested this way.
type fakeTraceReader struct {
	observabilitystorageext.TraceReader // nil embed — other methods panic if called

	trace *observabilitystorageext.Trace
	err   error
	gotID string
}

func (f *fakeTraceReader) GetTrace(_ context.Context, traceID string) (*observabilitystorageext.Trace, error) {
	f.gotID = traceID
	return f.trace, f.err
}

func newTempoHandlersWithFake(fake observabilitystorageext.TraceReader) *tempoHandlers {
	return &tempoHandlers{traceReader: fake, logger: zap.NewNop()}
}

// TestTempoHandler_GetTrace_NotFound proves the handler is now dependency-injected:
// a tempoHandlers built directly with a fake reader (no *Extension, no storage)
// routes the request, forwards the traceID, and returns 404 when the trace is absent.
func TestTempoHandler_GetTrace_NotFound(t *testing.T) {
	fake := &fakeTraceReader{trace: nil, err: nil} // reader returns no trace
	h := newTempoHandlersWithFake(fake)

	r := chi.NewRouter()
	r.Get("/api/traces/{traceID}", h.handleTempoGetTrace)

	req := httptest.NewRequest(http.MethodGet, "/api/traces/abc123", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Equal(t, "abc123", fake.gotID, "handler must forward the traceID to the injected reader")
}

// TestTempoHandler_GetTrace_ReaderError verifies the error path maps to 500.
func TestTempoHandler_GetTrace_ReaderError(t *testing.T) {
	fake := &fakeTraceReader{trace: nil, err: errors.New("boom")}
	h := newTempoHandlersWithFake(fake)

	r := chi.NewRouter()
	r.Get("/api/traces/{traceID}", h.handleTempoGetTrace)

	req := httptest.NewRequest(http.MethodGet, "/api/traces/xyz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, "xyz", fake.gotID)
}

// TestTempoHandler_GetTrace_MissingID verifies request validation.
func TestTempoHandler_GetTrace_MissingID(t *testing.T) {
	fake := &fakeTraceReader{}
	h := newTempoHandlersWithFake(fake)

	// Register without a {traceID} param so chi.URLParam returns "".
	r := chi.NewRouter()
	r.Get("/api/traces/", h.handleTempoGetTrace)

	req := httptest.NewRequest(http.MethodGet, "/api/traces/", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, fake.gotID, "reader must not be called when traceID is missing")
}

// TestTempoHandler_GetTrace_NoReader verifies the nil-reader guard.
func TestTempoHandler_GetTrace_NoReader(t *testing.T) {
	h := &tempoHandlers{traceReader: nil, logger: zap.NewNop()} // no reader wired

	r := chi.NewRouter()
	r.Get("/api/traces/{traceID}", h.handleTempoGetTrace)

	req := httptest.NewRequest(http.MethodGet, "/api/traces/abc123", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
