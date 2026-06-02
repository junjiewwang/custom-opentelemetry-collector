// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// ============================================================================
// Observability V2 Handlers — Structured Responses
//
// These handlers use the new unified storage Reader interfaces from
// observabilitystorageext. They return structured JSON (not raw backend proxy).
//
// Activation: when config.Observability.StorageExtension is set.
// Fallback: when not set, the legacy proxy handlers in observability_handler.go are used.
// ============================================================================

// ============================================================================
// Trace Handlers (V2 — structured)
// ============================================================================

// handleSearchTracesV2 searches for traces and returns structured results.
// GET /api/v2/observability/traces?service=xxx&operation=xxx&tags=key:value&limit=20&start=xxx&end=xxx&minDuration=xxx&maxDuration=xxx
func (e *Extension) handleSearchTracesV2(w http.ResponseWriter, r *http.Request) {
	if e.storageTraceReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	query := parseTraceQuery(r)
	result, err := e.storageTraceReader.SearchTraces(r.Context(), query)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

// handleGetTraceV2 retrieves a single trace by its trace ID.
// GET /api/v2/observability/traces/{traceID}
func (e *Extension) handleGetTraceV2(w http.ResponseWriter, r *http.Request) {
	if e.storageTraceReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	traceID := chi.URLParam(r, "traceID")
	if traceID == "" {
		e.writeError(w, http.StatusBadRequest, "traceID parameter is required")
		return
	}

	trace, err := e.storageTraceReader.GetTrace(r.Context(), traceID)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if trace == nil {
		e.writeError(w, http.StatusNotFound, "trace not found")
		return
	}

	e.writeJSON(w, http.StatusOK, trace)
}

// handleGetTraceServicesV2 returns all available service names.
// GET /api/v2/observability/traces/services?start=xxx&end=xxx
func (e *Extension) handleGetTraceServicesV2(w http.ResponseWriter, r *http.Request) {
	if e.storageTraceReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	timeRange := parseTimeRange(r)
	services, err := e.storageTraceReader.GetServices(r.Context(), timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": services,
	})
}

// handleGetTraceOperationsV2 returns all operations for the specified service.
// GET /api/v2/observability/traces/services/{service}/operations?start=xxx&end=xxx
func (e *Extension) handleGetTraceOperationsV2(w http.ResponseWriter, r *http.Request) {
	if e.storageTraceReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	service := chi.URLParam(r, "service")
	if service == "" {
		e.writeError(w, http.StatusBadRequest, "service parameter is required")
		return
	}

	timeRange := parseTimeRange(r)
	operations, err := e.storageTraceReader.GetOperations(r.Context(), service, timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": operations,
	})
}

// handleGetDependenciesV2 returns service dependency links for the Service Map.
// GET /api/v2/observability/dependencies?endTs=xxx&lookback=xxx
func (e *Extension) handleGetDependenciesV2(w http.ResponseWriter, r *http.Request) {
	if e.storageTraceReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Trace reader not available")
		return
	}

	endTsStr := r.URL.Query().Get("endTs")
	lookbackStr := r.URL.Query().Get("lookback")

	endTsMs, err := strconv.ParseInt(endTsStr, 10, 64)
	if err != nil || endTsMs <= 0 {
		endTsMs = time.Now().UnixMilli()
	}

	lookbackMs, err := strconv.ParseInt(lookbackStr, 10, 64)
	if err != nil || lookbackMs <= 0 {
		lookbackMs = 24 * 60 * 60 * 1000 // 24 hours
	}

	endTs := time.UnixMilli(endTsMs)
	startTs := endTs.Add(-time.Duration(lookbackMs) * time.Millisecond)

	timeRange := observabilitystorageext.TimeRange{Start: startTs, End: endTs}
	deps, err := e.storageTraceReader.GetDependencies(r.Context(), timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": deps,
	})
}

// ============================================================================
// Metric Handlers (V2 — structured)
// ============================================================================

// handleMetricQueryV2 executes an instant metric query.
// GET /api/v2/observability/metrics/query?metric=xxx&service=xxx&time=xxx&labels=key:value,key:value
func (e *Extension) handleMetricQueryV2(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Metric reader not available")
		return
	}

	query := parseMetricQuery(r)
	result, err := e.storageMetricReader.Query(r.Context(), query)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

