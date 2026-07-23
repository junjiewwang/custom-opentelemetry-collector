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

	"go.opentelemetry.io/collector/custom/extension/adminext/logql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════
// Loki HTTP API Handlers (MVP: query_range + labels + label/values)
// ═══════════════════════════════════════════════════

// requireLokiReader checks that the LogReader is available and writes an
// HTTP error if not. Returns false when unavailable.
func (e *Extension) requireLokiReader(w http.ResponseWriter) bool {
	if e.storageLogReader == nil {
		writeLokiError(w, "log storage not available — check collector configuration", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// ==================== query_range ====================

func (e *Extension) handleLokiQueryRange(w http.ResponseWriter, r *http.Request) {
	if !e.requireLokiReader(w) {
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		writeLokiError(w, "missing query parameter", http.StatusBadRequest)
		return
	}

	// Route metric queries (sum by, count_over_time, etc.) to metric handler.
	if logql.IsMetricQuery(q) {
		e.handleLokiMetricQuery(w, r, q)
		return
	}

	start, startOk := parseLokiTime(r.URL.Query().Get("start"))
	end, endOk := parseLokiTime(r.URL.Query().Get("end"))
	if !startOk || !endOk {
		writeLokiError(w, "invalid start/end time", http.StatusBadRequest)
		return
	}
	limit := lokiParseIntParam(r.URL.Query().Get("limit"), 100)
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "backward"
	}

	// Parse LogQL
	parsed, err := logql.Parse(q)
	if err != nil {
		e.logger.Debug("loki: failed to parse LogQL", zap.Error(err), zap.String("query", q))
		writeLokiError(w, "failed to parse query: "+err.Error(), http.StatusBadRequest)
		return
	}
	parsed.Start = start
	parsed.End = end
	parsed.Limit = limit
	parsed.Direction = direction

	// Convert to storage query
	ev := &logql.Evaluator{}
	storageQ := ev.Evaluate(parsed)

	// Execute
	logs, err := e.storageLogReader.SearchLogs(r.Context(), *storageQ)
	if err != nil {
		e.logger.Warn("loki: search logs failed", zap.Error(err))
		writeLokiError(w, "search failed", http.StatusInternalServerError)
		return
	}

	// Build Loki streams response
	writeLokiStreamsResponse(w, logs, direction)
}

// ==================== labels ====================

func (e *Extension) handleLokiLabels(w http.ResponseWriter, r *http.Request) {
	if !e.requireLokiReader(w) {
		return
	}
	start, _ := parseLokiTime(r.URL.Query().Get("start"))
	end, _ := parseLokiTime(r.URL.Query().Get("end"))
	tr := observabilitystorageext.TimeRange{Start: start, End: end}

	labels, err := e.storageLogReader.ListLogLabels(r.Context(), tr, "")
	if err != nil {
		e.logger.Warn("loki: list labels failed", zap.Error(err))
		writeLokiError(w, "labels query failed", http.StatusInternalServerError)
		return
	}

	if labels == nil {
		labels = []string{}
	}

	resp := lokiLabelsResponse{
		Status: "success",
		Data:   labels,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ==================== label values ====================

func (e *Extension) handleLokiLabelValues(w http.ResponseWriter, r *http.Request) {
	if !e.requireLokiReader(w) {
		return
	}
	label := r.PathValue("name")
	if label == "" {
		writeLokiError(w, "missing label name", http.StatusBadRequest)
		return
	}
	start, _ := parseLokiTime(r.URL.Query().Get("start"))
	end, _ := parseLokiTime(r.URL.Query().Get("end"))
	tr := observabilitystorageext.TimeRange{Start: start, End: end}

	values, err := e.storageLogReader.ListLogLabelValues(r.Context(), label, tr, "")
	if err != nil {
		e.logger.Warn("loki: list label values failed", zap.Error(err), zap.String("label", label))
		writeLokiError(w, "label values query failed", http.StatusInternalServerError)
		return
	}

	if values == nil {
		values = []string{}
	}

	resp := lokiLabelsResponse{
		Status: "success",
		Data:   values,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ==================== instant query ====================

func (e *Extension) handleLokiInstantQuery(w http.ResponseWriter, r *http.Request) {
	if !e.requireLokiReader(w) {
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		writeLokiError(w, "missing query parameter", http.StatusBadRequest)
		return
	}

	// Route metric queries (sum by, count_over_time, etc.) to metric handler.
	if logql.IsMetricQuery(q) {
		e.handleLokiMetricQuery(w, r, q)
		return
	}

	// Grafana health check sends "vector(1)+vector(1)" or "1+1" — not real LogQL.
	// Return a synthetic success response so Grafana marks the datasource healthy.
	// NOTE: Loki/Prometheus API convention for timestamps in "value" arrays is
	// Unix seconds as a floating-point number (e.g. "1784720984.899"), NOT nanoseconds.
	// Grafana's JSON parser rejects integers > 2^53 (JS number precision limit).
	if isLokiHealthCheckQuery(q) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now()
		// Format as seconds.nanoseconds (e.g. "1784720984.899813539")
		ts := fmt.Sprintf("%d.%09d", now.Unix(), now.Nanosecond())
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "vector",
				"result": []map[string]interface{}{
					{
						"metric": map[string]string{},
						"value":  []interface{}{json.Number(ts), "2"},
					},
				},
			},
		})
		return
	}

	limit := lokiParseIntParam(r.URL.Query().Get("limit"), 100)
	now := time.Now()

	parsed, err := logql.Parse(q)
	if err != nil {
		writeLokiError(w, "failed to parse query: "+err.Error(), http.StatusBadRequest)
		return
	}
	parsed.Start = now.Add(-1 * time.Minute)
	parsed.End = now
	parsed.Limit = limit
	parsed.Direction = "backward"

	ev := &logql.Evaluator{}
	storageQ := ev.Evaluate(parsed)

	logs, err := e.storageLogReader.SearchLogs(r.Context(), *storageQ)
	if err != nil {
		e.logger.Warn("loki: instant search failed", zap.Error(err))
		writeLokiError(w, "search failed", http.StatusInternalServerError)
		return
	}

	writeLokiStreamsResponse(w, logs, "backward")
}

// ==================== Response helpers ====================

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"` // [[timestamp_ns, log_line], ...]
}

type lokiQueryRangeResponse struct {
	Status string       `json:"status"`
	Data   lokiQueryData `json:"data"`
}

type lokiQueryData struct {
	ResultType string       `json:"resultType"`
	Result     []lokiStream `json:"result"`
}

type lokiLabelsResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
}

