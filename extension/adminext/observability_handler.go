// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/collector/custom/extension/adminext/observability"
)

// ============================================================================
// Observability Query Handlers
//
// Uses TraceReader and MetricReader abstraction interfaces to query backends.
// Design principles:
//   - Frontend does not access Jaeger/Prometheus directly; all queries go through Admin API auth
//   - TraceReader: abstracts Jaeger Query API (https://www.jaegertracing.io/docs/apis/#http-json)
//   - MetricReader: abstracts Prometheus HTTP API (https://prometheus.io/docs/prometheus/latest/querying/api/)
// ============================================================================

// ============================================================================
// Trace Query API Handlers (via TraceReader interface)
// ============================================================================

// handleGetTraceServices returns all available service names.
// GET /api/v2/observability/traces/services
func (h *obsLegacyHandlers) handleGetTraceServices(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace query backend not configured")
		return
	}

	result, err := h.traceReader.GetServices(r.Context())
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSON(w, result.StatusCode, result.RawBody)
}

// handleGetTraceOperations returns all operations for the specified service.
// GET /api/v2/observability/traces/services/{service}/operations
func (h *obsLegacyHandlers) handleGetTraceOperations(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace query backend not configured")
		return
	}

	service := chi.URLParam(r, "service")
	if service == "" {
		h.writeError(w, http.StatusBadRequest, "service parameter is required")
		return
	}

	result, err := h.traceReader.GetOperations(r.Context(), service)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSON(w, result.StatusCode, result.RawBody)
}

// handleSearchTraces searches for traces matching query parameters.
// GET /api/v2/observability/traces?service=xxx&operation=xxx&tags=xxx&limit=20&start=xxx&end=xxx&minDuration=xxx&maxDuration=xxx
func (h *obsLegacyHandlers) handleSearchTraces(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace query backend not configured")
		return
	}

	query := observability.TraceSearchQuery{
		RawQuery: r.URL.RawQuery,
	}

	result, err := h.traceReader.SearchTraces(r.Context(), query)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSON(w, result.StatusCode, result.RawBody)
}

// handleGetTrace retrieves a single trace by its trace ID.
// GET /api/v2/observability/traces/{traceID}
func (h *obsLegacyHandlers) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace query backend not configured")
		return
	}

	traceID := chi.URLParam(r, "traceID")
	if traceID == "" {
		h.writeError(w, http.StatusBadRequest, "traceID parameter is required")
		return
	}

	result, err := h.traceReader.GetTrace(r.Context(), traceID)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSON(w, result.StatusCode, result.RawBody)
}

// handleGetDependencies returns service dependency links for the Service Map.
// GET /api/v2/observability/dependencies?endTs=xxx&lookback=xxx
// endTs: end timestamp (Unix milliseconds), lookback: lookback duration (milliseconds)
func (h *obsLegacyHandlers) handleGetDependencies(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace query backend not configured")
		return
	}

	// Parse endTs (Unix milliseconds) and lookback (milliseconds) from query params
	endTsStr := r.URL.Query().Get("endTs")
	lookbackStr := r.URL.Query().Get("lookback")

	endTsMs, err := strconv.ParseInt(endTsStr, 10, 64)
	if err != nil || endTsMs <= 0 {
		// Default to current time if not provided or invalid
		endTsMs = time.Now().UnixMilli()
	}
	endTs := time.UnixMilli(endTsMs)

	lookbackMs, err := strconv.ParseInt(lookbackStr, 10, 64)
	if err != nil || lookbackMs <= 0 {
		// Default to 24 hours
		lookbackMs = 24 * 60 * 60 * 1000
	}
	lookback := time.Duration(lookbackMs) * time.Millisecond

	result, err := h.traceReader.GetDependencies(r.Context(), endTs, lookback)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSON(w, result.StatusCode, result.RawBody)
}

// ============================================================================
// Metric Query API Handlers (via MetricReader interface)
// ============================================================================

// handleMetricLabels returns all available label names.
// GET /api/v2/observability/metrics/labels
func (h *obsLegacyHandlers) handleMetricLabels(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	body, err := h.metricReader.GetLabels(r.Context())
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// handleMetricLabelValues returns all values for the specified label name.
// GET /api/v2/observability/metrics/labels/{labelName}/values
func (h *obsLegacyHandlers) handleMetricLabelValues(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	labelName := chi.URLParam(r, "labelName")
	if labelName == "" {
		h.writeError(w, http.StatusBadRequest, "labelName parameter is required")
		return
	}

	body, err := h.metricReader.GetLabelValues(r.Context(), labelName)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// handleMetricQuery executes a Prometheus instant query.
// GET /api/v2/observability/metrics/query?query=xxx&time=xxx
func (h *obsLegacyHandlers) handleMetricQuery(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	body, err := h.metricReader.QueryInstant(r.Context(), r.URL.RawQuery)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// handleMetricQueryRange executes a Prometheus range query.
// GET /api/v2/observability/metrics/query_range?query=xxx&start=xxx&end=xxx&step=xxx
func (h *obsLegacyHandlers) handleMetricQueryRange(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	body, err := h.metricReader.QueryRange(r.Context(), r.URL.RawQuery)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// handleMetricSeries queries Prometheus series metadata.
// GET /api/v2/observability/metrics/series?match[]=xxx
func (h *obsLegacyHandlers) handleMetricSeries(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	body, err := h.metricReader.GetSeries(r.Context(), r.URL.RawQuery)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// handleMetricMetadata queries Prometheus metric metadata.
// GET /api/v2/observability/metrics/metadata?metric=xxx
func (h *obsLegacyHandlers) handleMetricMetadata(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Metric query backend not configured")
		return
	}

	body, err := h.metricReader.GetMetadata(r.Context(), r.URL.RawQuery)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	h.writeRawJSONBytes(w, body)
}

// ============================================================================
// Response Helpers
// ============================================================================

// writeRawJSON / writeRawJSONBytes are defined in observability_deps.go.