// handleMetricQueryRangeV2 executes a range metric query.
// GET /api/v2/observability/metrics/query_range?metric=xxx&service=xxx&start=xxx&end=xxx&step=xxx&labels=key:value
func (e *Extension) handleMetricQueryRangeV2(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Metric reader not available")
		return
	}

	query := parseMetricRangeQuery(r)
	result, err := e.storageMetricReader.QueryRange(r.Context(), query)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

// handleMetricNamesV2 returns all available metric names.
// GET /api/v2/observability/metrics/names?start=xxx&end=xxx
func (e *Extension) handleMetricNamesV2(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Metric reader not available")
		return
	}

	timeRange := parseTimeRange(r)
	names, err := e.storageMetricReader.ListMetricNames(r.Context(), timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": names,
	})
}

// handleMetricLabelsV2 returns all available label names.
// GET /api/v2/observability/metrics/labels?start=xxx&end=xxx
func (e *Extension) handleMetricLabelsV2(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Metric reader not available")
		return
	}

	timeRange := parseTimeRange(r)
	names, err := e.storageMetricReader.ListLabelNames(r.Context(), timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": names,
	})
}

// handleMetricLabelValuesV2 returns all values for the specified label name.
// GET /api/v2/observability/metrics/labels/{labelName}/values?start=xxx&end=xxx
func (e *Extension) handleMetricLabelValuesV2(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Metric reader not available")
		return
	}

	labelName := chi.URLParam(r, "labelName")
	if labelName == "" {
		e.writeError(w, http.StatusBadRequest, "labelName parameter is required")
		return
	}

	timeRange := parseTimeRange(r)
	values, err := e.storageMetricReader.ListLabelValues(r.Context(), labelName, timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": values,
	})
}

// ============================================================================
// Log Handlers (V2 — new, only available with storage extension)
// ============================================================================

// handleSearchLogs searches for logs matching query parameters.
// GET /api/v2/observability/logs?query=xxx&service=xxx&severity=ERROR,WARN&traceId=xxx&start=xxx&end=xxx&limit=50&offset=0
func (e *Extension) handleSearchLogs(w http.ResponseWriter, r *http.Request) {
	if e.storageLogReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Log reader not available")
		return
	}

	query := parseLogQuery(r)
	result, err := e.storageLogReader.SearchLogs(r.Context(), query)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

// handleGetLogContext retrieves surrounding log lines for context.
// GET /api/v2/observability/logs/{logID}/context?lines=10
func (e *Extension) handleGetLogContext(w http.ResponseWriter, r *http.Request) {
	if e.storageLogReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Log reader not available")
		return
	}

	logID := chi.URLParam(r, "logID")
	if logID == "" {
		e.writeError(w, http.StatusBadRequest, "logID parameter is required")
		return
	}

	lines := 10 // default
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}

	ctx, err := e.storageLogReader.GetLogContext(r.Context(), logID, lines)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, ctx)
}

// handleListLogFields returns available log fields for filtering.
// GET /api/v2/observability/logs/fields?start=xxx&end=xxx
func (e *Extension) handleListLogFields(w http.ResponseWriter, r *http.Request) {
	if e.storageLogReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Log reader not available")
		return
	}

	timeRange := parseTimeRange(r)
	fields, err := e.storageLogReader.ListLogFields(r.Context(), timeRange)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"data": fields,
	})
}

// handleGetLogStats returns log statistics (counts, severity distribution, etc.).
// GET /api/v2/observability/logs/stats?service=xxx&start=xxx&end=xxx&groupBy=severity
func (e *Extension) handleGetLogStats(w http.ResponseWriter, r *http.Request) {
	if e.storageLogReader == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Log reader not available")
		return
	}

	query := observabilitystorageext.LogStatsQuery{
		ServiceName: r.URL.Query().Get("service"),
		TimeRange:   parseTimeRange(r),
		GroupBy:     r.URL.Query().Get("groupBy"),
	}

	stats, err := e.storageLogReader.GetLogStats(r.Context(), query)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, stats)
}

// ============================================================================
// Storage Admin Handlers
// ============================================================================

// handleStorageStatus returns the current storage health and statistics.
// GET /api/v2/observability/admin/status
func (e *Extension) handleStorageStatus(w http.ResponseWriter, r *http.Request) {
	if e.storageAdmin == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage admin not available")
		return
	}

	status, err := e.storageAdmin.GetStatus(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, status)
}

