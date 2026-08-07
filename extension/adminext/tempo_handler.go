// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"go.opentelemetry.io/collector/custom/extension/adminext/traceql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/unitconv"
)

// ═══════════════════════════════════════════════════
// Grafana Tempo Compatible HTTP API
// (for Grafana Tempo data source)
//
// Implemented endpoints:
//
//	GET /api/echo                            — health check
//	GET /api/traces/{traceID}                — OTLP JSON (resourceSpans format)
//	GET /api/search                          — trace search
//	GET /api/search/tags                     — tag names
//	GET /api/search/tag/{tagName}/values     — tag values
//
// Grafana configuration:
//
//	Type: Tempo
//	URL:  http://<collector>:8088/api/v2/tempo
//	Access: Server (proxy)
//	Auth: Basic Auth (same as admin API)
// ═══════════════════════════════════════════════════

// ── Tempo OTLP JSON Types ──────────────────────────
// These types precisely match Tempo's jsonpb serialization output, following
// camelCase naming conventions and typed attribute value wrappers as described
// in docs/tempo-api-design.md Section 7.

// tempoTrace is the top-level response for GET /api/traces/{traceID}.
type tempoTrace struct {
	ResourceSpans []tempoResourceSpans `json:"resourceSpans,omitempty"`
}

type tempoResourceSpans struct {
	Resource   tempoResource    `json:"resource,omitempty"`
	ScopeSpans []tempoScopeSpans `json:"scopeSpans,omitempty"`
}

type tempoResource struct {
	Attributes []tempoKeyValue `json:"attributes,omitempty"`
}

type tempoScopeSpans struct {
	Scope tempoScope  `json:"scope,omitempty"`
	Spans []tempoSpan `json:"spans,omitempty"`
}

type tempoScope struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type tempoSpan struct {
	TraceID           string           `json:"traceId"`
	SpanID            string           `json:"spanId"`
	ParentSpanID      string           `json:"parentSpanId,omitempty"`
	Name              string           `json:"name"`
	Kind              int              `json:"kind,omitempty"` // 0=unspecified (omitted)
	StartTimeUnixNano string           `json:"startTimeUnixNano"`
	EndTimeUnixNano   string           `json:"endTimeUnixNano"`
	Attributes        []tempoKeyValue  `json:"attributes,omitempty"`
	Events            []tempoSpanEvent `json:"events,omitempty"`
	Links             []tempoSpanLink  `json:"links,omitempty"`
	Status            *tempoStatus     `json:"status,omitempty"`
}

type tempoStatus struct {
	Code    int    `json:"code,omitempty"` // 0=unset (omitted)
	Message string `json:"message,omitempty"`
}

type tempoKeyValue struct {
	Key   string        `json:"key"`
	Value tempoAnyValue `json:"value"`
}

type tempoAnyValue struct {
	StringValue *string            `json:"stringValue,omitempty"`
	IntValue    *string            `json:"intValue,omitempty"` // int64 as string (proto jsonpb compatibility)
	DoubleValue *float64           `json:"doubleValue,omitempty"`
	BoolValue   *bool              `json:"boolValue,omitempty"`
	Value       *tempoAnyValueAlt  `json:"Value,omitempty"` // proto backward-compatible fallback format
}

// tempoAnyValueAlt provides proto-compatible snake_case field names
// used as a fallback by Grafana traces-drilldown for older backends.
type tempoAnyValueAlt struct {
	StringValue *string  `json:"string_value,omitempty"`
	IntValue    *string  `json:"int_value,omitempty"`
	DoubleValue *float64 `json:"double_value,omitempty"`
	BoolValue   *bool    `json:"bool_value,omitempty"`
}

type tempoSpanEvent struct {
	TimeUnixNano string           `json:"timeUnixNano"`
	Name         string           `json:"name"`
	Attributes   []tempoKeyValue  `json:"attributes,omitempty"`
}

type tempoSpanLink struct {
	TraceID    string          `json:"traceId"`
	SpanID     string          `json:"spanId"`
	Attributes []tempoKeyValue `json:"attributes,omitempty"`
}

// ── Tempo Search Response Types ────────────────────

type tempoSearchResponse struct {
	Traces  []tempoSearchTrace `json:"traces"`
	Metrics tempoSearchMetrics `json:"metrics"`
}

