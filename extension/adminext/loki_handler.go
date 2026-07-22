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

	// Grafana health check sends "vector(1)+vector(1)" or "1+1" — not real LogQL.
	// Return a synthetic success response so Grafana marks the datasource healthy.
	if isLokiHealthCheckQuery(q) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now()
		ts := fmt.Sprintf("%d", now.UnixNano())
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "vector",
				"result": []map[string]interface{}{
					{
						"metric": map[string]string{},
						"value":  []string{ts, "2"},
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
					"service_name": rec.ServiceName,
					"severity":     rec.SeverityText,
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