// handleStorageRetention returns current retention policies.
// GET /api/v2/observability/admin/retention
func (e *Extension) handleStorageRetention(w http.ResponseWriter, r *http.Request) {
	if e.storageAdmin == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage admin not available")
		return
	}

	retention, err := e.storageAdmin.GetRetention(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, retention)
}

// handleSetStorageRetention updates the retention policy for a signal type.
// PUT /api/v2/observability/admin/retention/{signal}
func (e *Extension) handleSetStorageRetention(w http.ResponseWriter, r *http.Request) {
	if e.storageAdmin == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage admin not available")
		return
	}

	signalStr := chi.URLParam(r, "signal")
	signal := observabilitystorageext.SignalType(signalStr)
	switch signal {
	case observabilitystorageext.SignalTrace, observabilitystorageext.SignalMetric, observabilitystorageext.SignalLog:
	default:
		e.writeError(w, http.StatusBadRequest, "signal must be 'trace', 'metric', or 'log'")
		return
	}

	var req struct {
		Duration string `json:"duration"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		e.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	duration, err := time.ParseDuration(req.Duration)
	if err != nil {
		e.writeError(w, http.StatusBadRequest, "invalid duration format: "+err.Error())
		return
	}

	policy := observabilitystorageext.RetentionPolicy{Duration: duration}
	if err := e.storageAdmin.SetRetention(r.Context(), signal, policy); err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("retention policy updated"))
}

// handleStoragePurge triggers a data purge for the specified signal type.
// POST /api/v2/observability/admin/purge/{signal}?before=xxx
func (e *Extension) handleStoragePurge(w http.ResponseWriter, r *http.Request) {
	if e.storageAdmin == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage admin not available")
		return
	}

	signalStr := chi.URLParam(r, "signal")
	signal := observabilitystorageext.SignalType(signalStr)
	switch signal {
	case observabilitystorageext.SignalTrace, observabilitystorageext.SignalMetric, observabilitystorageext.SignalLog:
	default:
		e.writeError(w, http.StatusBadRequest, "signal must be 'trace', 'metric', or 'log'")
		return
	}

	beforeStr := r.URL.Query().Get("before")
	var before time.Time
	if beforeStr != "" {
		t, err := time.Parse(time.RFC3339, beforeStr)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid 'before' time format (use RFC3339)")
			return
		}
		before = t
	} else {
		e.writeError(w, http.StatusBadRequest, "'before' query parameter is required (RFC3339 format)")
		return
	}

	result, err := e.storageAdmin.Purge(r.Context(), signal, before)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

// handleStorageDiskUsage returns storage space usage information.
// GET /api/v2/observability/admin/disk-usage
func (e *Extension) handleStorageDiskUsage(w http.ResponseWriter, r *http.Request) {
	if e.storageAdmin == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage admin not available")
		return
	}

	usage, err := e.storageAdmin.GetDiskUsage(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, usage)
}

// handleStorageHealth returns the health status of the storage backend.
// GET /api/v2/observability/admin/health
func (e *Extension) handleStorageHealth(w http.ResponseWriter, r *http.Request) {
	if e.observabilityStorage == nil {
		e.writeError(w, http.StatusServiceUnavailable, "Storage extension not available")
		return
	}

	health, err := e.observabilityStorage.HealthCheck(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	if !health.Healthy {
		status = http.StatusServiceUnavailable
	}
	e.writeJSON(w, status, health)
}

// ============================================================================
// Query Parameter Parsers
// ============================================================================

// parseTimeRange extracts start/end time from query parameters.
// Supports both Unix milliseconds and RFC3339 format.
func parseTimeRange(r *http.Request) observabilitystorageext.TimeRange {
	now := time.Now()
	tr := observabilitystorageext.TimeRange{
		Start: now.Add(-1 * time.Hour), // default: 1 hour ago
		End:   now,
	}

	if v := r.URL.Query().Get("start"); v != "" {
		tr.Start = parseTimeParam(v, tr.Start)
	}
	if v := r.URL.Query().Get("end"); v != "" {
		tr.End = parseTimeParam(v, tr.End)
	}

	return tr
}

// parseTimeParam parses a time parameter (Unix milliseconds or RFC3339).
func parseTimeParam(s string, fallback time.Time) time.Time {
	// Try Unix milliseconds first
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	// Try Unix seconds (float)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec)
	}
	// Try RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return fallback
}

// parseTraceQuery extracts trace search parameters from request.
func parseTraceQuery(r *http.Request) observabilitystorageext.TraceQuery {
	q := r.URL.Query()
	query := observabilitystorageext.TraceQuery{
		AppID:         q.Get("app_id"),
		ServiceName:   q.Get("service"),
		OperationName: q.Get("operation"),
		TimeRange:     parseTimeRange(r),
	}

	// Parse tags (format: key:value,key:value or key=value)
	if tagsStr := q.Get("tags"); tagsStr != "" {
		query.Tags = parseTags(tagsStr)
	}

	// Parse limit/offset
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query.Limit = n
		}
	}
	if query.Limit == 0 {
		query.Limit = 20 // default
	}

	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			query.Offset = n
		}
	}

	// Parse duration filters (microseconds)
	if v := q.Get("minDuration"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			query.MinDuration = d
		} else if us, err := strconv.ParseInt(v, 10, 64); err == nil {
			query.MinDuration = time.Duration(us) * time.Microsecond
		}
	}
	if v := q.Get("maxDuration"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			query.MaxDuration = d
		} else if us, err := strconv.ParseInt(v, 10, 64); err == nil {
			query.MaxDuration = time.Duration(us) * time.Microsecond
		}
	}

	return query
}

// parseMetricQuery extracts instant metric query parameters.
func parseMetricQuery(r *http.Request) observabilitystorageext.MetricQuery {
	q := r.URL.Query()
	query := observabilitystorageext.MetricQuery{
		AppID:       q.Get("app_id"),
		MetricName:  q.Get("metric"),
		ServiceName: q.Get("service"),
	}

	// Parse time
	if v := q.Get("time"); v != "" {
		query.Time = parseTimeParam(v, time.Now())
	} else {
		query.Time = time.Now()
	}

	// Parse labels
	if v := q.Get("labels"); v != "" {
		query.Labels = parseTags(v)
	}

	return query
}

// parseMetricRangeQuery extracts range metric query parameters.
func parseMetricRangeQuery(r *http.Request) observabilitystorageext.MetricRangeQuery {
	q := r.URL.Query()
	query := observabilitystorageext.MetricRangeQuery{
		AppID:       q.Get("app_id"),
		MetricName:  q.Get("metric"),
		ServiceName: q.Get("service"),
		TimeRange:   parseTimeRange(r),
	}

	// Parse step
	if v := q.Get("step"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			query.Step = d
		} else if sec, err := strconv.ParseFloat(v, 64); err == nil {
			query.Step = time.Duration(sec * float64(time.Second))
		}
	}
	if query.Step == 0 {
		// Auto-calculate step based on time range
		duration := query.TimeRange.End.Sub(query.TimeRange.Start)
		query.Step = duration / 60 // ~60 data points
		if query.Step < time.Second {
			query.Step = time.Second
		}
	}

	// Parse labels
	if v := q.Get("labels"); v != "" {
		query.Labels = parseTags(v)
	}

	return query
}

// parseLogQuery extracts log search parameters from request.
func parseLogQuery(r *http.Request) observabilitystorageext.LogQuery {
	q := r.URL.Query()
	query := observabilitystorageext.LogQuery{
		AppID:       q.Get("app_id"),
		Query:       q.Get("query"),
		ServiceName: q.Get("service"),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		TimeRange:   parseTimeRange(r),
	}

	// Parse severity (comma-separated)
	if v := q.Get("severity"); v != "" {
		query.Severity = strings.Split(v, ",")
	}

	// Parse attributes
	if v := q.Get("attributes"); v != "" {
		query.Attributes = parseTags(v)
	}

	// Parse limit/offset
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query.Limit = n
		}
	}
	if query.Limit == 0 {
		query.Limit = 50 // default
	}

	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			query.Offset = n
		}
	}

	return query
}

// parseTags parses a tag string in format "key:value,key:value" or "key=value,key=value".
func parseTags(s string) map[string]string {
	tags := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		// Try key:value first, then key=value
		var key, value string
		if idx := strings.Index(pair, ":"); idx > 0 {
			key = pair[:idx]
			value = pair[idx+1:]
		} else if idx := strings.Index(pair, "="); idx > 0 {
			key = pair[:idx]
			value = pair[idx+1:]
		} else {
			continue
		}
		if key != "" {
			tags[key] = value
		}
	}
	return tags
}

// decodeJSONBody decodes JSON request body into the given struct.
func decodeJSONBody(r *http.Request, v any) error {
	if r.Body == nil {
		return fmt.Errorf("request body is empty")
	}
	return json.NewDecoder(r.Body).Decode(v)
}
