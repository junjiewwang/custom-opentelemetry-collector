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
func (e *Extension) handleLokiMetricQuery(w http.ResponseWriter, r *http.Request, q string) {
	if !e.requireLokiReader(w) {
		return
	}

	// Instant queries use "time", range queries use "start"/"end".
	start, startOk := parseLokiTime(r.FormValue("start"))
	end, endOk := parseLokiTime(r.FormValue("end"))
	if !startOk || !endOk {
		// Fallback: instant query uses "time" parameter.
		t, tOk := parseLokiTime(r.FormValue("time"))
		if tOk {
			start, end, startOk, endOk = t, t, true, true
		}
	}
	if !startOk || !endOk {
		writeLokiError(w, "invalid start/end time", http.StatusBadRequest)
		return
	}

	step, _ := parseLokiTime(r.FormValue("step"))

	// Parse the metric expression
	expr, err := logql.ParseMetric(q)
	if err != nil {
		e.logger.Warn("loki: failed to parse metric query",
			zap.Error(err),
			zap.String("query", q),
		)
		writeLokiError(w, "failed to parse metric query: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Evaluate inner log query (stream selector + filters) → LogQuery
	expr.Inner.Start = start
	expr.Inner.End = end
	logEv := &logql.Evaluator{}
	storageQ := logEv.Evaluate(expr.Inner)

	// Compute histogram interval.
	// Priority: 1) MetricExpr range duration  2) HTTP step parameter  3) auto-calculate
	interval := computeMetricInterval(expr.RangeDuration, step, start, end)

	// Build the metric query
	metricQ := &observabilitystorageext.LogMetricQuery{
		LogQuery:     *storageQ,
		GroupByLabels: expr.By,
		IntervalNanos: interval,
		TopN:         10,
	}

	result, err := e.storageLogReader.SearchLogMetric(r.Context(), *metricQ)
	if err != nil {
		e.logger.Warn("loki: metric query failed", zap.Error(err))
		writeLokiError(w, "metric query failed", http.StatusInternalServerError)
		return
	}

	// Build matrix response
	writeLokiMatrixResponse(w, result)
}

// computeMetricInterval determines the histogram bucket interval in nanoseconds.
//
// Priority:
//  1. RangeDuration from the metric expression (e.g. 5m)
//  2. Step parameter from HTTP request
//  3. Auto-calculate from time range (target ~100 buckets)
func computeMetricInterval(rangeDur time.Duration, step time.Time, start, end time.Time) int64 {
	// Use the range vector duration as the histogram interval (most natural).
	if rangeDur > 0 {
		return int64(rangeDur)
	}

	// Use step from HTTP request.
	if !step.IsZero() {
		dur := step.Sub(time.Unix(0, 0))
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