func writeLokiStreamsResponse(w http.ResponseWriter, result *observabilitystorageext.LogSearchResult, direction string) {
	// Group logs by label set → streams.
	streamMap := make(map[string]*lokiStream)
	streamOrder := make([]string, 0)

	for _, rec := range result.Logs {
		key := rec.ServiceName + "|" + rec.SeverityText
		s, ok := streamMap[key]
		if !ok {
		s = &lokiStream{
			Stream: map[string]string{
				"service_name":   rec.ServiceName,
				"severity":       rec.SeverityText,
				"level":          rec.SeverityText, // Loki standard — used for color coding
				"detected_level": rec.SeverityText, // Loki standard — used by logs-drilldown plugin
			},
		}
			streamMap[key] = s
			streamOrder = append(streamOrder, key)
		}
		line := rec.Body
		if line == "" {
			line = rec.SeverityText // fallback
		}
		tsNano := rec.TimeUnixNano
		s.Values = append(s.Values, []string{tsNano, line})
	}

	// Sort within each stream by timestamp.
	if direction == "forward" {
		for _, s := range streamMap {
			reverseValues(s.Values)
		}
	}

	streams := make([]lokiStream, 0, len(streamOrder))
	for _, k := range streamOrder {
		streams = append(streams, *streamMap[k])
	}

	resp := lokiQueryRangeResponse{
		Status: "success",
		Data: lokiQueryData{
			ResultType: "streams",
			Result:     streams,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeLokiError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "error",
		"error":  msg,
	})
}

func reverseValues(vals [][]string) {
	for i, j := 0, len(vals)-1; i < j; i, j = i+1, j-1 {
		vals[i], vals[j] = vals[j], vals[i]
	}
}

// parseLokiTime parses a Loki-format time value (nanosecond epoch).
func parseLokiTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	// Support nanoseconds: "1784707266594000000"
	ns, err := strconv.ParseInt(s, 10, 64)
	if err == nil && s != "" {
		return time.Unix(0, ns), true
	}
	// Support seconds: "1784707266"
	sec, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return time.Unix(sec, 0), true
	}
	return time.Time{}, false
}

func lokiParseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultVal
	}
	return v
}

// isLokiHealthCheckQuery returns true for synthetic queries that Grafana
// sends during datasource health checks. These are not valid LogQL but
// Grafana expects a successful response to mark the datasource as healthy.
func isLokiHealthCheckQuery(q string) bool {
	// Grafana sends "vector(1)+vector(1)" or "1+1" depending on version
	return strings.Contains(q, "vector(") || q == "1+1"
}