type tempoSearchTrace struct {
	TraceID           string          `json:"traceID"`
	RootServiceName   string          `json:"rootServiceName"`
	RootTraceName     string          `json:"rootTraceName"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	DurationMs        int64           `json:"durationMs"`
	SpanSets          []tempoSpanSet  `json:"spanSets"`
}

type tempoSpanSet struct {
	Spans   []tempoSearchSpan `json:"spans"`
	Matched int               `json:"matched"`
}

type tempoSearchSpan struct {
	SpanID            string           `json:"spanID"`
	Name              string           `json:"name,omitempty"`
	StartTimeUnixNano string           `json:"startTimeUnixNano"`
	DurationNanos     string           `json:"durationNanos"`
	Attributes        []tempoKeyValue  `json:"attributes"`
}

type tempoSearchMetrics struct {
	InspectedTraces int64  `json:"inspectedTraces"`
	InspectedBytes  string `json:"inspectedBytes"`
}

// ── Tempo Tags Response Types ──────────────────────

type tempoTagNamesResponse struct {
	TagNames []string           `json:"tagNames"`
	Metrics  tempoSearchMetrics `json:"metrics"`
}

type tempoTagValuesResponse struct {
	TagValues []string           `json:"tagValues"`
	Metrics   tempoSearchMetrics `json:"metrics"`
}

// ── Tempo V2 Tags Types ────────────────────────────
// Grafana may call both V1 (/api/search/tags) and V2 (/api/v2/search/tags).
// V2 returns tags grouped by scope (resource/span/intrinsic) and values with type info.

type tempoV2TagNamesResponse struct {
	Scopes  []tempoV2Scope    `json:"scopes"`
	Metrics tempoSearchMetrics `json:"metrics"`
}

type tempoV2Scope struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type tempoV2TagValuesResponse struct {
	TagValues []tempoV2TagValue `json:"tagValues"`
	Metrics   tempoSearchMetrics `json:"metrics"`
}

type tempoV2TagValue struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ── Tempo Metrics Types ────────────────────────────
// Response format for /api/metrics/query_range (TraceQL metrics).
// Returns Prometheus-compatible time series with labels as key/value pairs.

type tempoMetricsResponse struct {
	Series  []tempoMetricSeries `json:"series"`
	Metrics tempoSearchMetrics  `json:"metrics"`
}

type tempoMetricSeries struct {
	Labels  []tempoMetricLabel  `json:"labels"`
	Samples []tempoMetricSample `json:"samples"`
}

type tempoMetricLabel struct {
	Key   string        `json:"key"`
	Value tempoAnyValue `json:"value"`
}

type tempoMetricSample struct {
	TimestampMs int64   `json:"timestampMs"`
	Value       float64 `json:"value"`
}

// ── Static Data ────────────────────────────────────

// intrinsic tags that Grafana Tempo expects.
// tempoIntrinsicTags lists intrinsic span properties that are reported in the
// /api/v2/search/tags response for Tempo API compatibility.
//
// Value retrieval support (per resolveIntrinsicTagValuesWithFilter):
//
//	✅ duration  — not queried via values endpoint (range filter)
//	✅ kind      — static values (unspecified/internal/server/client/producer/consumer)
//	✅ name      — queried from ES top-level "name" field via GetOperations
//	✅ status    — static values (unset/ok/error)
//	❌ statusMessage   — nested ES field "status.message", not yet queryable (TODO)
//	❌ rootName        — not stored in ES; would require trace root span derivation (TODO)
//	❌ rootServiceName — not stored in ES; would require trace root span derivation (TODO)
var tempoIntrinsicTags = TempoIntrinsicTags

// span kind values reported by Tempo.
var tempoSpanKindValues = TempoSpanKindValues

// status code values.
var tempoStatusCodeValues = TempoStatusCodeValues

// emptyMetrics is reused for responses where no inspection metrics are tracked.
var emptyMetrics = tempoSearchMetrics{InspectedBytes: "0"}

// ── Handler: /api/echo ─────────────────────────────

// handleTempoEcho handles GET /api/echo (health check).
func (h *tempoHandlers) handleTempoEcho(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Handler: /api/status/buildinfo ─────────────────

// handleTempoBuildInfo handles GET /api/status/buildinfo.
// Grafana 12+ probes this endpoint to detect backend capabilities.
// Returning a proper response helps Grafana correctly determine API version support.
func (h *tempoHandlers) handleTempoBuildInfo(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{
		"version":   "customcol-1.0.0",
		"branch":    "main",
		"revision":  "unknown",
		"goVersion": "go1.25.0",
	})
}

// ── Handler: /api/traces/{traceID} ─────────────────

// handleTempoGetTrace handles GET /api/traces/{traceID}.
func (h *tempoHandlers) handleTempoGetTrace(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	traceID := chi.URLParam(r, "traceID")
	if traceID == "" {
		h.writeError(w, http.StatusBadRequest, "traceID parameter is required")
		return
	}

	trace, err := h.traceReader.GetTrace(r.Context(), traceID)
	if err != nil {
		h.logger.Error("tempo get trace failed", zap.String("traceID", traceID), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if trace == nil || len(trace.Spans) == 0 {
		h.writeError(w, http.StatusNotFound, "trace not found")
		return
	}

	tt := convertToTempoTrace(trace)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	h.writeJSON(w, http.StatusOK, tt)
}

// handleTempoV2GetTrace handles GET /api/v2/traces/{traceID}.
// Returns OTLP protobuf binary (Grafana 12+ expects protobuf for V2 endpoints).
func (h *tempoHandlers) handleTempoV2GetTrace(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	traceID := chi.URLParam(r, "traceID")
	if traceID == "" {
		h.writeError(w, http.StatusBadRequest, "traceID parameter is required")
		return
	}

	trace, err := h.traceReader.GetTrace(r.Context(), traceID)
	if err != nil {
		h.logger.Error("tempo v2 get trace failed", zap.String("traceID", traceID), zap.Error(err))
		SpanFromContext(r.Context()).SetAttributes(attribute.String("tempo.v2.error", "get_trace_failed"))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if trace == nil || len(trace.Spans) == 0 {
		SpanFromContext(r.Context()).SetAttributes(attribute.String("tempo.v2.error", "trace_not_found"))
		h.writeError(w, http.StatusNotFound, "trace not found")
		return
	}

	pbBytes, err := convertTraceToProtobuf(trace)
	if err != nil {
		h.logger.Error("tempo v2 protobuf conversion failed",
			zap.String("traceID", traceID),
			zap.Int("spans", len(trace.Spans)),
			zap.Error(err),
		)
		h.writeError(w, http.StatusInternalServerError, "failed to encode trace as protobuf")
		return
	}

	// Safety: proto.Marshal can return 0 bytes for an empty TracesData.
	if len(pbBytes) == 0 {
		h.logger.Error("tempo v2 protobuf produced empty body",
			zap.String("traceID", traceID),
			zap.Int("spans", len(trace.Spans)),
		)
		h.writeError(w, http.StatusInternalServerError, "trace data could not be encoded (empty protobuf body)")
		return
	}

	// Wrap TracesData bytes as tempopb.TraceByIDResponse wire format.
	// Grafana 12+ V2 endpoint expects TraceByIDResponse{trace: Trace{resourceSpans: ...}}.
	// Since TracesData and Trace share identical wire format (field 1 = repeated ResourceSpans),
	// the TracesData bytes serve directly as the Trace message body.
	responseBytes := wrapAsTraceByIDResponse(pbBytes)

	w.Header().Set("Content-Type", "application/protobuf")
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBytes)))

	// Expose diagnostics via response headers (visible in istio/proxy access logs).
	w.Header().Set("X-Tempo-V2-PbBytes", strconv.Itoa(len(responseBytes)))
	w.Header().Set("X-Tempo-V2-Spans", strconv.Itoa(len(trace.Spans)))
	w.Header().Set("X-Tempo-V2-TracesData-Bytes", strconv.Itoa(len(pbBytes)))

	w.WriteHeader(http.StatusOK)
	n, err := w.Write(responseBytes)

	// Record in tracing span (removed IsRecording check — SetAttributes is no-op for non-recording spans).
	SpanFromContext(r.Context()).SetAttributes(
		attribute.String("tempo.v2.trace_id", traceID),
		attribute.Int("tempo.v2.spans_count", len(trace.Spans)),
		attribute.Int("tempo.v2.protobuf_bytes_expected", len(responseBytes)),
		attribute.Int("tempo.v2.protobuf_bytes_written", n),
	)
	if err != nil {
		SpanFromContext(r.Context()).SetAttributes(attribute.String("tempo.v2.write_error", err.Error()))
		h.logger.Error("tempo v2 write protobuf body failed",
			zap.String("traceID", traceID),
			zap.Int("bytesExpected", len(responseBytes)),
			zap.Error(err),
		)
		return
	}
}

// convertToTempoTrace converts an internal Trace into Tempo OTLP JSON format.
// Spans are grouped by service name (→ resourceSpans.resource.service.name) and
// then by a default scope. Span Kind is converted from string to int, Status Code
// likewise. Zero values (kind=0, code=0) are omitted via omitempty.
func convertToTempoTrace(trace *observabilitystorageext.Trace) tempoTrace {
	if trace == nil || len(trace.Spans) == 0 {
		return tempoTrace{}
	}

	// Group spans by service name (each service becomes a resourceSpans).
	type serviceGroup struct {
		serviceName string
		resource    map[string]any // resource attributes from first span
		spans       []observabilitystorageext.Span
	}

	var groups []serviceGroup
	seen := make(map[string]int) // serviceName → index in groups

	for _, span := range trace.Spans {
		svc := span.ServiceName
		if svc == "" {
			svc = "unknown"
		}
		if idx, ok := seen[svc]; ok {
			groups[idx].spans = append(groups[idx].spans, span)
		} else {
			// Build resource attributes from span's Resource field.
			resAttrs := make(map[string]any)
			for _, kv := range span.Resource {
				resAttrs[kv.Key] = kv.Value
			}
			seen[svc] = len(groups)
			groups = append(groups, serviceGroup{
				serviceName: svc,
				resource:    resAttrs,
				spans:       []observabilitystorageext.Span{span},
			})
		}
	}

	resourceSpans := make([]tempoResourceSpans, 0, len(groups))
	for _, g := range groups {
		rs := tempoResourceSpans{
			Resource: tempoResource{
				Attributes: mapToTempoKeyValues(g.resource),
			},
		}

		// All spans in a service group share the same scope for now.
		// Future enhancement: group by actual InstrumentationScope.
		tempoSpans := make([]tempoSpan, 0, len(g.spans))
		for _, s := range g.spans {
			tempoSpans = append(tempoSpans, publicSpanToTempoSpan(s))
		}

		rs.ScopeSpans = append(rs.ScopeSpans, tempoScopeSpans{
			Scope: tempoScope{Name: "opentelemetry"},
			Spans: tempoSpans,
		})
		resourceSpans = append(resourceSpans, rs)
	}

	return tempoTrace{ResourceSpans: resourceSpans}
}

// publicSpanToTempoSpan converts a public Span to Tempo-compatible format.
func publicSpanToTempoSpan(s observabilitystorageext.Span) tempoSpan {
	ts := tempoSpan{
		TraceID:           s.TraceID,
		SpanID:            s.SpanID,
		ParentSpanID:      s.ParentSpanID,
		Name:              s.Name,
		Kind:              spanKindToInt(s.Kind),
		StartTimeUnixNano: s.StartTimeUnixNano,
		EndTimeUnixNano:   s.EndTimeUnixNano,
		Attributes:        publicKeyValuesToTempo(s.Attributes),
		Events:            publicEventsToTempo(s.Events),
		Links:             publicLinksToTempo(s.Links),
	}

	if s.Status.Code != "" {
		ts.Status = &tempoStatus{
			Code:    statusCodeToInt(s.Status.Code),
			Message: s.Status.Message,
		}
	}
	return ts
}

// ── Handler: /api/search ───────────────────────────

// handleTempoSearch handles GET /api/search.
// Uses SearchTraceSummaries for lightweight single-query search (no bulk span fetch).
func (h *tempoHandlers) handleTempoSearch(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	rawQuery := r.URL.RawQuery

	plan, query, err := parseTempoSearchParams(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Extract select fields from plan (for | select(...) projection).
	var selectFields []string
	if plan != nil {
		selectFields = plan.SelectFields
	}

	// ── Metrics Query Path ──
	// When the query contains a metrics pipeline stage (rate/quantile_over_time/etc.),
	// execute as an ES aggregation query and return time-series data.
	if plan != nil && plan.HasMetrics && plan.MetricsStage != nil {
		h.executeTempoMetricsQuery(w, r, plan, query)
		return
	}

	// Parse spss (spans per span set) from query params, default 3.
	spss := 3
	if v := r.FormValue("spss"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			spss = n
		}
	}

	// traceDuration post-filter: over-fetch to compensate for post-filtering.
	originalLimit := query.Limit
	hasTraceDurationFilter := plan != nil && (plan.TraceMinDuration > 0 || plan.TraceMaxDuration > 0)
	if hasTraceDurationFilter {
		query.Limit *= traceDurationOverfetchFactor
	}

	// Lightweight search — returns only root info + first spss spans per trace.
	result, err := h.traceReader.SearchTraceSummaries(r.Context(), query, spss)
	if err != nil {
		h.logger.Error("tempo search failed", zap.String("query", rawQuery), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Apply traceDuration post-filter (before structural, to reduce its work).
	if hasTraceDurationFilter {
		result.Summaries = h.filterByTraceDuration(r.Context(), result.Summaries, plan, query)
		if len(result.Summaries) > originalLimit {
			result.Summaries = result.Summaries[:originalLimit]
		}
		query.Limit = originalLimit // restore for structural post-filter
	}

	// If the query has structural operators, do two-phase evaluation:
	// 1. ES broad search (already done above)
	// 2. Per-trace structural matching (fetch full traces, verify structure)
	if plan != nil && plan.HasStructural && plan.FullAST != nil {
		verified := h.structuralPostFilter(r.Context(), result.Summaries, plan, query.Limit)
		h.logger.Debug("tempo structural search completed",
			zap.String("query", rawQuery),
			zap.Int("candidates", len(result.Summaries)),
			zap.Int("verified", len(verified)),
		)
		// Build response from verified traces with matched span info.
		searchTraces := make([]tempoSearchTrace, 0, len(verified))
		for _, sr := range verified {
			st := convertStructuralResultToTempoSearchTrace(sr, selectFields, spss)
			searchTraces = append(searchTraces, st)
		}
		resp := tempoSearchResponse{
			Traces: searchTraces,
			Metrics: tempoSearchMetrics{
				InspectedTraces: result.Total,
				InspectedBytes:  "0",
			},
		}
		h.writeJSON(w, http.StatusOK, resp)
		return
	}

	// Build Tempo-format search response from summaries (non-structural path).
	searchTraces := make([]tempoSearchTrace, 0, len(result.Summaries))
	for _, s := range result.Summaries {
		st := convertTraceSummaryToTempoSearchTrace(s, selectFields)
		searchTraces = append(searchTraces, st)
	}

	resp := tempoSearchResponse{
		Traces: searchTraces,
		Metrics: tempoSearchMetrics{
			InspectedTraces: result.Total,
			InspectedBytes:  "0",
		},
	}

	h.writeJSON(w, http.StatusOK, resp)
	h.logger.Debug("tempo search completed",
		zap.String("query", rawQuery),
		zap.Int("returned", len(searchTraces)),
		zap.Int64("total", result.Total),
	)
}

// traceDurationOverfetchFactor controls how many extra candidate traces to
// fetch when a traceDuration post-filter is active. Post-filtering reduces
// the result set, so we over-fetch to compensate.
const traceDurationOverfetchFactor = 5

// filterByTraceDuration post-filters candidate trace summaries by their actual
// wall-clock duration (max(endTime) - min(startTime) across all spans). This is
// a two-step process: the ES search (Step 1) finds candidates by span-level
// filters; this method (Step 2) queries ES for per-trace min/max times, computes
// the real trace duration, and filters. On error, returns candidates unfiltered
// (robustness — never block the search on a post-filter failure).
func (h *tempoHandlers) filterByTraceDuration(ctx context.Context, summaries []observabilitystorageext.TraceSummary, plan *traceql.ExecutionPlan, query observabilitystorageext.TraceQuery) []observabilitystorageext.TraceSummary {
	if len(summaries) == 0 {
		return summaries
	}

	traceIDs := make([]string, 0, len(summaries))
	for _, s := range summaries {
		traceIDs = append(traceIDs, s.TraceID)
	}

	durations, err := h.traceReader.QueryTraceDurations(ctx, traceIDs, query)
	if err != nil {
		h.logger.Warn("traceDuration filter failed, returning unfiltered", zap.Error(err))
		return summaries
	}

	filtered := make([]observabilitystorageext.TraceSummary, 0, len(summaries))
	for _, s := range summaries {
		dur, ok := durations[s.TraceID]
		if !ok {
			// Trace not in duration map (no spans in range?) — keep it.
			filtered = append(filtered, s)
			continue
		}
		if plan.TraceMinDuration > 0 && dur < int64(plan.TraceMinDuration) {
			continue
		}
		if plan.TraceMaxDuration > 0 && dur > int64(plan.TraceMaxDuration) {
			continue
		}
		filtered = append(filtered, s)
	}

	h.logger.Debug("traceDuration post-filter applied",
		zap.Int("candidates", len(summaries)),
		zap.Int("filtered", len(filtered)),
	)

	return filtered
}

// structuralPostFilter performs two-phase evaluation for queries with structural operators.
// Phase 1 (ES broad search) is already done — we have candidate TraceSummaries.
// Phase 2: for each candidate, fetch full trace spans and verify the structural relationship.
// Returns only the summaries whose traces contain at least one matching structural pair.
//
// Performance: capped at maxStructuralTraces (default 50) to avoid excessive trace fetches.
const maxStructuralTraces = 50

// structuralVerifyResult holds the result of structural verification for a single trace.
type structuralVerifyResult struct {
	summary       observabilitystorageext.TraceSummary
	fullSpans     []observabilitystorageext.Span
	matchedSpanIDs map[string]bool // spanIDs that matched the structural expression
}

// nestedSetInfo holds the computed nested set model values for a span.
type nestedSetInfo struct {
	Parent int // parent span's Left value; -1 for root
	Left   int // DFS pre-order entry number
	Right  int // DFS pre-order exit number
}

// structuralPostFilterConcurrency controls the maximum number of concurrent
// GetTrace calls during structural post-filtering. Each call fetches a full
// trace from the storage backend (e.g., ES), which is IO-bound.
// 10 workers provides ~5x speedup for typical maxStructuralTraces=50 batches
// without overloading the storage backend.
const structuralPostFilterConcurrency = 10

func (h *tempoHandlers) structuralPostFilter(
	ctx context.Context,
	candidates []observabilitystorageext.TraceSummary,
	plan *traceql.ExecutionPlan,
	limit int,
) []structuralVerifyResult {
	if len(candidates) == 0 || plan.FullAST == nil {
		return nil
	}

	// Cap the number of traces to evaluate structurally.
	evalCount := len(candidates)
	if evalCount > maxStructuralTraces {
		evalCount = maxStructuralTraces
		h.logger.Debug("structural search: capping evaluation",
			zap.Int("candidates", len(candidates)),
			zap.Int("max", maxStructuralTraces),
		)
	}

	// Concurrent GetTrace + structural evaluation.
	// Each goroutine independently fetches a trace from storage and evaluates
	// the structural condition. Results are collected via a buffered channel
	// sized to prevent blocking.
	type verifyResult struct {
		sr *structuralVerifyResult
	}

	resultCh := make(chan verifyResult, evalCount)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(structuralPostFilterConcurrency)

	for i := 0; i < evalCount; i++ {
		s := candidates[i]
		g.Go(func() error {
			trace, err := h.traceReader.GetTrace(gctx, s.TraceID)
			if err != nil {
				h.logger.Debug("structural search: skip trace fetch error",
					zap.String("traceID", s.TraceID), zap.Error(err))
				return nil
			}
			if trace == nil || len(trace.Spans) == 0 {
				return nil
			}

			// Convert to evaluator-friendly SpanData and evaluate structurally.
			spanData := convertSpansToSpanData(trace.Spans)
			result := traceql.EvaluateTraceStructural(plan.FullAST, spanData)
			if result == nil || !result.HasMatch {
				return nil
			}
			result.SetTraceResultTraceID(s.TraceID)

			// Collect matched span IDs.
			matchedIDs := make(map[string]bool, len(result.Matches)*2)
			for _, m := range result.Matches {
				matchedIDs[m.LeftSpanID] = true
				matchedIDs[m.RightSpanID] = true
			}

			resultCh <- verifyResult{sr: &structuralVerifyResult{
				summary:        s,
				fullSpans:      trace.Spans,
				matchedSpanIDs: matchedIDs,
			}}
			return nil
		})
	}

	// Close result channel after all goroutines complete.
	go func() {
		_ = g.Wait()
		close(resultCh)
	}()

	// Collect all verified results.
	var verified []structuralVerifyResult
	for vr := range resultCh {
		verified = append(verified, *vr.sr)
	}

	// Apply limit if specified.
	if limit > 0 && len(verified) > limit {
		verified = verified[:limit]
	}

	return verified
}

// convertSpansToSpanData converts observabilitystorageext Span objects to evaluator SpanData.
func convertSpansToSpanData(spans []observabilitystorageext.Span) []traceql.SpanData {
	result := make([]traceql.SpanData, 0, len(spans))
	for _, s := range spans {
		sd := traceql.SpanData{
			SpanID:       s.SpanID,
			ParentSpanID: s.ParentSpanID,
			Name:         s.Name,
			Kind:         spanKindToString(s.Kind),
			ServiceName:  s.ServiceName,
			StatusCode:   spanStatusToString(s.Status),
		}

		// Parse timestamps.
		if t, err := parseInt64(s.StartTimeUnixNano); err == nil {
			sd.StartUnixNano = t
		}
		if t, err := parseInt64(s.EndTimeUnixNano); err == nil {
			sd.EndUnixNano = t
		}
		if t, err := parseInt64(s.DurationNano); err == nil {
			sd.DurationNano = t
		}

		// Flatten attributes.
		sd.Attributes = make(map[string]string, len(s.Attributes))
		for _, attr := range s.Attributes {
			sd.Attributes[attr.Key] = keyValueString(attr)
		}

		// Flatten resource attributes.
		sd.Resource = make(map[string]string, len(s.Resource))
		for _, attr := range s.Resource {
			sd.Resource[attr.Key] = keyValueString(attr)
		}

		result = append(result, sd)
	}
	return result
}

// spanKindToString converts SpanKind to lowercase short form.
// Handles all formats stored in ES: OTel enum ("SPAN_KIND_SERVER"), ptrace.SpanKind.String() ("Server"),
// and lowercase ("server"). Delegates to NormalizeSpanKind for robust multi-format normalization.
func spanKindToString(kind observabilitystorageext.SpanKind) string {
	normalized := observabilitystorageext.NormalizeSpanKind(string(kind))
	switch normalized {
	case observabilitystorageext.SpanKindInternal:
		return "internal"
	case observabilitystorageext.SpanKindServer:
		return "server"
	case observabilitystorageext.SpanKindClient:
		return "client"
	case observabilitystorageext.SpanKindProducer:
		return "producer"
	case observabilitystorageext.SpanKindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

// spanStatusToString converts SpanStatus to the status code string ("ok"/"error"/"unset").
// Handles all formats stored in ES: OTel enum ("STATUS_CODE_OK"), ptrace.StatusCode.String() ("Ok"),
// and lowercase ("ok"). Delegates to NormalizeStatusCode for robust multi-format normalization.
func spanStatusToString(status observabilitystorageext.SpanStatus) string {
	normalized := observabilitystorageext.NormalizeStatusCode(string(status.Code))
	switch normalized {
	case observabilitystorageext.StatusCodeOk:
		return "ok"
	case observabilitystorageext.StatusCodeError:
		return "error"
	default:
		return "unset"
	}
}

// keyValueString converts a KeyValue's AnyValue to its string representation.
func keyValueString(kv observabilitystorageext.KeyValue) string {
	if kv.Value.StringValue != nil {
		return *kv.Value.StringValue
	}
	if kv.Value.IntValue != nil {
		return fmt.Sprintf("%d", *kv.Value.IntValue)
	}
	if kv.Value.DoubleValue != nil {
		return fmt.Sprintf("%f", *kv.Value.DoubleValue)
	}
	if kv.Value.BoolValue != nil {
		if *kv.Value.BoolValue {
			return "true"
		}
		return "false"
	}
	return ""
}

// parseInt64 safely parses a string to int64.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// ── Handler: /api/v2/search ────────────────────────

// handleTempoV2Search handles GET /api/v2/search.
// Returns OTLP protobuf binary containing full trace data for matching traces.
// Grafana 12+ expects protobuf for all V2 endpoints including search.
func (h *tempoHandlers) handleTempoV2Search(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	_, query, err := parseTempoSearchParams(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Tempo's /api/v2/search returns the SAME JSON search response as /api/search.
	// OTLP protobuf encoding is reserved for /api/v2/traces/{id}, NOT search — a
	// binary body here breaks Grafana (Grafana 12+ defaults to the V2 search path).
	spss := 3
	if v := r.FormValue("spss"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			spss = n
		}
	}

	result, err := h.traceReader.SearchTraceSummaries(r.Context(), query, spss)
	if err != nil {
		h.logger.Error("tempo v2 search failed", zap.String("query", r.URL.RawQuery), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	searchTraces := make([]tempoSearchTrace, 0, len(result.Summaries))
	for _, s := range result.Summaries {
		searchTraces = append(searchTraces, convertTraceSummaryToTempoSearchTrace(s, nil))
	}

	resp := tempoSearchResponse{
		Traces: searchTraces,
		Metrics: tempoSearchMetrics{
			InspectedTraces: result.Total,
			InspectedBytes:  "0",
		},
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// convertTraceSummaryToTempoSearchTrace converts a TraceSummary to a Tempo search result entry.
func convertTraceSummaryToTempoSearchTrace(s observabilitystorageext.TraceSummary, selectFields []string) tempoSearchTrace {
	st := tempoSearchTrace{
		TraceID:           s.TraceID,
		RootServiceName:   s.RootServiceName,
		RootTraceName:     s.RootSpanName,
		StartTimeUnixNano: s.StartTimeUnixNano,
		DurationMs:        s.DurationMs,
	}

	// Pre-compute nested set model if select requests nested set fields.
	var nsInfo map[string]nestedSetInfo
	if needsNestedSet(selectFields) {
		nsInfo = computeNestedSet(s.SpanSet)
	}

	searchSpans := make([]tempoSearchSpan, 0, len(s.SpanSet))
	for _, sp := range s.SpanSet {
		projected := projectSpanWithSelect(sp, selectFields, nsInfo)
		searchSpans = append(searchSpans, tempoSearchSpan{
			SpanID:            sp.SpanID,
			Name:              projected.Name,
			StartTimeUnixNano: sp.StartTimeUnixNano,
			DurationNanos:     sp.DurationNano,
			Attributes:        projected.Attributes,
		})
	}

	st.SpanSets = []tempoSpanSet{{
		Spans:   searchSpans,
		Matched: len(searchSpans),
	}}
	return st
}

// convertStructuralResultToTempoSearchTrace builds a tempoSearchTrace from a structural
// verification result. Uses full trace data to:
// 1. Build spanSet entries from structurally matched spans
// 2. Apply select projection if selectFields is non-empty
//
// Special case: when selectFields includes nestedSet fields (nestedSetParent/Left/Right),
// Grafana needs ALL spans in the trace to render the Service Structure hierarchy tree.
// In this case, we return all spans (up to spss) rather than just matched endpoints.
func convertStructuralResultToTempoSearchTrace(
	sr structuralVerifyResult,
	selectFields []string,
	spss int,
) tempoSearchTrace {
	st := tempoSearchTrace{
		TraceID:           sr.summary.TraceID,
		RootServiceName:   sr.summary.RootServiceName,
		RootTraceName:     sr.summary.RootSpanName,
		StartTimeUnixNano: sr.summary.StartTimeUnixNano,
		DurationMs:        sr.summary.DurationMs,
	}

	// Pre-compute nested set model on full trace spans if select requests nested set fields.
	includeAllSpans := needsNestedSet(selectFields)
	var nsInfo map[string]nestedSetInfo
	if includeAllSpans {
		nsInfo = computeNestedSet(sr.fullSpans)
	}

	// Build spanSet.
	// When nestedSet fields are selected (Grafana Service Structure), include ALL spans
	// so the frontend can reconstruct the full hierarchy tree.
	// Otherwise, only include structurally matched spans.
	var resultSpans []tempoSearchSpan
	for _, sp := range sr.fullSpans {
		if !includeAllSpans && !sr.matchedSpanIDs[sp.SpanID] {
			continue
		}
		projected := projectSpanWithSelect(sp, selectFields, nsInfo)
		resultSpans = append(resultSpans, tempoSearchSpan{
			SpanID:            sp.SpanID,
			Name:              projected.Name,
			StartTimeUnixNano: sp.StartTimeUnixNano,
			DurationNanos:     sp.DurationNano,
			Attributes:        projected.Attributes,
		})
		// Limit spans per spanSet (spss).
		if len(resultSpans) >= spss {
			break
		}
	}

	matchedCount := len(sr.fullSpans)
	if includeAllSpans {
		matchedCount = len(sr.fullSpans)
	} else {
		matchedCount = len(sr.matchedSpanIDs)
	}
	if len(resultSpans) == 0 {
		resultSpans = make([]tempoSearchSpan, 0)
	}

	st.SpanSets = []tempoSpanSet{{
		Spans:   resultSpans,
		Matched: matchedCount,
	}}
	return st
}

// ── Nested Set Model ──────────────────────────────

// computeNestedSet computes nested set model (left/right/parent) for a list of spans
// using DFS traversal based on parentSpanID relationships.
// Returns a map from spanID to nestedSetInfo.
func computeNestedSet(spans []observabilitystorageext.Span) map[string]nestedSetInfo {
	if len(spans) == 0 {
		return nil
	}

	// Build adjacency: parentSpanID → children
	children := make(map[string][]string, len(spans))
	spanIndex := make(map[string]int, len(spans))
	var roots []string
	for i, sp := range spans {
		spanIndex[sp.SpanID] = i
		if sp.ParentSpanID == "" {
			roots = append(roots, sp.SpanID)
		} else {
			children[sp.ParentSpanID] = append(children[sp.ParentSpanID], sp.SpanID)
		}
	}

	// Sort children by start time for deterministic order.
	for pid := range children {
		sort.Slice(children[pid], func(i, j int) bool {
			si := spanIndex[children[pid][i]]
			sj := spanIndex[children[pid][j]]
			return spans[si].StartTimeUnixNano < spans[sj].StartTimeUnixNano
		})
	}

	// Handle orphan spans (parentSpanID not found in this span set).
	for _, sp := range spans {
		if sp.ParentSpanID != "" {
			if _, exists := spanIndex[sp.ParentSpanID]; !exists {
				roots = append(roots, sp.SpanID)
			}
		}
	}

	// Sort roots by start time.
	sort.Slice(roots, func(i, j int) bool {
		si := spanIndex[roots[i]]
		sj := spanIndex[roots[j]]
		return spans[si].StartTimeUnixNano < spans[sj].StartTimeUnixNano
	})

	// DFS traversal to assign left/right numbers.
	result := make(map[string]nestedSetInfo, len(spans))
	counter := 1

	var dfs func(spanID string, parentLeft int)
	dfs = func(spanID string, parentLeft int) {
		left := counter
		counter++
		cids := children[spanID]
		for _, childID := range cids {
			dfs(childID, left)
		}
		right := counter
		counter++
		result[spanID] = nestedSetInfo{
			Parent: parentLeft,
			Left:   left,
			Right:  right,
		}
	}

	for _, rootID := range roots {
		dfs(rootID, -1)
	}

	// Spans not reached by DFS (disconnected) get fallback values.
	for _, sp := range spans {
		if _, ok := result[sp.SpanID]; !ok {
			left := counter
			counter++
			right := counter
			counter++
			result[sp.SpanID] = nestedSetInfo{Parent: -1, Left: left, Right: right}
		}
	}

	return result
}

// needsNestedSet checks if any select field requires nested set computation.
func needsNestedSet(selectFields []string) bool {
	for _, f := range selectFields {
		switch f {
		case TempoIntrinsicNestedSetParent, TempoIntrinsicNestedSetLeft, TempoIntrinsicNestedSetRight:
			return true
		}
	}
	return false
}

// ── Handler: /api/search/tags ──────────────────────

// handleTempoSearchTags handles GET /api/search/tags.
func (h *tempoHandlers) handleTempoSearchTags(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	tagNames := make([]string, 0, 20)

	// Always include service.name (core tag).
	tagNames = append(tagNames, "service.name")

	// Intrinsic tags.
	tagNames = append(tagNames, tempoIntrinsicTags...)

	// Common span attribute keys.
	tagNames = append(tagNames, TempoV1CommonSpanAttributeKeys...)

	// Try to fetch services from backend for additional tag discovery.
	timeRange := parseTimeRange(r)
	services, err := h.traceReader.GetServices(r.Context(), timeRange)
	if err != nil {
		h.logger.Debug("tempo tags: could not fetch services", zap.Error(err))
	}

	resp := tempoTagNamesResponse{
		TagNames: tagNames,
		Metrics:  emptyMetrics,
	}
	// Add operations as hints for tag discovery (optional).
	_ = services

	h.writeJSON(w, http.StatusOK, resp)
}

// ── Handler: /api/search/tag/{tagName}/values ───────

// handleTempoSearchTagValues handles GET /api/search/tag/{tagName}/values.
func (h *tempoHandlers) handleTempoSearchTagValues(w http.ResponseWriter, r *http.Request) {
	if h.traceReader == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	tagName := chi.URLParam(r, "tagName")
	if tagName == "" {
		h.writeError(w, http.StatusBadRequest, "tagName parameter is required")
		return
	}

	values, err := h.resolveTagValues(r, tagName)
	if err != nil {
		h.logger.Error("tempo tag values fetch failed", zap.String("tag", tagName), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, tempoTagValuesResponse{
		TagValues: values,
		Metrics:   emptyMetrics,
	})
}

// resolveTagValues returns distinct values for a given tag name.
func (h *tempoHandlers) resolveTagValues(r *http.Request, tagName string) ([]string, error) {
	// Use the wider default window (7d) like the V2 tag-values handler, so that
	// value discovery is not limited to the last hour (which made the V1
	// service.name values come back empty while V2 returned results).
	timeRange := parseTempoTagValuesTimeRange(r)

	switch tagName {
	case "service.name":
		services, err := h.traceReader.GetServices(r.Context(), timeRange)
		if err != nil {
			return nil, fmt.Errorf("get services: %w", err)
		}
		values := make([]string, len(services))
		for i, s := range services {
			values[i] = s.Name
		}
		return values, nil

	case OTelAttrSpanKind, TempoIntrinsicKind:
		return tempoSpanKindValues, nil

	case TempoIntrinsicStatus, "status.code":
		return tempoStatusCodeValues, nil

	case TempoIntrinsicName:
		// For "name" tag, try fetching operations from all services.
		return h.fetchAllOperations(r.Context(), timeRange)

	default:
		// For intrinsic tags, return empty (Grafana will handle gracefully).
		for _, it := range tempoIntrinsicTags {
			if it == tagName {
				return nil, nil
			}
		}
		// For unknown tags, return empty list (P1 will add ES aggregation support).
		return nil, nil
	}
}

// fetchAllOperations gathers operation names across all services in the time range.
func (h *tempoHandlers) fetchAllOperations(ctx context.Context, timeRange observabilitystorageext.TimeRange) ([]string, error) {
	services, err := h.traceReader.GetServices(ctx, timeRange)
	if err != nil {
		return nil, fmt.Errorf("get services: %w", err)
	}

	seen := make(map[string]struct{})
	for _, svc := range services {
		ops, err := h.traceReader.GetOperations(ctx, svc.Name, timeRange)
		if err != nil {
			h.logger.Debug("tempo tags: could not fetch operations", zap.String("service", svc.Name), zap.Error(err))
			continue
		}
		for _, op := range ops {
			seen[op.Name] = struct{}{}
		}
	}

	values := make([]string, 0, len(seen))
	for k := range seen {
		values = append(values, k)
	}
	return values, nil
}

// ── Handler: /api/v2/search/tags ───────────────────

// handleTempoV2SearchTags handles GET /api/v2/search/tags.
// Returns tags grouped by scope: resource, span, intrinsic.
func (h *tempoHandlers) handleTempoV2SearchTags(w http.ResponseWriter, r *http.Request) {
	timeRange := parseTimeRange(r)

	resp := tempoV2TagNamesResponse{
		Scopes:  make([]tempoV2Scope, 0, 3),
		Metrics: emptyMetrics,
	}

	// Intrinsic scope is always static.
	resp.Scopes = append(resp.Scopes, tempoV2Scope{
		Name: TempoScopeIntrinsic,
		Tags: tempoIntrinsicTags,
	})

	// Resource scope: try backend tag discovery first, fall back to common keys.
	resourceKeys := h.fetchTempoTagKeys(r.Context(), timeRange, TempoScopeResource)
	if len(resourceKeys) == 0 {
		resourceKeys = TempoCommonResourceAttributeKeys
	}
	resp.Scopes = append(resp.Scopes, tempoV2Scope{
		Name: TempoScopeResource,
		Tags: resourceKeys,
	})

	// Span scope: try backend tag discovery first, fall back to common keys.
	spanKeys := h.fetchTempoTagKeys(r.Context(), timeRange, TempoScopeSpan)
	if len(spanKeys) == 0 {
		spanKeys = TempoCommonSpanAttributeKeys
	}
	resp.Scopes = append(resp.Scopes, tempoV2Scope{
		Name: TempoScopeSpan,
		Tags: spanKeys,
	})

	h.writeJSON(w, http.StatusOK, resp)
}

// fetchTempoTagKeys queries the backend for attribute keys in the given scope.
// Returns nil on error (caller will use fallback keys).
func (h *tempoHandlers) fetchTempoTagKeys(ctx context.Context, timeRange observabilitystorageext.TimeRange, scope string) []string {
	if h.traceReader == nil {
		return nil
	}
	keys, err := h.traceReader.GetTagKeys(ctx, timeRange, scope)
	if err != nil {
		h.logger.Debug("tempo v2 tags: could not fetch tag keys from backend",
			zap.String("scope", scope), zap.Error(err))
		return nil
	}
	return keys
}

// ── Handler: /api/v2/search/tag/{tagName}/values ────

// parseScopedTagName splits a Grafana V2 tag name in "{scope}.{key}" format.
// Grafana sends tag names like "resource.service.name" or "span.http.method"
// where the first dot-separated segment is the scope. If no known scope prefix
// is found, returns scope="" and the original tagName as the key.
func parseScopedTagName(tagName string) (scope, key string) {
	for _, prefix := range []string{"resource.", "span."} {
		if strings.HasPrefix(tagName, prefix) {
			return strings.TrimSuffix(prefix, "."), tagName[len(prefix):]
		}
	}
	// No known scope prefix — treat the entire name as the key.
	return "", tagName
}

// handleTempoV2SearchTagValues handles GET /api/v2/search/tag/{tagName}/values.
// Returns values with type annotations (string, int, float, keyword).
// Supports optional `q` parameter to filter the scope (e.g. q={resource.service.name="my-svc"}).
func (h *tempoHandlers) handleTempoV2SearchTagValues(w http.ResponseWriter, r *http.Request) {
	tagName := chi.URLParam(r, "tagName")
	if tagName == "" {
		h.writeError(w, http.StatusBadRequest, "tagName parameter is required")
		return
	}

	// Parse scope prefix from Grafana V2 format: "resource.service.name" → scope="resource", key="service.name"
	scope, tagKey := parseScopedTagName(tagName)

	// Parse optional q parameter for filtering (e.g. {resource.service.name="tapm-api"}).
	filterTags := parseTagValuesFilter(r, h.logger)

	// First try V1-style resolution (fast path: services, static lists).
	// Only use fast path if no filter is specified (filters require backend query).
	if len(filterTags) == 0 {
		values, err := h.resolveTagValues(r, tagKey)
		if err != nil {
			h.logger.Error("tempo v2 tag values failed", zap.String("tag", tagName), zap.Error(err))
			h.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// If V1 resolution returned values, wrap them with type info.
		if len(values) > 0 {
			tv := make([]tempoV2TagValue, len(values))
			for i, v := range values {
				tv[i] = tempoV2TagValue{Type: "string", Value: v}
			}
			h.writeJSON(w, http.StatusOK, tempoV2TagValuesResponse{
				TagValues: tv,
				Metrics:   emptyMetrics,
			})
			return
		}
	}

	// Handle intrinsic tags with filter support.
	// Intrinsic fields (e.g. "name") are stored at top-level ES fields, not under attributes/resource.
	// The generic fetchTempoTagValues would incorrectly query "attributes.name" which doesn't exist.
	if tv := h.resolveIntrinsicTagValuesWithFilter(r, tagKey, filterTags); tv != nil {
		h.writeJSON(w, http.StatusOK, tempoV2TagValuesResponse{
			TagValues: tv,
			Metrics:   emptyMetrics,
		})
		return
	}

	// Try backend tag value discovery for span/resource attributes.
	tv := h.fetchTempoTagValues(r, tagKey, scope, filterTags)
	h.writeJSON(w, http.StatusOK, tempoV2TagValuesResponse{
		TagValues: tv,
		Metrics:   emptyMetrics,
	})
}

// resolveIntrinsicTagValuesWithFilter handles intrinsic tag value queries that include filter conditions.
// Intrinsic fields like "name" (span name / operation name) are stored at top-level ES fields,
// NOT under "attributes." or "resource." prefixes. The generic GetTagValues would query
// "attributes.name" which doesn't exist, returning empty results.
//
// This function recognizes intrinsic tags and routes them to the correct query logic:
//   - "name": queries operations filtered by service name from filterTags
//   - "kind", "status": returns static value lists (filter doesn't affect possible values)
//
// Returns nil if the tagKey is not an intrinsic field (caller should proceed with generic path).
func (h *tempoHandlers) resolveIntrinsicTagValuesWithFilter(r *http.Request, tagKey string, filterTags map[string]string) []tempoV2TagValue {
	switch tagKey {
	case TempoIntrinsicName:
		// "name" is the span name / operation name, stored in ES top-level field "name".
		// If filter specifies service.name, fetch operations only for that service.
		// Otherwise fetch operations across all services.
		timeRange := parseTempoTagValuesTimeRange(r)
		var values []string
		var err error

		if svcName, ok := filterTags["service.name"]; ok && svcName != "" {
			// Fetch operations for the specific service.
			ops, opErr := h.traceReader.GetOperations(r.Context(), svcName, timeRange)
			if opErr != nil {
				h.logger.Debug("tempo v2 tag values: get operations for service failed",
					zap.String("service", svcName), zap.Error(opErr))
				return nil
			}
			values = make([]string, len(ops))
			for i, op := range ops {
				values[i] = op.Name
			}
		} else {
			// No service filter — fetch operations across all services.
			values, err = h.fetchAllOperations(r.Context(), timeRange)
			if err != nil {
				h.logger.Debug("tempo v2 tag values: fetch all operations failed", zap.Error(err))
				return nil
			}
		}

		if len(values) == 0 {
			return nil
		}
		tv := make([]tempoV2TagValue, len(values))
		for i, v := range values {
			tv[i] = tempoV2TagValue{Type: "string", Value: v}
		}
		return tv

	case TempoIntrinsicKind, OTelAttrSpanKind:
		tv := make([]tempoV2TagValue, len(tempoSpanKindValues))
		for i, v := range tempoSpanKindValues {
			tv[i] = tempoV2TagValue{Type: "string", Value: v}
		}
		return tv

	case TempoIntrinsicStatus, "status.code":
		tv := make([]tempoV2TagValue, len(tempoStatusCodeValues))
		for i, v := range tempoStatusCodeValues {
			tv[i] = tempoV2TagValue{Type: "string", Value: v}
		}
		return tv

	// ── Intrinsic tag: rootName / rootServiceName ──
	// Derived from root spans: queries the root spans (parentSpanId absent)
	// for distinct name / serviceName values via terms aggregation.
	case TempoIntrinsicRootName:
		return h.fetchRootSpanTagValues(r, func(ctx context.Context, timeRange observabilitystorageext.TimeRange) ([]string, error) {
			return h.traceReader.ListRootSpanNames(ctx, timeRange, "")
		})
	case TempoIntrinsicRootServiceName:
		return h.fetchRootSpanTagValues(r, func(ctx context.Context, timeRange observabilitystorageext.TimeRange) ([]string, error) {
			return h.traceReader.ListRootSpanServices(ctx, timeRange, "")
		})

	// ── Intrinsic tag: statusMessage ──
	// status.message is a "text" field (no .keyword sub-field), so terms
	// aggregation is not possible. Return nil — Grafana will show the tag
	// as available but without value suggestions.
	case TempoIntrinsicStatusMessage:
		return nil

	default:
		// Not an intrinsic tag — let the caller handle via generic path.
		return nil
	}
}

// fetchTempoTagValues queries the backend for values of a specific tag.
// If scope is specified (non-empty), queries only that scope; otherwise tries
// span first, then resource, returning the first non-empty result.
// filterTags narrows the aggregation scope (e.g. only spans from a specific service).
func (h *tempoHandlers) fetchTempoTagValues(r *http.Request, tagKey string, scope string, filterTags map[string]string) []tempoV2TagValue {
	if h.traceReader == nil {
		return nil
	}

	timeRange := parseTempoTagValuesTimeRange(r)

	// If scope is known, query only that scope; otherwise try both.
	scopes := []string{"span", "resource"}
	if scope != "" {
		scopes = []string{scope}
	}

	for _, s := range scopes {
		values, err := h.traceReader.GetTagValues(r.Context(), tagKey, timeRange, s, filterTags)
		if err != nil {
			h.logger.Debug("tempo v2 tag values: backend lookup failed",
				zap.String("tag", tagKey), zap.String("scope", s), zap.Error(err))
			continue
		}
		if len(values) > 0 {
			tv := make([]tempoV2TagValue, len(values))
			for i, v := range values {
				tv[i] = tempoV2TagValue{Type: "string", Value: v}
			}
			return tv
		}
	}

	return nil
}

// planToTagFilter collects all "=" (equality) conditions from an ExecutionPlan
// into a flat tag filter map: key → value. Intrinsics (service.name/name/kind/
// status) are projected to their unscoped Tempo tag names (so
// resolveIntrinsicTagValuesWithFilter, which reads filterTags["service.name"],
// keeps working); custom attributes keep their plan key (scoped, e.g.
// "span.http.method" — more precise than the legacy unscoped form, and the
// storage reader resolves either form).
//
// Non-"=" conditions (!=, =~, != nil) are intentionally excluded — the
// tag-values filter only supports equality narrowing, matching the legacy
// parser's behavior.
//
// Pure function: plan → map. Returns nil when there is nothing to filter on.
func planToTagFilter(plan *traceql.ExecutionPlan) map[string]string {
	if plan == nil {
		return nil
	}
	var m map[string]string
	add := func(k, v string) {
		if v == "" {
			return
		}
		if m == nil {
			m = make(map[string]string)
		}
		m[k] = v
	}
	add("service.name", plan.ServiceName)
	add("name", plan.OperationName)
	add("kind", plan.SpanKind)
	add("status", plan.Status)
	for k, v := range plan.Tags {
		add(k, v)
	}
	return m
}

// parseTagValuesFilter parses the optional `q` parameter in tag values requests.
// Grafana sends: q={resource.service.name="tapm-api"} to filter tag values by service.
// Returns the "=" AND conditions as a map (OR/regex/exists conditions are not
// supported for tag-value filtering). Uses the unified AST parser; a parse
// failure is logged and yields no filter (nil).
func parseTagValuesFilter(r *http.Request, logger *zap.Logger) map[string]string {
	rawQ := r.FormValue("q")
	if rawQ == "" {
		return nil
	}
	ast, err := traceql.Parse(normalizeTraceQLMetricsQuery(rawQ))
	if err != nil {
		if logger != nil {
			logger.Warn("tempo tag-values: failed to parse q filter, ignoring",
				zap.String("q", rawQ), zap.Error(err))
		}
		return nil
	}
	return planToTagFilter(traceql.Plan(ast))
}

// fetchRootSpanTagValues is a helper that calls the given fetcher with the request's
// time range and converts the results to tempoV2TagValue slice. Used by
// resolveIntrinsicTagValuesWithFilter for rootName and rootServiceName.
func (h *tempoHandlers) fetchRootSpanTagValues(
	r *http.Request,
	fetchFn func(ctx context.Context, timeRange observabilitystorageext.TimeRange) ([]string, error),
) []tempoV2TagValue {
	timeRange := parseTempoTagValuesTimeRange(r)
	values, err := fetchFn(r.Context(), timeRange)
	if err != nil {
		h.logger.Debug("tempo v2 tag values: root span tag values query failed", zap.Error(err))
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	tv := make([]tempoV2TagValue, len(values))
	for i, v := range values {
		tv[i] = tempoV2TagValue{Type: "string", Value: v}
	}
	return tv
}

// ── TraceQL Metrics Search Path ─────────────────────

// executeTempoMetricsQuery runs a TraceQL metrics query (rate/quantile_over_time/etc.)
// using ES aggregations and returns time-series data.
func (h *tempoHandlers) executeTempoMetricsQuery(w http.ResponseWriter, r *http.Request, plan *traceql.ExecutionPlan, query observabilitystorageext.TraceQuery) {
	ms := plan.MetricsStage
	if ms == nil {
		h.writeError(w, http.StatusBadRequest, "missing metrics stage in execution plan")
		return
	}

	timeRange := parseTempoTimeRange(r)
	step := parseTempoMetricsStep(r, timeRange)

	metricsQuery := observabilitystorageext.TraceMetricsQuery{
		AppID:         query.AppID,
		ServiceName:   plan.ServiceName,
		OperationName: plan.OperationName,
		Tags:          plan.Tags,
		TagsOr:        plan.TagsOr,
		TagsNot:       plan.TagsNot,
		TagsExists:    plan.TagsExists,
		TagsRegex:     plan.TagsRegex,
		SpanKind:      plan.SpanKind,
		Status:        plan.Status,
		IsRoot:        plan.IsRoot,
		RootName:      plan.RootName,
		RootService:   plan.RootService,
		MinDuration:   plan.MinDuration,
		MaxDuration:   plan.MaxDuration,
		TimeRange:     timeRange,
		Step:          step,
		Function:      string(ms.Function),
		Field:         ms.Field,
		Percentiles:   ms.Percentiles,
		ByLabels:      ms.ByLabels,
		Sample:        ms.Sample,
	}

	result, err := h.traceReader.QueryTraceMetrics(r.Context(), metricsQuery)
	if err != nil {
		h.logger.Error("tempo metrics query failed", zap.String("function", string(ms.Function)), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, result)
}

// parseTempoMetricsStep extracts the step interval from the request.
func parseTempoMetricsStep(r *http.Request, timeRange observabilitystorageext.TimeRange) time.Duration {
	stepStr := r.FormValue("step")
	if stepStr != "" {
		if d, err := parseTempoDuration(stepStr); err == nil && d > 0 {
			return d
		}
	}
	// Auto-compute: ~60 buckets across the time range.
	defaultStep := timeRange.End.Sub(timeRange.Start) / 60
	if defaultStep < 15*time.Second {
		defaultStep = 15 * time.Second
	}
	return defaultStep
}

// ── Handler: /api/metrics/query_range (TraceQL Metrics) ──

// handleTempoMetricsQueryRange handles GET /api/metrics/query_range.
// TraceQL is parsed once via the unified AST parser; the resulting MetricsStage
// is dispatched to the TraceReader (real-time aggregation from raw spans) when
// available, otherwise to the MetricReader (pre-aggregated spanmetrics).
func (h *tempoHandlers) handleTempoMetricsQueryRange(w http.ResponseWriter, r *http.Request) {
	rawQ := r.FormValue("q")
	if rawQ == "" {
		h.writeError(w, http.StatusBadRequest, "parameter 'q' is required")
		return
	}

	// Parse time range and step.
	timeRange := parseTempoTimeRange(r)
	step := parseTempoMetricsStep(r, timeRange)

	ast, err := traceql.Parse(normalizeTraceQLMetricsQuery(rawQ))
	if err != nil {
		h.logger.Warn("tempo metrics: invalid query", zap.String("q", rawQ), zap.Error(err))
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid TraceQL metrics query: %v", err))
		return
	}
	plan := traceql.Plan(ast)
	if plan == nil || plan.MetricsStage == nil {
		h.writeError(w, http.StatusBadRequest,
			"not a TraceQL metrics query: missing rate()/count_over_time()/quantile_over_time()/histogram_over_time()")
		return
	}

	// Prefer real-time aggregation from raw spans (TraceReader); fall back to
	// pre-aggregated spanmetrics (MetricReader) when only that is available.
	if h.traceReader != nil {
		h.executeTempoMetricsQueryRange(w, r, plan, timeRange, step)
		return
	}
	if h.metricReader != nil {
		h.executeTempoMetricsViaMetricReader(w, r, plan, timeRange, step)
		return
	}
	h.writeError(w, http.StatusServiceUnavailable, "no metrics reader available")
}

// metricsFuncParam returns the numeric parameter of a metrics function (the
// quantile for quantile_over_time); 0 for functions without a parameter.
func metricsFuncParam(ms *traceql.MetricsStage) float64 {
	if len(ms.Percentiles) > 0 {
		return ms.Percentiles[0]
	}
	return 0
}

// executeTempoMetricsViaMetricReader runs a TraceQL metrics query against
// pre-aggregated spanmetrics via the MetricReader and writes the response in
// Tempo /api/metrics/query_range format. planToTagFilter supplies the equality
// filter labels (intrinsics + custom attrs) the same way the legacy parser did.
func (h *tempoHandlers) executeTempoMetricsViaMetricReader(w http.ResponseWriter, r *http.Request, plan *traceql.ExecutionPlan, timeRange observabilitystorageext.TimeRange, step time.Duration) {
	ms := plan.MetricsStage
	funcStr := string(ms.Function)
	param := metricsFuncParam(ms)

	// Build MetricRangeQuery from the plan.
	labels := translateTraceQLLabels(planToTagFilter(plan))
	metricName := translateTraceQLMetric(funcStr)
	aggregation := selectMetricsAggregation(funcStr, param)

	query := observabilitystorageext.MetricRangeQuery{
		MetricName:  metricName,
		Labels:      labels,
		TimeRange:   timeRange,
		Step:        step,
		Aggregation: aggregation,
		GroupBy:     ms.ByLabels,
		Fill:        "null",
	}

	h.logger.Debug("tempo metrics: executing via MetricReader",
		zap.String("metric", metricName),
		zap.String("function", funcStr),
		zap.Float64("param", param),
		zap.String("aggregation", aggregation),
		zap.Strings("groupBy", query.GroupBy),
		zap.Any("labels", labels),
	)

	result, err := h.metricReader.QueryRange(r.Context(), query)
	if err != nil {
		h.logger.Error("tempo metrics: query failed", zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Normalize duration units to seconds (Tempo protocol standard).
	// MetricReader returns duration values in milliseconds; unitconv handles the conversion.
	// For non-duration functions (rate, count_over_time), sourceUnit is DurationUnitNone → no-op.
	sourceUnit := unitconv.SourceUnitForMetricReader(funcStr, "duration")
	if sourceUnit != unitconv.DurationUnitNone {
		for i := range result.Data {
			for j := range result.Data[i].Values {
				result.Data[i].Values[j].Value = unitconv.ToSeconds(result.Data[i].Values[j].Value, sourceUnit)
			}
		}
	}

	// Convert to Tempo metrics format.
	resp := convertMetricRangeToTempoMetrics(result)
	h.writeJSON(w, http.StatusOK, resp)
}

// executeTempoMetricsQueryRange runs a TraceQL metrics query via TraceReader
// and writes the response in Tempo /api/metrics/query_range format.
func (h *tempoHandlers) executeTempoMetricsQueryRange(w http.ResponseWriter, r *http.Request, plan *traceql.ExecutionPlan, timeRange observabilitystorageext.TimeRange, step time.Duration) {
	ms := plan.MetricsStage

	metricsQuery := observabilitystorageext.TraceMetricsQuery{
		ServiceName:   plan.ServiceName,
		OperationName: plan.OperationName,
		Tags:          plan.Tags,
		TagsOr:        plan.TagsOr,
		TagsNot:       plan.TagsNot,
		TagsExists:    plan.TagsExists,
		TagsRegex:     plan.TagsRegex,
		SpanKind:      plan.SpanKind,
		Status:        plan.Status,
		IsRoot:        plan.IsRoot,
		RootName:      plan.RootName,
		RootService:   plan.RootService,
		MinDuration:   plan.MinDuration,
		MaxDuration:   plan.MaxDuration,
		TimeRange:     timeRange,
		Step:          step,
		Function:      string(ms.Function),
		Field:         ms.Field,
		Percentiles:   ms.Percentiles,
		ByLabels:      ms.ByLabels,
		Sample:        ms.Sample,
	}

	result, err := h.traceReader.QueryTraceMetrics(r.Context(), metricsQuery)
	if err != nil {
		h.logger.Error("tempo metrics query_range failed",
			zap.String("function", string(ms.Function)), zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert TraceMetricsResult to Tempo metrics response format.
	resp := convertTraceMetricsToTempoResponse(result)
	h.writeJSON(w, http.StatusOK, resp)
}

// convertTraceMetricsToTempoResponse converts a TraceMetricsResult (from TraceReader
// real-time aggregation) to the Tempo /api/metrics/query_range response format.
func convertTraceMetricsToTempoResponse(result *observabilitystorageext.TraceMetricsResult) tempoMetricsResponse {
	series := make([]tempoMetricSeries, 0, len(result.Series))
	for _, s := range result.Series {
		labels := make([]tempoMetricLabel, 0, len(s.Labels))
		for k, v := range s.Labels {
			labels = append(labels, tempoMetricLabel{Key: k, Value: stringToTempoAnyValue(v)})
		}

		samples := make([]tempoMetricSample, 0, len(s.Values))
		for _, p := range s.Values {
			samples = append(samples, tempoMetricSample{
				TimestampMs: p.TimestampMs,
				Value:       p.Value,
			})
		}

		if len(samples) > 0 {
			series = append(series, tempoMetricSeries{
				Labels:  labels,
				Samples: samples,
			})
		}
	}

	return tempoMetricsResponse{
		Series: series,
		Metrics: tempoSearchMetrics{
			InspectedBytes: "0",
		},
	}
}

// ── TraceQL → Metric Translation ─────────────────────

// translateTraceQLLabels maps TraceQL filter keys (e.g. "status") to
// spanmetrics label keys (e.g. "status_code"). Strips scope prefixes.
func translateTraceQLLabels(filterTags map[string]string) map[string]string {
	if len(filterTags) == 0 {
		return nil
	}
	out := make(map[string]string, len(filterTags))
	for k, v := range filterTags {
		key := stripTraceQLScopePrefix(k)
		// Map TraceQL key names to spanmetrics label names.
		switch key {
		case "status":
			out["status_code"] = normalizeStatusValue(v)
		case "kind":
			out["span_kind"] = v
		default:
			out[key] = v
		}
	}
	return out
}

// translateTraceQLMetric maps a TraceQL function to the MetricGenerator metric name.
// Naming aligned with Tempo MetricGenerator convention (underscores).
func translateTraceQLMetric(fn string) string {
	switch fn {
	case "quantile_over_time":
		return "traces_spanmetrics_latency"
	case "histogram_over_time":
		return "traces_spanmetrics_latency"
	default:
		return "traces_spanmetrics_calls_total"
	}
}

// mapQuantileToAggregation converts a quantile value to an ES aggregation name.
// 0.50 → p50, 0.90 → p90, 0.95 → p95, 0.99 → p99, etc.
func mapQuantileToAggregation(q float64) string {
	// Common quantiles supported by our ES metric reader.
	switch {
	case q >= 0.99:
		return "p99"
	case q >= 0.95:
		return "p95"
	case q >= 0.90:
		return "p90"
	case q >= 0.50:
		return "p50"
	default:
		return "avg"
	}
}

// selectMetricsAggregation returns the appropriate aggregation for a TraceQL function.
func selectMetricsAggregation(fn string, param float64) string {
	switch fn {
	case "quantile_over_time":
		return mapQuantileToAggregation(param)
	case "histogram_over_time":
		return "avg" // histogram data is bucket-based
	case "rate", "count_over_time":
		return "sum"
	default:
		return "sum"
	}
}

// normalizeStatusValue maps TraceQL status value to spanmetrics format.
// TraceQL: "error" / "ok" / "unset" → spanmetrics: "STATUS_CODE_ERROR" / "STATUS_CODE_OK" / ""
func normalizeStatusValue(v string) string {
	switch strings.ToLower(v) {
	case "error":
		return "STATUS_CODE_ERROR"
	case "ok":
		return "STATUS_CODE_OK"
	default:
		return v
	}
}

// stripTraceQLScopePrefix removes "resource." or "span." scope prefixes.
func stripTraceQLScopePrefix(key string) string {
	for _, prefix := range []string{"resource.", "span.", "."} {
		if strings.HasPrefix(key, prefix) {
			return key[len(prefix):]
		}
	}
	return key
}

// convertMetricRangeToTempoMetrics converts a MetricRangeResult to Tempo /api/metrics format.
func convertMetricRangeToTempoMetrics(result *observabilitystorageext.MetricRangeResult) tempoMetricsResponse {
	series := make([]tempoMetricSeries, 0, len(result.Data))
	for _, s := range result.Data {
		labels := make([]tempoMetricLabel, 0, len(s.Labels))
		for k, v := range s.Labels {
			labels = append(labels, tempoMetricLabel{Key: k, Value: stringToTempoAnyValue(v)})
		}

		samples := make([]tempoMetricSample, 0, len(s.Values))
		for _, tv := range s.Values {
			ms, _ := strconv.ParseInt(tv.TimeUnixMilli, 10, 64)
			samples = append(samples, tempoMetricSample{
				TimestampMs: ms,
				Value:       tv.Value,
			})
		}

		if len(samples) > 0 {
			series = append(series, tempoMetricSeries{
				Labels:  labels,
				Samples: samples,
			})
		}
	}

	return tempoMetricsResponse{
		Series: series,
		Metrics: tempoSearchMetrics{
			InspectedBytes: "0",
		},
	}
}

// ── Utility: Duration Parser ──────────────────────────

// parseTempoDuration parses a duration string like "15s", "1m", "28s".
func parseTempoDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Try as seconds (integer).
	if sec, err := strconv.Atoi(s); err == nil {
		return time.Duration(sec) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration: %s", s)
}

// ── Conversion Helpers ─────────────────────────────

// spanKindToInt maps the internal SpanKind string to the Tempo integer encoding.
func spanKindToInt(kind observabilitystorageext.SpanKind) int {
	switch kind {
	case observabilitystorageext.SpanKindInternal:
		return 1
	case observabilitystorageext.SpanKindServer:
		return 2
	case observabilitystorageext.SpanKindClient:
		return 3
	case observabilitystorageext.SpanKindProducer:
		return 4
	case observabilitystorageext.SpanKindConsumer:
		return 5
	default:
		return 0 // unspecified, omitted by omitempty
	}
}

// statusCodeToInt maps the internal StatusCode string to the integer encoding.
func statusCodeToInt(code observabilitystorageext.StatusCode) int {
	switch code {
	case observabilitystorageext.StatusCodeOk:
		return 1
	case observabilitystorageext.StatusCodeError:
		return 2
	default:
		return 0 // unset, omitted by omitempty
	}
}

// publicKeyValuesToTempo converts public KeyValue list to Tempo format.
func publicKeyValuesToTempo(kvs []observabilitystorageext.KeyValue) []tempoKeyValue {
	if len(kvs) == 0 {
		return nil
	}
	result := make([]tempoKeyValue, 0, len(kvs))
	for _, kv := range kvs {
		tkv := tempoKeyValue{
			Key:   kv.Key,
			Value: publicAnyValueToTempo(kv.Value),
		}
		// Skip zero-value entries (matching Tempo behavior where empty values are not serialized).
		if isEmptyTempoValue(tkv.Value) {
			continue
		}
		result = append(result, tkv)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// publicAnyValueToTempo converts a public AnyValue to Tempo format.
// Note: intValue is converted to string (proto jsonpb compatibility).
// Also populates the Value.* fallback field for Grafana traces-drilldown compatibility.
func publicAnyValueToTempo(v observabilitystorageext.AnyValue) tempoAnyValue {
	tv := tempoAnyValue{}

	switch {
	case v.StringValue != nil:
		s := *v.StringValue
		tv.StringValue = &s
		tv.Value = &tempoAnyValueAlt{StringValue: &s}
	case v.IntValue != nil:
		s := strconv.FormatInt(*v.IntValue, 10)
		tv.IntValue = &s
		tv.Value = &tempoAnyValueAlt{IntValue: &s}
	case v.DoubleValue != nil:
		d := *v.DoubleValue
		tv.DoubleValue = &d
		tv.Value = &tempoAnyValueAlt{DoubleValue: &d}
	case v.BoolValue != nil:
		b := *v.BoolValue
		tv.BoolValue = &b
		tv.Value = &tempoAnyValueAlt{BoolValue: &b}
	}
	return tv
}

// isEmptyTempoValue returns true if the value is a nil/zero value (all fields nil).
func isEmptyTempoValue(v tempoAnyValue) bool {
	return v.StringValue == nil && v.IntValue == nil && v.DoubleValue == nil && v.BoolValue == nil
}

// ═══════════════════════════════════════════════════
// Select Projection — | select(field1, field2, ...)
// ═══════════════════════════════════════════════════

// projectSpanWithSelect returns a filtered set of attributes for a span,
// including only the fields specified in selectFields plus system fields.
// If selectFields is empty, returns the original attributes (no projection).
//
// nsInfo (may be nil) provides pre-computed nested set model values for
// intrinsic fields like nestedSetParent/Left/Right.
//
// Supported select fields:
//   - name              → span operation name
//   - kind               → span kind (client/server/internal/...)
//   - status             → span status (ok/error/unset)
//   - status.code        → span status code
//   - status.message     → span status message
//   - duration           → span duration in nanoseconds
//   - resource.service.name → service name
//   - resource.X         → resource attribute X
//   - .X / span.X        → span attribute X
//   - plain X            → check both span attributes and resource attributes
//   - nestedSetParent / nestedSetLeft / nestedSetRight → nested set model
// projectSpanWithSelectResult holds the result of span projection, separating
// the top-level span name from attributes for Grafana compatibility.
type projectSpanWithSelectResult struct {
	Name       string          // top-level span name (extracted from "name" select field)
	Attributes []tempoKeyValue // remaining projected attributes
}

func projectSpanWithSelect(span observabilitystorageext.Span, selectFields []string, nsInfo map[string]nestedSetInfo) projectSpanWithSelectResult {
	if len(selectFields) == 0 {
		return projectSpanWithSelectResult{
			Name:       span.Name,
			Attributes: publicKeyValuesToTempo(span.Attributes),
		}
	}

	result := make([]tempoKeyValue, 0, len(selectFields))
	spanName := ""
	for _, field := range selectFields {
		// "name" is promoted to a top-level span field for Grafana compatibility.
		// Grafana reads span.name directly, not from attributes.
		if field == "name" {
			spanName = span.Name
			continue
		}
		val := resolveSelectField(span, field, nsInfo)
		if val == nil {
			continue
		}
		// Grafana expects attribute keys without scope prefixes.
		// e.g., "resource.service.name" → "service.name"
		key := tempoAttributeKey(field)
		result = append(result, tempoKeyValue{Key: key, Value: *val})
	}
	return projectSpanWithSelectResult{Name: spanName, Attributes: result}
}

// tempoAttributeKey converts a scoped field name to the key format Grafana expects.
// Grafana's traces-drilldown plugin reads attributes with keys like "service.name",
// not "resource.service.name". Strip the scope prefix for compatibility.
func tempoAttributeKey(field string) string {
	if strings.HasPrefix(field, "resource.") {
		return field[len("resource."):]
	}
	if strings.HasPrefix(field, "span.") {
		return field[len("span."):]
	}
	return field
}

// resolveSelectField resolves a single select field to a tempoAnyValue.
// Returns nil if the field cannot be resolved.
func resolveSelectField(span observabilitystorageext.Span, field string, nsInfo map[string]nestedSetInfo) *tempoAnyValue {
	// Strip scope prefix (dot notation and colon notation).
	key := field
	scope := ""
	if strings.HasPrefix(field, "resource.") {
		scope = "resource"
		key = field[len("resource."):]
	} else if strings.HasPrefix(field, "span.") {
		scope = "span"
		key = field[len("span."):]
	} else if strings.HasPrefix(field, "event.") {
		scope = "event"
		key = field[len("event."):]
	} else if strings.HasPrefix(field, "trace.") {
		scope = "trace"
		key = field[len("trace."):]
	} else if strings.HasPrefix(field, ".") {
		scope = "span"
		key = field[1:]
	} else if idx := strings.Index(field, ":"); idx > 0 {
		// Colon prefix intrinsics: event:name, trace:duration, span:status
		scope = field[:idx]
		key = field[idx+1:]
	}

	// ── Event scope ──
	// Resolved before the intrinsic switch: event:name must read the event's
	// name, not the span's. The Exceptions tab selects
	// event.exception.{message,type,stacktrace}, which live on the span's events
	// rather than its attributes. Event-scoped keys resolve only here, so an
	// unscoped or span-scoped key never picks up an event value.
	if scope == TempoScopeEvent {
		if key == TempoIntrinsicName {
			for _, ev := range span.Events {
				if ev.Name != "" {
					return strVal(ev.Name)
				}
			}
			return nil
		}
		for _, ev := range span.Events {
			for _, attr := range ev.Attributes {
				if attr.Key == key {
					tv := publicAnyValueToTempo(attr.Value)
					return &tv
				}
			}
		}
		return nil
	}

	// ── System / intrinsic fields ──
	switch key {
	case TempoIntrinsicName:
		if span.Name != "" {
			return strVal(span.Name)
		}
	case TempoIntrinsicKind:
		return strVal(string(span.Kind))
	case TempoIntrinsicStatus:
		return strVal(string(span.Status.Code))
	case "status.code":
		return strVal(string(span.Status.Code))
	case "status.message":
		if span.Status.Message != "" {
			return strVal(span.Status.Message)
		}
	case TempoIntrinsicDuration:
		return strVal(span.DurationNano)
	case OTelAttrServiceName:
		if scope == TempoScopeResource || scope == "" {
			return strVal(span.ServiceName)
		}

	// ── Nested set model intrinsic fields ──
	case TempoIntrinsicNestedSetParent:
		if nsInfo != nil {
			if info, ok := nsInfo[span.SpanID]; ok {
				return intVal(info.Parent)
			}
		}
		if span.ParentSpanID == "" {
			return intVal(-1)
		}
		return intVal(1)

	case TempoIntrinsicNestedSetLeft:
		if nsInfo != nil {
			if info, ok := nsInfo[span.SpanID]; ok {
				return intVal(info.Left)
			}
		}
		return intVal(1)

	case TempoIntrinsicNestedSetRight:
		if nsInfo != nil {
			if info, ok := nsInfo[span.SpanID]; ok {
				return intVal(info.Right)
			}
		}
		return intVal(2)
	}

	// ── Resource attributes ──
	if scope == TempoScopeResource || scope == "" {
		for _, attr := range span.Resource {
			if attr.Key == key {
				tv := publicAnyValueToTempo(attr.Value)
				return &tv
			}
		}
	}

	// ── Span attributes ──
	if scope == TempoScopeSpan || scope == "" {
		for _, attr := range span.Attributes {
			if attr.Key == key {
				tv := publicAnyValueToTempo(attr.Value)
				return &tv
			}
		}
	}

	return nil
}

// strVal creates a tempoAnyValue holding a string.
func strVal(s string) *tempoAnyValue {
	return &tempoAnyValue{StringValue: &s, Value: &tempoAnyValueAlt{StringValue: &s}}
}

// intVal creates a tempoAnyValue holding an integer (as string per proto jsonpb convention).
func intVal(n int) *tempoAnyValue {
	s := strconv.Itoa(n)
	return &tempoAnyValue{IntValue: &s, Value: &tempoAnyValueAlt{IntValue: &s}}
}

// publicEventsToTempo converts public SpanEvent list to Tempo format.
func publicEventsToTempo(events []observabilitystorageext.SpanEvent) []tempoSpanEvent {
	if len(events) == 0 {
		return nil
	}
	result := make([]tempoSpanEvent, len(events))
	for i, e := range events {
		result[i] = tempoSpanEvent{
			TimeUnixNano: e.TimeUnixNano,
			Name:         e.Name,
			Attributes:   publicKeyValuesToTempo(e.Attributes),
		}
	}
	return result
}

// publicLinksToTempo converts public SpanLink list to Tempo format.
func publicLinksToTempo(links []observabilitystorageext.SpanLink) []tempoSpanLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]tempoSpanLink, len(links))
	for i, l := range links {
		result[i] = tempoSpanLink{
			TraceID:    l.TraceID,
			SpanID:     l.SpanID,
			Attributes: publicKeyValuesToTempo(l.Attributes),
		}
	}
	return result
}

// mapToTempoKeyValues converts a map[string]any to tempoKeyValue list.
// Used for resource attributes extracted from the span's Resource field.
func mapToTempoKeyValues(m map[string]any) []tempoKeyValue {
	if len(m) == 0 {
		return nil
	}
	result := make([]tempoKeyValue, 0, len(m))
	for k, v := range m {
		tkv := tempoKeyValue{Key: k, Value: anyToTempoValue(v)}
		if isEmptyTempoValue(tkv.Value) {
			continue
		}
		result = append(result, tkv)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// stringToTempoAnyValue wraps a string into a typed Tempo AnyValue.
// Used for metrics series labels where values are always strings.
//
// Unlike span attributes (see anyToTempoValue) this deliberately omits the
// snake_case Value fallback: traces-drilldown only reads Value.string_value on
// the trace-merge path (Structure tab), never for metrics labels, so emitting it
// here just doubled the size of every label in a by() query.
func stringToTempoAnyValue(s string) tempoAnyValue {
	return tempoAnyValue{StringValue: &s}
}

// anyToTempoValue converts interface{} to Tempo AnyValue.
func anyToTempoValue(v any) tempoAnyValue {
	if v == nil {
		return tempoAnyValue{}
	}
	switch val := v.(type) {
	case string:
		s := val
		return tempoAnyValue{StringValue: &s, Value: &tempoAnyValueAlt{StringValue: &s}}
	case int:
		s := strconv.FormatInt(int64(val), 10)
		return tempoAnyValue{IntValue: &s, Value: &tempoAnyValueAlt{IntValue: &s}}
	case int64:
		s := strconv.FormatInt(val, 10)
		return tempoAnyValue{IntValue: &s, Value: &tempoAnyValueAlt{IntValue: &s}}
	case int32:
		s := strconv.FormatInt(int64(val), 10)
		return tempoAnyValue{IntValue: &s, Value: &tempoAnyValueAlt{IntValue: &s}}
	case float64:
		d := val
		return tempoAnyValue{DoubleValue: &d, Value: &tempoAnyValueAlt{DoubleValue: &d}}
	case float32:
		d := float64(val)
		return tempoAnyValue{DoubleValue: &d, Value: &tempoAnyValueAlt{DoubleValue: &d}}
	case bool:
		b := val
		return tempoAnyValue{BoolValue: &b, Value: &tempoAnyValueAlt{BoolValue: &b}}
	default:
		s := fmt.Sprintf("%v", val)
		return tempoAnyValue{StringValue: &s, Value: &tempoAnyValueAlt{StringValue: &s}}
	}
}

// ── Parameter Parsing ──────────────────────────────

// parseTempoSearchParams extracts Tempo search parameters from the request.
// Tempo uses tags=service.name%3Dmy-svc, start/end in Unix seconds.
// Returns the ExecutionPlan for structural queries (may be nil for simple queries).
//
// TraceQL is parsed via the unified AST parser (traceql.Parse + Plan). A parse
// error is returned (→ 400) — invalid TraceQL is rejected rather than silently
// salvaged. An empty selector ("{}") yields no filter (match-all).
// metricFnPrefixRe matches the standard Tempo TraceQL metrics syntax where a
// metric function wraps a selector with a range selector, e.g.
// `rate({...}[5m])` or `quantile_over_time({...}[5m], 0.95)`.
//
//	group 1: function name
//	group 2: selector ({...})
//	group 3: range selector contents (e.g. "5m")
//	group 4: extra function args (e.g. ", 0.95")
//	group 5: optional by(...) clause
var metricFnPrefixRe = regexp.MustCompile(`^(rate|count_over_time|quantile_over_time|histogram_over_time)\(\s*(\{[^}]*\})\[([^\]]*)\]\s*(,[^)]*)?\)(\s+by\s*\([^)]*\))?$`)

// normalizeTraceQLMetricsQuery rewrites the standard Tempo TraceQL metrics syntax
// (function-prefix + range selector) into the pipeline form this parser accepts:
//
//	rate({...}[5m])                   -> {...} | rate()
//	count_over_time({...}[5m])        -> {...} | count_over_time()
//	quantile_over_time({...}[5m],0.95) -> {...} | quantile_over_time(duration, 0.95)
//	histogram_over_time({...}[5m])    -> {...} | histogram_over_time(duration)
//
// Grafana's TraceQL metrics panel and service map emit the function-prefix form;
// our parser only tokenizes the pipeline form (the lexer has no '[' token). The
// range selector (e.g. [5m]) is redundant here because the step is derived from
// the query time range.
func normalizeTraceQLMetricsQuery(q string) string {
	m := metricFnPrefixRe.FindStringSubmatch(strings.TrimSpace(q))
	if m == nil {
		return q
	}
	selector := m[2]
	extraArgs := strings.TrimPrefix(m[4], ",") // e.g. " 0.95"
	by := m[5]

	var args string
	switch m[1] {
	case "quantile_over_time", "histogram_over_time":
		// These take a duration as the leading argument; the range selector maps
		// to the literal "duration" keyword the parser expects.
		if extraArgs != "" {
			args = "duration, " + strings.TrimSpace(extraArgs)
		} else {
			args = "duration"
		}
	default: // rate, count_over_time
		args = strings.TrimSpace(extraArgs)
	}
	return selector + " | " + m[1] + "(" + args + ")" + by
}

func parseTempoSearchParams(r *http.Request) (*traceql.ExecutionPlan, observabilitystorageext.TraceQuery, error) {
	q := r.URL.Query()
	query := observabilitystorageext.TraceQuery{
		TimeRange: parseTempoTimeRange(r),
	}
	var plan *traceql.ExecutionPlan

	// Parse tags: tags=service.name%3Dmy-svc (URL-encoded key=value) or tags=key:value
	if tagsStr := q.Get("tags"); tagsStr != "" {
		query.Tags = parseTempoTags(tagsStr)
	} else {
		query.Tags = make(map[string]string)
	}

	// Parse TraceQL q parameter. Takes priority over tags.
	// All TraceQL queries go through the unified AST parser + planner,
	// which correctly handles all operators (=, >, <, >=, <=, !=, =~)
	// and intrinsic fields (duration, kind, status, name, etc.).
	if traceQL := q.Get("q"); traceQL != "" {
		ast, err := traceql.Parse(normalizeTraceQLMetricsQuery(traceQL))
		if err != nil {
			return nil, query, fmt.Errorf("invalid TraceQL query: %w", err)
		}
		if ast != nil {
			plan = traceql.Plan(ast)
			// Apply extracted conditions to query.
			for k, v := range plan.Tags {
				query.Tags[k] = v
			}
			if len(plan.TagsOr) > 0 {
				query.TagsOr = plan.TagsOr
			}
			if len(plan.TagsNotOr) > 0 {
				query.TagsNotOr = plan.TagsNotOr
			}
			if len(plan.TagsRegexOr) > 0 {
				query.TagsRegexOr = plan.TagsRegexOr
			}
			if plan.ServiceName != "" {
				query.ServiceName = plan.ServiceName
			}
			if plan.OperationName != "" {
				query.OperationName = plan.OperationName
			}
			if plan.SpanKind != "" {
				query.SpanKind = plan.SpanKind
			}
			if plan.Status != "" {
				query.Status = plan.Status
			}
			if plan.IsRoot {
				query.IsRoot = true
			}
			if plan.RootName != "" {
				query.RootName = plan.RootName
			}
			if plan.RootService != "" {
				query.RootService = plan.RootService
			}
			if plan.MinDuration > 0 {
				query.MinDuration = plan.MinDuration
			}
			if plan.MaxDuration > 0 {
				query.MaxDuration = plan.MaxDuration
			}
			if len(plan.EventTags) > 0 {
				query.EventTags = []map[string]string{plan.EventTags}
			}
			if len(plan.EventTagsOr) > 0 {
				query.EventTagsOr = plan.EventTagsOr
			}
			if len(plan.TagsNot) > 0 {
				query.TagsNot = plan.TagsNot
			}
			if len(plan.TagsExists) > 0 {
				query.TagsExists = plan.TagsExists
			}
			if len(plan.TagsNotExists) > 0 {
				query.TagsNotExists = plan.TagsNotExists
			}
			if len(plan.TagsRegex) > 0 {
				query.TagsRegex = plan.TagsRegex
			}
		}
	}

	// Parse limit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query.Limit = n
		}
	}
	if query.Limit == 0 {
		query.Limit = 20
	}

	// Parse offset
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			query.Offset = n
		}
	}

	// Parse duration filters (Tempo format: 100ms, 1s, 500us)
	if v := q.Get("minDuration"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			query.MinDuration = d
		}
	}
	if v := q.Get("maxDuration"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			query.MaxDuration = d
		}
	}

	// Extract intrinsic fields from tags so they are not incorrectly queried
	// as attributes.* or resource.* in Elasticsearch.
	// In ES, these map to top-level fields (e.g., "name", "status.code", "kind").
	if svc, ok := query.Tags["service.name"]; ok && svc != "" {
		query.ServiceName = svc
		delete(query.Tags, "service.name")
	}
	if op, ok := query.Tags["name"]; ok && op != "" {
		query.OperationName = op
		delete(query.Tags, "name")
	}
	if kind, ok := query.Tags["kind"]; ok && kind != "" {
		query.SpanKind = kind
		delete(query.Tags, "kind")
	}
	if status, ok := query.Tags["status"]; ok && status != "" {
		query.Status = status
		delete(query.Tags, "status")
	}

	return plan, query, nil
}

// parseTempoTimeRange extracts start/end from Tempo search parameters.
// Tempo uses Unix epoch seconds (float).
func parseTempoTimeRange(r *http.Request) observabilitystorageext.TimeRange {
	now := time.Now()
	tr := observabilitystorageext.TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now,
	}

	q := r.URL.Query()
	if v := q.Get("start"); v != "" {
		if t, ok := parseTempoTimeParam(v); ok {
			tr.Start = t
		}
	}
	if v := q.Get("end"); v != "" {
		if t, ok := parseTempoTimeParam(v); ok {
			tr.End = t
		}
	}

	return tr
}

// parseTempoTimeParam parses a Tempo time parameter, which may be:
//   - Unix seconds (float, possibly fractional): "1720000000" / "1720000000.123"
//   - RFC3339: "2026-07-01T12:00:00Z" (Tempo accepts both; Grafana may emit either)
func parseTempoTimeParam(v string) (time.Time, bool) {
	if ft, err := strconv.ParseFloat(v, 64); err == nil {
		sec := int64(ft)
		nsec := int64((ft - float64(sec)) * 1e9)
		return time.Unix(sec, nsec), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseTempoTagValuesTimeRange returns the time range for tag value discovery.
// Uses a wider default window (7 days) than regular search, since tag values
// are used for autocomplete/discovery and should cover more historical data.
// If explicit start/end are provided, those are used instead.
func parseTempoTagValuesTimeRange(r *http.Request) observabilitystorageext.TimeRange {
	q := r.URL.Query()
	hasExplicitTime := q.Get("start") != "" || q.Get("end") != ""
	if hasExplicitTime {
		return parseTempoTimeRange(r)
	}
	// No explicit time params — use wider default (7 days).
	now := time.Now()
	return observabilitystorageext.TimeRange{
		Start: now.Add(-7 * 24 * time.Hour),
		End:   now,
	}
}

// parseTempoTags parses tags in Tempo format like "service.name=my-svc,tag2=val2".
// Also supports "key:value" format as fallback.
func parseTempoTags(s string) map[string]string {
	tags := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		var key, value string
		if idx := strings.Index(pair, "="); idx > 0 {
			key = pair[:idx]
			value = pair[idx+1:]
		} else if idx := strings.Index(pair, ":"); idx > 0 {
			key = pair[:idx]
			value = pair[idx+1:]
		} else {
			continue
		}
		tags[key] = value
	}
	return tags
}

// ── Utility Functions ──────────────────────────────

// parseNanos parses a Unix nano timestamp string, returning 0 on error.
func parseNanos(s string) int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return 0
}

// nanoToMs converts a nanosecond duration string to milliseconds.
func nanoToMs(s string) int64 {
	return parseNanos(s) / int64(time.Millisecond)
}

// ── TraceQL Parser ─────────────────────────────────
