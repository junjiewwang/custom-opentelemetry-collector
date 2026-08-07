// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/collector/custom/extension/adminext/logql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════
// LogQL Metric Query Handler (count_over_time, sum by, etc.)
// ═══════════════════════════════════════════════════

// isMetricQuery checks whether a raw LogQL string represents a metric query
// (starts with an aggregation keyword or range function).
func isMetricQuery(q string) bool {
	return isLokiHealthCheckQuery(q) || logql.IsMetricQuery(q)
}

// handleLokiMetricQuery executes a metric query and returns a matrix response.
//
// It handles Grafana Logs Volume queries:
//
//	sum by (level, detected_level) (count_over_time({} |= ""[5m]))
//
// Response format (Loki matrix):
//
//	{"status":"success","data":{"resultType":"matrix","result":[...]}}
func (h *lokiHandlers) handleLokiMetricQuery(w http.ResponseWriter, r *http.Request, q string) {
	if !h.requireLokiReader(w) {
		return
	}

	// `step` is a duration (e.g. "15", "15s", "5m"), NOT an epoch timestamp.
	// Parse it as a Prometheus duration; reusing parseLokiTime (epoch semantics)
	// silently drops suffixed values like "15s".
	var step time.Duration
	if s := r.FormValue("step"); s != "" {
		if d, err := parsePrometheusDuration(s); err == nil {
			step = d
		}
	}

	// Parse the metric expression
	expr, err := logql.ParseMetric(q)
	if err != nil {
		h.logger.Warn("loki: failed to parse metric query",
			zap.Error(err),
			zap.String("query", q),
		)
		writeLokiError(w, "failed to parse metric query: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Range queries use start/end (+ step). Instant queries use a single
	// "time" parameter. Grafana/Loki instant queries (e.g. the Logs Drilldown
	// ad-hoc metric mode, or `count_over_time({...}[5m])` evaluated at a point)
	// must evaluate the range vector by looking back RangeDuration ending at
	// `time`, and return a VECTOR — not an empty matrix over a zero-width range.
	start, startOk := parseLokiTime(r.FormValue("start"))
	end, endOk := parseLokiTime(r.FormValue("end"))
	instant := false
	if !startOk || !endOk {
		t, tOk := parseLokiTime(r.FormValue("time"))
		if !tOk {
			writeLokiError(w, "invalid start/end/time", http.StatusBadRequest)
			return
		}
		lookback := expr.RangeDuration
		if lookback <= 0 {
			lookback = 5 * time.Minute // default for instant-only vectors
		}
		start, end = t.Add(-lookback), t
		instant = true
	}

	// Resolve inner branches: OR-decomposed or single query.
	innerBranches := expr.InnerBranches
	if innerBranches == nil {
		innerBranches = []*logql.LogQLQuery{expr.Inner}
	}

	// Compute histogram interval.
	interval := computeMetricInterval(expr.RangeDuration, step, start, end)
	if instant {
		// Single bucket spanning the whole lookback window.
		interval = end.Sub(start).Nanoseconds()
	}

	// Evaluate and execute each OR branch independently.
	var branchResults []*observabilitystorageext.LogMetricResult
	logEv := &logql.Evaluator{}
	for _, branch := range innerBranches {
		branch.Start = start
		branch.End = end
		storageQ := logEv.Evaluate(branch)

		topN := 10
		if instant {
			topN = 1000 // vector must not be truncated by default TopN
		}
		metricQ := &observabilitystorageext.LogMetricQuery{
			LogQuery:      *storageQ,
			GroupByLabels: expr.By,
			IntervalNanos: interval,
			TopN:          topN,
		}

		result, err := h.logReader.SearchLogMetric(r.Context(), *metricQ)
		if err != nil {
			h.logger.Warn("loki: metric query failed for OR branch", zap.Error(err))
			continue
		}
		branchResults = append(branchResults, result)
	}

	if len(branchResults) == 0 {
		writeLokiError(w, "all metric OR branches failed", http.StatusInternalServerError)
		return
	}
	merged := mergeMetricResults(branchResults)
	if instant {
		writeLokiVectorResponse(w, merged)
	} else {
		writeLokiMatrixResponse(w, merged)
	}
}

// computeMetricInterval determines the histogram bucket interval in nanoseconds.
//
// Priority:
//  1. RangeDuration from the metric expression (e.g. 5m)
//  2. Step parameter from HTTP request
//  3. Auto-calculate from time range (target ~100 buckets)
func computeMetricInterval(rangeDur time.Duration, step time.Duration, start, end time.Time) int64 {
	// Use the range vector duration as the histogram interval (most natural).
	if rangeDur > 0 {
		return int64(rangeDur)
	}

	// Use step from HTTP request.
	if step != 0 {
		dur := step
		if dur > 0 {
			return int64(dur)
		}
	}

	// Auto-calculate: target ~100 buckets.
	rangeNanos := end.Sub(start).Nanoseconds()
	if rangeNanos <= 0 {
		return 300_000_000_000 // fallback: 5min
	}
	interval := rangeNanos / 100
	if interval < 1_000_000_000 { // min 1 second
		interval = 1_000_000_000
	}
	return interval
}

// ── Matrix (metric) Response Builder ──────────────────

type lokiMatrixResponse struct {
	Status string         `json:"status"`
	Data   lokiMatrixData `json:"data"`
}

type lokiMatrixData struct {
	ResultType string          `json:"resultType"`
	Result     []lokiMatrixRow `json:"result"`
}

type lokiMatrixRow struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"` // [[timestamp_seconds_as_number, "value_string"], ...]
}

func writeLokiMatrixResponse(w http.ResponseWriter, result *observabilitystorageext.LogMetricResult) {
	rows := make([]lokiMatrixRow, 0, len(result.Series))
	for _, s := range result.Series {
		values := make([][]interface{}, 0, len(s.Values))
		for _, v := range s.Values {
			// Loki/Prometheus convention: timestamp as seconds.nanoseconds float.
			secs := v.TimestampNano / 1_000_000_000
			nanos := v.TimestampNano % 1_000_000_000
			ts := json.Number(fmt.Sprintf("%d.%09d", secs, nanos))
			values = append(values, []interface{}{
				ts,
				fmt.Sprintf("%d", int64(v.Value)),
			})
		}
		rows = append(rows, lokiMatrixRow{
			Metric: s.Labels,
			Values: values,
		})
	}

	if rows == nil {
		rows = []lokiMatrixRow{}
	}

	resp := lokiMatrixResponse{
		Status: "success",
		Data: lokiMatrixData{
			ResultType: "matrix",
			Result:     rows,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