// ═══════════════════════════════════════════════════
// logs-drilldown app endpoints (Loki 3.x compatibility)
// ═══════════════════════════════════════════════════

// handleLokiDrilldownLimits returns a minimal config for the logs-drilldown app.
// The app uses this to discover Loki capabilities (volume_enabled, etc.).
// Returns a synthetic 200 response so the app doesn't show errors.
func (e *Extension) handleLokiDrilldownLimits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"volume_enabled": true,
		"volume_max_series": 1000,
		"max_query_series": 1000,
		"pattern_ingester_enabled": false,
		"version": "custom-otel-collector",
	})
}

// handleLokiIndexVolume returns label value volumes for the logs-drilldown app.
// Used to populate label pickers with per-value document counts.
//
// Query format: {service_name=~".+"}  →  extracts label name from stream selector
// Response format: Loki vector:
//
//	{"status":"success","data":{"resultType":"vector","result":[...]}}
func (e *Extension) handleLokiIndexVolume(w http.ResponseWriter, r *http.Request) {
	if !e.requireLokiReader(w) {
		return
	}

	q := r.URL.Query().Get("query")
	if q == "" {
		writeLokiError(w, "missing query parameter", http.StatusBadRequest)
		return
	}

	start, startOk := parseLokiTime(r.URL.Query().Get("start"))
	end, endOk := parseLokiTime(r.URL.Query().Get("end"))
	if !startOk || !endOk {
		writeLokiError(w, "invalid start/end time", http.StatusBadRequest)
		return
	}

	// Parse the stream selector to extract the primary label name.
	parsed, err := logql.Parse(q)
	if err != nil {
		e.logger.Warn("loki: failed to parse index/volume query", zap.String("query", q), zap.Error(err))
		writeLokiError(w, "failed to parse query", http.StatusBadRequest)
		return
	}

	// Determine the label to aggregate: use the first stream selector matcher.
	// The logs-drilldown app passes {service_name=~".+"} where service_name is the primaryLabel.
	groupByLabel := "service_name" // default
	if len(parsed.StreamSelector.Matchers) > 0 {
		groupByLabel = parsed.StreamSelector.Matchers[0].Name
	}

	// Evaluate the query into a storage LogQuery (applies filters, time range, etc.)
	parsed.Start = start
	parsed.End = end
	ev := &logql.Evaluator{}
	storageQ := ev.Evaluate(parsed)

	// Execute a metric aggregation: one terms agg on the groupByLabel.
	metricQ := &observabilitystorageext.LogMetricQuery{
		LogQuery:      *storageQ,
		GroupByLabels: []string{groupByLabel},
		IntervalNanos: end.Sub(start).Nanoseconds(), // single bucket for the whole range
		TopN:          lokiParseIntParam(r.URL.Query().Get("limit"), 100),
	}

	result, err := e.storageLogReader.SearchLogMetric(r.Context(), *metricQ)
	if err != nil {
		e.logger.Warn("loki: index volume query failed", zap.Error(err))
		writeLokiError(w, "volume query failed", http.StatusInternalServerError)
		return
	}

	// Convert metric result to vector format.
	// Matrix result → vector: take the last value from each series.
	writeLokiVectorResponse(w, result)
}

// writeLokiVectorResponse converts LogMetricResult to Loki vector format.
func writeLokiVectorResponse(w http.ResponseWriter, result *observabilitystorageext.LogMetricResult) {
	type vectorRow struct {
		Metric map[string]string `json:"metric"`
		Value  []interface{}     `json:"value"` // [timestamp_seconds_number, "value_string"]
	}

	rows := make([]vectorRow, 0, len(result.Series))
	for _, s := range result.Series {
		// Use the first (or last) value point as the vector value.
		if len(s.Values) == 0 {
			continue
		}
		v := s.Values[len(s.Values)-1] // last bucket = total count

		secs := v.TimestampNano / 1_000_000_000
		nanos := v.TimestampNano % 1_000_000_000
		ts := json.Number(fmt.Sprintf("%d.%09d", secs, nanos))

		rows = append(rows, vectorRow{
			Metric: s.Labels,
			Value:  []interface{}{ts, fmt.Sprintf("%d", int64(v.Value))},
		})
	}

	if rows == nil {
		rows = []vectorRow{}
	}

	resp := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"resultType": "vector",
			"result":     rows,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
