// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════
// Prometheus v1 Compatible HTTP API
// (for Grafana Prometheus data source)
//
// Implemented endpoints:
//
//	GET/POST /api/v1/query        — instant query → vector
//	GET/POST /api/v1/query_range  — range query  → matrix
//	GET/POST /api/v1/labels       — label names
//	GET      /api/v1/label/{name}/values — label values
//	GET/POST /api/v1/series       — series metadata
//	GET      /api/v1/metadata     — metric metadata
//
// PromQL subset supported:
//
//	metric_name
//	metric_name{label1="val", label2=~"regex.*"}
//	sum(metric) by (label1, label2)
//	avg/max/min/count(metric) [by (labels)]
//	rate(metric[5m]), increase(metric[5m]), irate(metric[5m])
//	sum(rate(metric[5m])) by (label1)
//	histogram_quantile(0.95, sum(rate(metric_bucket[5m])) by (le))
//	topk(5, sum(rate(metric[30m])) by (label))
//	bottomk(5, sum(rate(metric[30m])) by (label))
//
// Grafana configuration:
//
//	Type: Prometheus
//	URL:  http://<collector>:8088/api/v2/prometheus
//	Access: Server (proxy)
//	Auth: Basic Auth (same as admin API)
// ═══════════════════════════════════════════════════

// ── Prometheus JSON envelope types ──────────────────

type promResponse struct {
	Status    string       `json:"status"`
	Data      any          `json:"data,omitempty"`
	ErrorType string       `json:"errorType,omitempty"`
	Error     string       `json:"error,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
}

type promQueryData struct {
	ResultType string      `json:"resultType"`
	Result     any         `json:"result"`
}

// promMetric is the label set for a Prometheus series.
type promMetric map[string]string

// promVectorSample is a single instant sample: [timestamp, "value"]
type promVectorSample struct {
	Metric promMetric `json:"metric"`
	Value  []any      `json:"value"`
}

// promMatrixSample is a range series: [[timestamp, "value"], ...]
type promMatrixSample struct {
	Metric promMetric `json:"metric"`
	Values [][]any    `json:"values"`
}

// ── Parsed PromQL expression ────────────────────────

// promqlExpr holds a parsed PromQL expression.
type promqlExpr struct {
	MetricName    string
	Labels        map[string]string // exact match (=)
	LabelMatch    map[string]string // regex match (=~)
	Aggregation   string            // sum, avg, max, min, count, ""
	GroupBy       []string          // by (label1, label2)
	RangeDuration time.Duration     // [5m] for rate/increase/irate
	Function      string            // rate, increase, irate, ""
	HistogramSub  string            // "sum" or "bucket" (for HS sub-series), ""
	BaseMetric    string            // original metric name before _sum/_bucket strip
	Quantile      float64           // θ for histogram_quantile, NaN if not set
	TopK          int               // K for topk/bottomk; 0 = not set
	IsBottomK     bool              // true = bottomk (smallest K), false = topk (largest K)
}

// ── Handler: query ─────────────────────────────────

// handlePromQuery handles GET/POST /api/v1/query.
func (e *Extension) handlePromQuery(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	queryStr := e.getQueryParam(r, "query")
	if queryStr == "" {
		e.writePromError(w, "bad_data", "parameter 'query' is required")
		return
	}

	// Parse query time (default: now)
	var evalTime time.Time
	if ts := r.FormValue("time"); ts != "" {
		if t, err := parsePrometheusTime(ts); err == nil {
			evalTime = t
		}
	}
	if evalTime.IsZero() {
		evalTime = time.Now()
	}

	expr, err := parsePromQL(queryStr)
	if err != nil {
		e.writePromError(w, "bad_data", err.Error())
		return
	}

	// Attach PromQL expression details to the OTel span for observability
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String(SpanAttrPromQLExpr, queryStr),
		attribute.String(SpanAttrPromQLMetric, expr.MetricName),
		attribute.String(SpanAttrPromQLAggregation, expr.Aggregation),
		attribute.StringSlice(SpanAttrPromQLGroupBy, expr.GroupBy),
		attribute.String(SpanAttrPromQLFunction, expr.Function),
		attribute.Int(SpanAttrPromQLTopK, expr.TopK),
		attribute.Bool(SpanAttrPromQLIsBottomK, expr.IsBottomK),
	)

	result := e.dispatchInstantQuery(r, expr, evalTime, expr.Labels, expr.LabelMatch)
	if result == nil {
		result = &promQueryData{ResultType: ResultTypeVector, Result: []promVectorSample{}}
	}
	e.writePromSuccess(w, result)
}

// ── Handler: query_range ───────────────────────────

// handlePromQueryRange handles GET/POST /api/v1/query_range.
func (e *Extension) handlePromQueryRange(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	queryStr := e.getQueryParam(r, "query")
	if queryStr == "" {
		e.writePromError(w, "bad_data", "parameter 'query' is required")
		return
	}

	start, err := parsePrometheusTime(r.FormValue("start"))
	if err != nil {
		e.writePromError(w, "bad_data", "invalid 'start' parameter: "+err.Error())
		return
	}
	end, err := parsePrometheusTime(r.FormValue("end"))
	if err != nil {
		e.writePromError(w, "bad_data", "invalid 'end' parameter: "+err.Error())
		return
	}
	step, err := parsePrometheusDuration(r.FormValue("step"))
	if err != nil {
		e.writePromError(w, "bad_data", "invalid 'step' parameter: "+err.Error())
		return
	}

	expr, err := parsePromQL(queryStr)
	if err != nil {
		e.writePromError(w, "bad_data", err.Error())
		return
	}

	// Attach PromQL expression details to the OTel span for observability
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String(SpanAttrPromQLExpr, queryStr),
		attribute.String(SpanAttrPromQLMetric, expr.MetricName),
		attribute.String(SpanAttrPromQLAggregation, expr.Aggregation),
		attribute.StringSlice(SpanAttrPromQLGroupBy, expr.GroupBy),
		attribute.String(SpanAttrPromQLFunction, expr.Function),
		attribute.Int(SpanAttrPromQLTopK, expr.TopK),
		attribute.Bool(SpanAttrPromQLIsBottomK, expr.IsBottomK),
	)

	result := e.dispatchRangeQuery(r, expr, start, end, step, expr.Labels, expr.LabelMatch)
	if result == nil {
		result = &promQueryData{ResultType: ResultTypeMatrix, Result: []promMatrixSample{}}
	}
	e.writePromSuccess(w, result)
}

// extractMetricNameFromMatch extracts the __name__ label from Prometheus match[]
// selectors. Returns "" if no metric name is found (lists all labels).
// Handles both exact (=) and regex (=~) selectors:
//
//	{__name__="traces_service_graph_request_total"}         → "traces_service_graph_request_total"
//	{__name__=~".*traces_service_graph_request_total.*"}    → "traces_service_graph_request_total"
//
// For regex with | alternation, returns "" so the caller knows to use extractMetricNamesFromMatch
// instead to get individual metric names.
func extractMetricNameFromMatch(matches []string) string {
	for _, m := range matches {
		i := strings.Index(m, PromLabelName)
		if i < 0 {
			continue
		}
		rest := m[i+len(PromLabelName):]

		// Find the operator: either "=" or "=~".
		eq := strings.Index(rest, "=")
		if eq < 0 {
			continue
		}
		opEnd := eq + 1
		isRegex := opEnd < len(rest) && rest[opEnd] == '~'
		if isRegex {
			opEnd++
		}

		// Find the value between quotes after the operator.
		valStart := strings.IndexAny(rest[opEnd:], `"'`+"`")
		if valStart < 0 {
			continue
		}
		valStart += opEnd
		quote := rest[valStart]
		valEnd := strings.IndexByte(rest[valStart+1:], quote)
		if valEnd < 0 {
			continue
		}
		raw := rest[valStart+1 : valStart+1+valEnd]

		// For regex patterns (=~):
		// - If the pattern contains | (alternation), return "" so caller uses
		//   extractMetricNamesFromMatch to get all names individually.
		// - If the pattern is wrapped in .* (e.g. ".*metric_name.*"), strip the
		//   wildcards and return the single metric name for targeted filtering.
		if isRegex {
			if strings.Contains(raw, "|") {
				return ""
			}
			raw = strings.TrimPrefix(raw, ".*")
			raw = strings.TrimSuffix(raw, ".*")
		}
		return raw
	}
	return ""
}

// extractMetricNamesFromMatch extracts all individual metric names from a regex
// with | alternation. Used when the match pattern is __name__=~"a|b|c".
// Each metric name has leading/trailing .* stripped.
func extractMetricNamesFromMatch(matches []string) []string {
	for _, m := range matches {
		i := strings.Index(m, PromLabelName)
		if i < 0 {
			continue
		}
		rest := m[i+len(PromLabelName):]

		eq := strings.Index(rest, "=")
		if eq < 0 {
			continue
		}
		opEnd := eq + 1
		if opEnd < len(rest) && rest[opEnd] == '~' {
			opEnd++
		}

		valStart := strings.IndexAny(rest[opEnd:], `"'`+"`")
		if valStart < 0 {
			continue
		}
		valStart += opEnd
		quote := rest[valStart]
		valEnd := strings.IndexByte(rest[valStart+1:], quote)
		if valEnd < 0 {
			continue
		}
		raw := rest[valStart+1 : valStart+1+valEnd]

		// Split by | to get individual metric name patterns.
		parts := strings.Split(raw, "|")
		names := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			p = strings.TrimPrefix(p, ".*")
			p = strings.TrimSuffix(p, ".*")
			if p != "" {
				names = append(names, p)
			}
		}
		return names
	}
	return nil
}

// ── Handler: labels ────────────────────────────────

// handlePromLabels handles GET/POST /api/v1/labels.
func (e *Extension) handlePromLabels(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	tr := observabilitystorageext.TimeRange{}
	if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
		tr.Start = start
	}
	if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
		tr.End = end
	}

	// Extract optional metric name from match[] parameter.
	metricName := extractMetricNameFromMatch(r.Form["match[]"])

	var names []string
	if metricName == "" {
		// Regex with | alternation (e.g. __name__=~"a|b|c"):
		// query labels for each metric individually and union the results.
		metricNames := extractMetricNamesFromMatch(r.Form["match[]"])
		if len(metricNames) > 0 {
			labelSet := make(map[string]struct{})
			for _, mn := range metricNames {
				n, err := e.storageMetricReader.ListLabelNames(r.Context(), tr, mn)
				if err != nil {
					e.logger.Debug("list label names skipped",
						zap.String("metric", mn), zap.Error(err))
					continue
				}
				for _, label := range n {
					labelSet[label] = struct{}{}
				}
			}
			for k := range labelSet {
				names = append(names, k)
			}
		} else {
			// No match filter → list ALL labels across ALL metrics.
			var err error
			names, err = e.storageMetricReader.ListLabelNames(r.Context(), tr, "")
			if err != nil {
				e.writePromError(w, "execution", err.Error())
				return
			}
		}
	} else {
		var err error
		names, err = e.storageMetricReader.ListLabelNames(r.Context(), tr, metricName)
		if err != nil {
			e.writePromError(w, "execution", err.Error())
			return
		}
	}

	// Always include __name__
	hasName := false
	for _, n := range names {
		if n == PromLabelName {
			hasName = true
			break
		}
	}
	if !hasName {
		names = append(names, PromLabelName)
	}

	// Translate ES dot-format label names back to PromQL underscore format
	// (e.g. "service.name" → "service_name") so Grafana uses valid Prometheus
	// label names that the label_translator can map back correctly at query time.
	for i, n := range names {
		names[i] = translateLabelToPromQL(n)
	}

	e.writePromSuccessLabelList(w, names)
}

// ── Handler: label values ──────────────────────────

// handlePromLabelValues handles GET /api/v1/label/{labelName}/values.
func (e *Extension) handlePromLabelValues(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	labelName := decodePrometheusLabelName(chi.URLParam(r, "labelName"))
	if labelName == "" {
		e.writePromError(w, "bad_data", "parameter 'labelName' is required")
		return
	}

	// Handle __name__ (metric names)
	if labelName == PromLabelName {
		tr := observabilitystorageext.TimeRange{}
		if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
			tr.Start = start
		}
		if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
			tr.End = end
		}
		names, err := e.storageMetricReader.ListMetricNames(r.Context(), tr)
		if err != nil {
			e.writePromError(w, "execution", err.Error())
			return
		}
		e.writePromSuccessLabelList(w, names)
		return
	}

	tr := observabilitystorageext.TimeRange{}
	if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
		tr.Start = start
	}
	if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
		tr.End = end
	}

	// Translate PromQL underscore label name to ES dot format for the storage query.
	// e.g. Grafana sends "span_kind" → ES stores "span.kind".
	esLabelName := prometheusToOtelLabelKeys[labelName]
	if esLabelName == "" {
		esLabelName = labelName
	}

	values, err := e.storageMetricReader.ListLabelValues(r.Context(), esLabelName, tr)
	if err != nil {
		e.writePromError(w, "execution", err.Error())
		return
	}

	e.writePromSuccessLabelList(w, values)
}

// ── Handler: series ────────────────────────────────

// handlePromSeries handles GET/POST /api/v1/series.
func (e *Extension) handlePromSeries(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	// Parse match[] parameters
	matchParams := r.Form["match[]"]
	if len(matchParams) == 0 {
		e.writePromError(w, "bad_data", "parameter 'match[]' is required")
		return
	}

	var allSeries []promMetric
	for _, matchStr := range matchParams {
		expr, err := parsePromQLSelector(matchStr)
		if err != nil {
			continue
		}

		tr := observabilitystorageext.TimeRange{}
		if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
			tr.Start = start
		}
		if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
			tr.End = end
		}

		query := observabilitystorageext.MetricQuery{
			MetricName: expr.MetricName,
			Labels:     expr.Labels,
			Time:       tr.End,
		}
		if query.Time.IsZero() {
			query.Time = time.Now()
		}

		result, err := e.storageMetricReader.Query(r.Context(), query)
		if err != nil {
			continue
		}
		for _, dp := range result.Data {
			m := promMetric{PromLabelName: expr.MetricName}
			for k, v := range dp.Labels {
				m[translateLabelToPromQL(k)] = v
			}
			allSeries = append(allSeries, m)
		}
	}

	e.writePromSuccess(w, allSeries)
}

// ── Handler: metadata ──────────────────────────────

// handlePromMetadata handles GET /api/v1/metadata.
func (e *Extension) handlePromMetadata(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	tr := observabilitystorageext.TimeRange{}
	if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
		tr.Start = start
	}
	if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
		tr.End = end
	}

	names, err := e.storageMetricReader.ListMetricNames(r.Context(), tr)
	if err != nil {
		e.writePromError(w, "execution", err.Error())
		return
	}

	// Return minimal metadata (type: gauge for all metrics)
	metadata := make(map[string][]map[string]string, len(names))
	for _, name := range names {
		metadata[name] = []map[string]string{
			{"type": "gauge", "help": "", "unit": ""},
		}
	}

	e.writePromSuccess(w, metadata)
}

// ── Query dispatch ─────────────────────────────────

// dispatchLabelExplore handles PromQL "group by" label exploration queries.
// Uses ES terms aggregation to return unique label value combinations.
func (e *Extension) dispatchLabelExplore(r *http.Request, expr *promqlExpr) *promQueryData {
	query := observabilitystorageext.LabelCombinationsQuery{
		MetricName: expr.MetricName,
		LabelKeys:  expr.GroupBy,
	}

	result, err := e.storageMetricReader.ListLabelCombinations(r.Context(), query)
	if err != nil {
		e.logger.Error("label explore failed", zap.Error(err))
		return nil
	}

	vectors := make([]promVectorSample, 0, len(result.Combinations))
	for _, combo := range result.Combinations {
		m := promMetric{PromLabelName: expr.MetricName}
		for k, v := range combo {
			m[translateLabelToPromQL(k)] = v
		}
		vectors = append(vectors, promVectorSample{
			Metric: m,
			Value:  []any{float64(time.Now().Unix()), "1"},
		})
	}

	return &promQueryData{
		ResultType: ResultTypeVector,
		Result:     vectors,
	}
}

// dispatchInstantQuery routes to the appropriate backend call based on the parsed expression.
func (e *Extension) dispatchInstantQuery(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	// Label exploration: "group by" without aggregation or rate function.
	// Example: group by (client, connection_type, server) (traces_service_graph_request_total)
	if len(expr.GroupBy) > 0 && expr.Aggregation == "" && expr.Function == "" {
		return e.dispatchLabelExplore(r, expr)
	}

	// ── Data fetching ─────────────────────────────────────

	var vectors []promVectorSample

	if expr.Function != "" && expr.RangeDuration > 0 {
		// rate/increase/irate path: QueryFlat → group → compute rate.
		// histogram_quantile is handled inside execRateInstant.
		vectors = e.execRateInstant(r, expr, evalTime, labels, labelMatch)
		if vectors == nil {
			vectors = []promVectorSample{}
		}
	} else {
		// Plain instant query.
		query := observabilitystorageext.MetricQuery{
			MetricName: expr.MetricName,
			Labels:     filterInternalLabels(labels),
			LabelMatch: labelMatch,
			Time:       evalTime,
		}

		// Record the effective ES query labels on the span
		span.SetAttributes(
			attribute.StringSlice("es.labels", flattenLabels(query.Labels)),
		)

		result, err := e.storageMetricReader.Query(r.Context(), query)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String(SpanAttrErrorType, "query"))
			e.logger.Error("prom instant query failed", zap.Error(err))
			return nil
		}

		vectors = make([]promVectorSample, 0, len(result.Data))
		for _, dp := range result.Data {
			var val float64
			if expr.HistogramSub == HistogramSubBucket {
				val = resolveHistogramBucket(dp, expr)
			} else {
				val = dp.Value
			}

			name := expr.MetricName
			if expr.HistogramSub != "" {
				name = expr.BaseMetric + "_" + expr.HistogramSub
			}
			m := promMetric{PromLabelName: name}
			for k, v := range dp.Labels {
				if expr.HistogramSub == HistogramSubBucket && k == PromLabelLe {
					continue
				}
				m[translateLabelToPromQL(k)] = v
			}
			// For _bucket: preserve the le label from the query.
			if expr.HistogramSub == HistogramSubBucket {
				if le, ok := expr.Labels[PromLabelLe]; ok {
					m[PromLabelLe] = le
				}
			}
			vectors = append(vectors, promVectorSample{
				Metric: m,
				Value:  []any{float64(parseTimeUnixMilli(dp.TimeUnixMilli)) / 1000.0, formatPromValue(val)},
			})
		}
	}

	span.SetAttributes(attribute.Int(SpanAttrPromQLSeriesCount, len(vectors)))

	// ── Unified post-processing pipeline ──────────────────

	// Step 1: Aggregation (skip for histogram_quantile — it's handled internally).
	if expr.Aggregation != "" && expr.Aggregation != AggHistogramQuantile {
		vectors = applyAggregation(expr.Aggregation, expr.GroupBy, vectors)
		span.SetAttributes(attribute.Int(SpanAttrPromQLAggregatedCount, len(vectors)))
		// Standard Prometheus behavior: aggregation results only retain groupBy labels.
		stripMetricToGroupBy(vectors, expr.GroupBy)
	}

	// Step 2: topk/bottomk post-processing.
	if expr.TopK > 0 {
		vectors = applyTopK(expr.TopK, expr.IsBottomK, vectors)
		span.SetAttributes(attribute.Int(SpanAttrPromQLTopKCount, len(vectors)))
	}

	// ── Wrap and return ───────────────────────────────────

	return &promQueryData{
		ResultType: ResultTypeVector,
		Result:     vectors,
	}
}

// dispatchRangeQuery routes to the appropriate backend call based on the parsed expression.
func (e *Extension) dispatchRangeQuery(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	if expr.Function != "" && expr.RangeDuration > 0 {
		// histogram_quantile + _bucket range query
		if expr.HistogramSub == "bucket" && !math.IsNaN(expr.Quantile) {
			defer span.SetAttributes(attribute.Int("promql.series_count", 0))
			result := e.execHistogramQuantileRange(r, expr, start, end, step, labels, labelMatch)
			if result == nil {
				return &promQueryData{ResultType: "matrix", Result: []promMatrixSample{}}
			}
			return result
		}
		// rate/increase/irate path: use QueryRaw to get original samples
		defer span.SetAttributes(attribute.Int("promql.series_count", 0))
		result := e.execRateRange(r, expr, start, end, step, labels, labelMatch)
		if result == nil {
			return &promQueryData{ResultType: ResultTypeMatrix, Result: []promMatrixSample{}}
		}
		return result
	}

	query := observabilitystorageext.MetricRangeQuery{
		MetricName:  expr.MetricName,
		Labels:      filterInternalLabels(labels),
		LabelMatch:  labelMatch,
		TimeRange:   observabilitystorageext.TimeRange{Start: start, End: end},
		Step:        step,
		Aggregation: expr.Aggregation,
		GroupBy:     expr.GroupBy,
	}
	if query.Aggregation == "" {
		query.Aggregation = AggAvg
	}

	// Record the effective ES query labels on the span
	span.SetAttributes(
		attribute.StringSlice(SpanAttrESLabels, flattenLabels(query.Labels)),
		attribute.StringSlice(SpanAttrESGroupByFull, query.GroupBy),
	)

	result, err := e.storageMetricReader.QueryRange(r.Context(), query)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String(SpanAttrErrorType, "query_range"))
		e.logger.Error("prom range query failed", zap.Error(err))
		return nil
	}

	span.SetAttributes(attribute.Int(SpanAttrPromQLSeriesCount, len(result.Data)))

	matrix := make([]promMatrixSample, 0, len(result.Data))
	for _, series := range result.Data {
		m := buildSeriesMetric(expr, series.Labels)

		values := make([][]any, 0, len(series.Values))
		for _, tv := range series.Values {
			values = append(values, []any{
				float64(parseTimeUnixMilli(tv.TimeUnixMilli)) / 1000.0,
				formatPromValue(tv.Value),
			})
		}

		matrix = append(matrix, promMatrixSample{
			Metric: m,
			Values: values,
		})
	}

	return &promQueryData{
		ResultType: ResultTypeMatrix,
		Result:     matrix,
	}
}

// flattenLabels converts label map to key=value string slice for span attributes.
func flattenLabels(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	result := make([]string, 0, len(labels))
	for k, v := range labels {
		result = append(result, k+"="+v)
	}
	return result
}

// ── rate/increase/irate support via QueryRaw ────────

// execRateRange handles rate/increase/irate range queries.
// Uses QueryFlat (flat ES search, no top_hits limit) instead of QueryRaw
// to avoid ES max_inner_result_window constraint on long time windows.
func (e *Extension) execRateRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	// Extend time range back by the range duration for lookback.
	lookbackStart := start.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		LabelMatch: labelMatch,
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	flatResult, err := e.concurrentQueryFlat(r.Context(), flatQuery, e.logger)
	if err != nil {
		e.logger.Error("prom query_flat failed", zap.Error(err))
		return nil
	}
	if flatResult == nil || len(flatResult.Samples) == 0 {
		return nil
	}

	e.checkFlatTruncation(flatResult)

	// Group samples by labels in Go (replaces ES-side composite aggregation).
	sampleGroups := groupMetricSamplesByLabels(flatResult.Samples)

	matrix := make([]promMatrixSample, 0, len(sampleGroups))
	for _, sg := range sampleGroups {
		if len(sg.Samples) < 2 {
			continue
		}

		// Reconstruct Prometheus metric name with _sum/_bucket suffix if applicable.
		name := expr.MetricName
		if expr.HistogramSub != "" {
			name = expr.BaseMetric + "_" + expr.HistogramSub
		}
		m := promMetric{PromLabelName: name}
		for k, v := range sg.Labels {
			// Translate ES dot-format keys back to PromQL underscore format.
			m[translateLabelToPromQL(k)] = v
		}

		values := e.computeRate(sg.Samples, start, end, step, expr)
		matrix = append(matrix, promMatrixSample{
			Metric: m,
			Values: values,
		})
	}

	if expr.Aggregation != "" && len(expr.GroupBy) > 0 {
		matrix = aggregateMatrix(expr.Aggregation, expr.GroupBy, matrix)
		stripMatrixMetricToGroupBy(matrix, expr.GroupBy)
	}

	return &promQueryData{
		ResultType: ResultTypeMatrix,
		Result:     matrix,
	}
}

// execRateInstant handles rate/increase/irate instant queries.
func (e *Extension) execRateInstant(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) []promVectorSample {
	// histogram_quantile + _bucket → aggregate bucket_counts and compute quantile.
	if expr.HistogramSub == HistogramSubBucket && !math.IsNaN(expr.Quantile) {
		return e.execHistogramQuantileInstant(r, expr, evalTime, labels, labelMatch)
	}

	// For instant query, look back from evalTime by the range duration.
	lookbackStart := evalTime.Add(-expr.RangeDuration)

	// Use QueryFlat (same as execRateRange) to avoid ES top_hits limit and
	// painless script hardcoded label fields. Data is grouped by labels in Go.
	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		LabelMatch: labelMatch,
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: evalTime},
	}

	flatResult, err := e.concurrentQueryFlat(r.Context(), flatQuery, e.logger)
	if err != nil {
		e.logger.Error("prom query_flat failed", zap.Error(err))
		return nil
	}
	if flatResult == nil || len(flatResult.Samples) == 0 {
		return nil
	}

	e.checkFlatTruncation(flatResult)

	// Group samples by labels in Go (replaces ES-side composite+painless aggregation).
	sampleGroups := groupMetricSamplesByLabels(flatResult.Samples)

	vectors := make([]promVectorSample, 0, len(sampleGroups))
	for _, sg := range sampleGroups {
		if len(sg.Samples) < 2 {
			continue
		}

		// Reconstruct Prometheus metric name with _sum/_bucket suffix if applicable.
		name := expr.MetricName
		if expr.HistogramSub != "" {
			name = expr.BaseMetric + "_" + expr.HistogramSub
		}
		m := promMetric{PromLabelName: name}
		for k, v := range sg.Labels {
			// Translate ES dot-format keys back to PromQL underscore format
			// (e.g. "span.name" → "span_name") so that downstream GroupBy
			// and aggregation logic uses the same format as the PromQL query.
			m[translateLabelToPromQL(k)] = v
		}

		val := computeRateAtTime(sg.Samples, evalTime, expr.RangeDuration, expr.Function)
		if !math.IsNaN(val) {
			vectors = append(vectors, promVectorSample{
				Metric: m,
				Value:  []any{float64(evalTime.UnixMilli()) / 1000.0, formatPromValue(val)},
			})
		}
	}

	return vectors
}

// execHistogramQuantileInstant handles histogram_quantile(θ, rate(_bucket[...]))
// by querying ES histogram documents via QueryFlat, grouping by labels in Go,
// aggregating bucket_counts across time, and computing quantiles via linear interpolation.
func (e *Extension) execHistogramQuantileInstant(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) []promVectorSample {
	lookbackStart := evalTime.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		LabelMatch: labelMatch,
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: evalTime},
	}

	result, err := e.concurrentQueryFlat(r.Context(), flatQuery, e.logger)
	if err != nil {
		e.logger.Error("histogram_quantile query_flat failed", zap.Error(err))
		return nil
	}
	if result == nil || len(result.Samples) == 0 {
		return nil
	}

	e.checkFlatTruncation(result)

	// Group samples by labels in Go (replaces ES-side composite+painless script).
	groups := groupSamplesByLabels(result.Samples)

	buckets := make([]HistogramBucket, 0, len(groups))
	for _, samples := range groups {
		hb := HistogramBucket{}
		// Extract labels from first sample.
		if len(samples) > 0 {
			hb.Labels = copyMap(samples[0].Labels)
		}

		// Find bounds from first sample that has them, then accumulate.
		for _, sample := range samples {
			if len(sample.Bounds) > 0 && len(hb.Bounds) == 0 {
				hb.Bounds = sample.Bounds
				hb.BucketCounts = make([]int64, len(sample.Bounds))
			}
		}
		if len(hb.Bounds) == 0 {
			continue
		}

		for _, sample := range samples {
			hb.TotalSum += sample.Value
			for i := 0; i < len(sample.BucketCounts) && i < len(hb.BucketCounts); i++ {
				hb.BucketCounts[i] += sample.BucketCounts[i]
				hb.TotalCount += sample.BucketCounts[i]
			}
		}

		if hb.TotalCount > 0 {
			buckets = append(buckets, hb)
		}
	}

	if len(buckets) == 0 {
		return nil
	}

	aggregated := AggregateHistogramBuckets(buckets)

	vectors := make([]promVectorSample, 0, len(aggregated))
	for _, hb := range aggregated {
		q := ComputeHistogramQuantile(expr.Quantile, hb)
		m := promMetric{PromLabelName: expr.MetricName}
		for k, v := range hb.Labels {
			if k == PromLabelLe {
				continue
			}
			// Translate ES dot-format keys back to PromQL underscore format.
			m[translateLabelToPromQL(k)] = v
		}
		vectors = append(vectors, promVectorSample{
			Metric: m,
			Value:  []any{float64(evalTime.UnixMilli()) / 1000.0, formatPromValue(q)},
		})
	}

	return vectors
}

// execHistogramQuantileRange handles histogram_quantile(θ, rate(_bucket[...]))
// range queries by computing quantile at each step from a sliding window.
func (e *Extension) execHistogramQuantileRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	lookbackStart := start.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		LabelMatch: labelMatch,
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	result, err := e.concurrentQueryFlat(r.Context(), flatQuery, e.logger)
	if err != nil {
		e.logger.Error("histogram_quantile range query_flat failed", zap.Error(err))
		return nil
	}
	if result == nil || len(result.Samples) == 0 {
		return nil
	}

	e.checkFlatTruncation(result)

	// Group samples by labels in Go (replaces ES-side composite+painless script).
	groups := groupSamplesByLabels(result.Samples)

	stepMs := step.Milliseconds()
	rangeMs := expr.RangeDuration.Milliseconds()

	// Build matrix: one series per label group, values at each step.
	matrix := make([]promMatrixSample, 0, len(groups))
	for _, histSamples := range groups {
		values := make([][]any, 0)
		for t := start.UnixMilli(); t <= end.UnixMilli(); t += stepMs {
			windowStart := t - rangeMs

			// Aggregate bucket_counts from samples within [windowStart, t].
			hb := AggregateHistogramSamples(histSamples, windowStart, t)
			if hb.TotalCount == 0 {
				continue
			}
			q := ComputeHistogramQuantile(expr.Quantile, hb)
			values = append(values, []any{
				float64(t) / 1000.0,
				formatPromValue(q),
			})
		}

		if len(values) == 0 {
			continue
		}

		m := promMetric{PromLabelName: expr.MetricName}
		if len(histSamples) > 0 {
			for k, v := range histSamples[0].Labels {
				if k == PromLabelLe {
					continue
				}
				// Translate ES dot-format keys back to PromQL underscore format.
				m[translateLabelToPromQL(k)] = v
			}
		}
		matrix = append(matrix, promMatrixSample{Metric: m, Values: values})
	}

	if len(matrix) == 0 {
		return nil
	}

	return &promQueryData{
		ResultType: ResultTypeMatrix,
		Result:     matrix,
	}
}

// groupSamplesByLabels groups MetricSample values by their Labels map,
// converting to HistogramSample for the histogram_quantile pipeline.
// Used by histogram_quantile to replace ES-side composite+painless script grouping.
// Returns map keyed by sorted label string (compatible with histogram_calc.sortedLabelKey).
func groupSamplesByLabels(samples []observabilitystorageext.MetricSample) map[string][]HistogramSample {
	groups := make(map[string][]HistogramSample)
	for _, s := range samples {
		key := sortedLabelKey(s.Labels)
		groups[key] = append(groups[key], HistogramSample{
			TimestampMs:  s.TimestampMs,
			Value:        s.Value,
			BucketCounts: s.BucketCounts,
			Bounds:       s.Bounds,
			Labels:       s.Labels,
		})
	}
	return groups
}

// metricSampleGroup holds samples grouped by labels, keeping MetricSample type
// for use by computeRate (which operates on MetricSample directly).
type metricSampleGroup struct {
	Labels  map[string]string
	Samples []observabilitystorageext.MetricSample
}

// groupMetricSamplesByLabels groups MetricSample values by their Labels map,
// preserving MetricSample type. Used by execRateRange to replace ES-side
// composite aggregation with Go-side grouping.
func groupMetricSamplesByLabels(samples []observabilitystorageext.MetricSample) []metricSampleGroup {
	index := make(map[string]int)
	var order []string
	var groups []metricSampleGroup
	for _, s := range samples {
		key := sortedLabelKey(s.Labels)
		if idx, ok := index[key]; ok {
			groups[idx].Samples = append(groups[idx].Samples, s)
		} else {
			index[key] = len(groups)
			order = append(order, key)
			groups = append(groups, metricSampleGroup{
				Labels:  s.Labels,
				Samples: []observabilitystorageext.MetricSample{s},
			})
		}
	}
	return groups
}

// checkFlatTruncation logs a warning if QueryFlat returned fewer documents
// than matched in ES (indicating data was truncated by MaxDocs limit).
func (e *Extension) checkFlatTruncation(result *observabilitystorageext.MetricFlatResult) {
	if result.Total > int64(len(result.Samples)) {
		e.logger.Warn("QueryFlat data truncated by MaxDocs limit",
			zap.Int64("total_matching_in_es", result.Total),
			zap.Int("returned", len(result.Samples)),
		)
	}
}

// computeRate computes rate/increase/irate at each step point.
func (e *Extension) computeRate(samples []observabilitystorageext.MetricSample, start, end time.Time, step time.Duration, expr *promqlExpr) [][]any {
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	stepMs := step.Milliseconds()
	rangeMs := expr.RangeDuration.Milliseconds()

	result := make([][]any, 0)
	for t := startMs; t <= endMs; t += stepMs {
		windowStart := t - rangeMs
		val := computeRateInWindow(samples, windowStart, t, expr.Function)
		if !math.IsNaN(val) {
			result = append(result, []any{
				float64(t) / 1000.0,
				formatPromValue(val),
			})
		}
	}
	return result
}

// computeRateInWindow computes the rate/increase within a time window.
//
// Handles counter resets (e.g. service restart → counter rolls back to 0)
// using the same algorithm as Prometheus rate(): when a sample value drops
// below the previous sample, the pre-reset value is accumulated as compensation.
func computeRateInWindow(samples []observabilitystorageext.MetricSample, windowStart, windowEnd int64, fn string) float64 {
	if fn == FnIrate {
		// irate: use the last two samples in the window
		return computeIRate(samples, windowStart, windowEnd)
	}

	// For rate and increase, use first and last samples in the window.
	firstIdx, lastIdx := -1, -1
	for i, s := range samples {
		if s.TimestampMs >= windowStart && s.TimestampMs <= windowEnd {
			if firstIdx == -1 {
				firstIdx = i
			}
			lastIdx = i
		}
	}

	if firstIdx < 0 || lastIdx < 0 || firstIdx >= lastIdx {
		return math.NaN()
	}

	increase := counterIncrease(samples, firstIdx, lastIdx)

	if fn == FnIncrease {
		return increase
	}

	// rate: increase / duration in seconds
	durationSec := float64(samples[lastIdx].TimestampMs-samples[firstIdx].TimestampMs) / 1000.0
	if durationSec <= 0 {
		return math.NaN()
	}
	return increase / durationSec
}

// counterIncrease computes the total increase of a counter metric between
// samples[firstIdx] and samples[lastIdx], compensating for counter resets.
//
// When a counter resets (value drops), the pre-reset total is added back.
// Algorithm is consistent with Prometheus rate() counter reset detection:
// for each pair (samples[i-1], samples[i]), if value decreases, treat as
// reset and add back the pre-reset accumulated value.
func counterIncrease(samples []observabilitystorageext.MetricSample, firstIdx, lastIdx int) float64 {
	delta := samples[lastIdx].Value - samples[firstIdx].Value

	for i := firstIdx + 1; i <= lastIdx; i++ {
		if samples[i].Value < samples[i-1].Value {
			// Counter reset detected: add back the value before reset.
			delta += samples[i-1].Value
		}
	}

	// After compensation, a true counter should be non-negative.
	// If still negative, the data is genuinely broken; clamp to 0.
	if delta < 0 {
		return 0
	}
	return delta
}

// computeIRate computes the instant rate using the last two samples in the window.
func computeIRate(samples []observabilitystorageext.MetricSample, windowStart, windowEnd int64) float64 {
	var prev, last *observabilitystorageext.MetricSample
	for i := range samples {
		if samples[i].TimestampMs >= windowStart && samples[i].TimestampMs <= windowEnd {
			prev = last
			cp := samples[i]
			last = &cp
		}
	}
	if prev == nil || last == nil {
		return math.NaN()
	}
	durationSec := float64(last.TimestampMs-prev.TimestampMs) / 1000.0
	if durationSec <= 0 {
		return math.NaN()
	}
	return (last.Value - prev.Value) / durationSec
}

// computeRateAtTime computes the rate/increase at a single time point.
func computeRateAtTime(samples []observabilitystorageext.MetricSample, evalTime time.Time, rangeDuration time.Duration, fn string) float64 {
	windowStart := evalTime.Add(-rangeDuration).UnixMilli()
	windowEnd := evalTime.UnixMilli()
	return computeRateInWindow(samples, windowStart, windowEnd, fn)
}

// applyTopK sorts vectors by value and returns the top K (or bottom K).
// When isBottomK is true, returns the K smallest values.
// If k >= len(vectors), returns all vectors (no-op).
// Sorting is stable to ensure deterministic results for equal values.
func applyTopK(k int, isBottomK bool, vectors []promVectorSample) []promVectorSample {
	if k <= 0 || len(vectors) == 0 {
		return vectors
	}
	if k >= len(vectors) {
		return vectors
	}

	// Sort by value descending (topk) or ascending (bottomk).
	// Use sort.SliceStable for deterministic ordering on ties.
	sort.SliceStable(vectors, func(i, j int) bool {
		vi := parseVectorValue(vectors[i])
		vj := parseVectorValue(vectors[j])
		if isBottomK {
			return vi < vj
		}
		return vi > vj
	})

	return vectors[:k]
}

// parseVectorValue extracts the float64 value from a promVectorSample's Value field.
func parseVectorValue(v promVectorSample) float64 {
	if len(v.Value) < 2 {
		return 0
	}
	if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
		return f
	}
	return 0
}

// ── Aggregation helpers ─────────────────────────────

func applyAggregation(fn string, groupBy []string, vectors []promVectorSample) []promVectorSample {
	if len(groupBy) == 0 {
		// Aggregate all into one group
		return []promVectorSample{aggregateGroup(fn, vectors)}
	}

	// Group by the specified labels
	groups := make(map[string][]promVectorSample)
	for _, v := range vectors {
		key := groupKey(v.Metric, groupBy)
		groups[key] = append(groups[key], v)
	}

	result := make([]promVectorSample, 0, len(groups))
	for _, group := range groups {
		agg := aggregateGroup(fn, group)
		// Restore groupBy labels from the first vector in the group so that
		// stripMetricToGroupBy can correctly retain the grouping dimensions.
		if len(group) > 0 {
			agg.Metric = filterMetricByKeys(group[0].Metric, groupBy)
		}
		result = append(result, agg)
	}
	return result
}

// filterMetricByKeys returns a new promMetric containing only the specified keys.
func filterMetricByKeys(m promMetric, keys []string) promMetric {
	filtered := make(promMetric, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			filtered[k] = v
		}
	}
	return filtered
}

func groupKey(m promMetric, groupBy []string) string {
	parts := make([]string, 0, len(groupBy))
	for _, k := range groupBy {
		if v, ok := m[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

func aggregateGroup(fn string, vectors []promVectorSample) promVectorSample {
	if len(vectors) == 0 {
		return promVectorSample{}
	}

	val := 0.0
	switch fn {
	case AggSum:
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				val += f
			}
		}
	case AggAvg:
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				val += f
			}
		}
		if len(vectors) > 0 {
			val /= float64(len(vectors))
		}
	case AggMax:
		val = math.Inf(-1)
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				if f > val {
					val = f
				}
			}
		}
	case AggMin:
		val = math.Inf(1)
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				if f < val {
					val = f
				}
			}
		}
	case AggCount:
		val = float64(len(vectors))
	}

	return promVectorSample{
		Metric: promMetric{},
		Value:  []any{vectors[0].Value[0], formatPromValue(val)},
	}
}

// buildSeriesMetric constructs the metric label set for a series in the response.
// Standard Prometheus behavior: when an aggregation with groupBy is applied,
// the result metric only contains the groupBy labels (no __name__).
// Without aggregation, it includes __name__ and all original labels.
func buildSeriesMetric(expr *promqlExpr, seriesLabels map[string]string) promMetric {
	if expr.Aggregation != "" && len(expr.GroupBy) > 0 {
		// Aggregated result: only retain groupBy labels
		m := make(promMetric, len(expr.GroupBy))
		for _, k := range expr.GroupBy {
			// seriesLabels keys are ES dot format (e.g. "span.name").
			// GroupBy keys are PromQL underscore format (e.g. "span_name").
			// Translate GroupBy to dot format to look up the value, but use
			// the original underscore key for the Prometheus response.
			esKey := prometheusToOtelLabelKeys[k]
			if esKey == "" {
				esKey = k
			}
			if v, ok := seriesLabels[esKey]; ok {
				m[k] = v
			} else if v, ok := seriesLabels[k]; ok {
				m[k] = v
			}
		}
		return m
	}
	// Non-aggregated: include __name__ and all labels
	m := promMetric{PromLabelName: expr.MetricName}
	for k, v := range seriesLabels {
		m[translateLabelToPromQL(k)] = v
	}
	return m
}

// stripMetricToGroupBy removes all labels except groupBy from vector samples.
// This implements standard Prometheus behavior where aggregation results
// only retain the labels specified in the by() clause.
func stripMetricToGroupBy(vectors []promVectorSample, groupBy []string) {
	if len(groupBy) == 0 {
		return
	}
	groupBySet := make(map[string]struct{}, len(groupBy))
	for _, k := range groupBy {
		groupBySet[k] = struct{}{}
	}
	for i := range vectors {
		filtered := make(promMetric, len(groupBy))
		for k, v := range vectors[i].Metric {
			if _, ok := groupBySet[k]; ok {
				filtered[k] = v
			}
		}
		vectors[i].Metric = filtered
	}
}

// stripMatrixMetricToGroupBy removes all labels except groupBy from matrix samples.
func stripMatrixMetricToGroupBy(matrix []promMatrixSample, groupBy []string) {
	if len(groupBy) == 0 {
		return
	}
	groupBySet := make(map[string]struct{}, len(groupBy))
	for _, k := range groupBy {
		groupBySet[k] = struct{}{}
	}
	for i := range matrix {
		filtered := make(promMetric, len(groupBy))
		for k, v := range matrix[i].Metric {
			if _, ok := groupBySet[k]; ok {
				filtered[k] = v
			}
		}
		matrix[i].Metric = filtered
	}
}

// aggregateMatrix performs groupBy aggregation on matrix samples.
func aggregateMatrix(fn string, groupBy []string, matrix []promMatrixSample) []promMatrixSample {
	if len(groupBy) == 0 {
		return matrix
	}

	// Group matrix by groupBy labels
	groups := make(map[string][]promMatrixSample)
	for _, m := range matrix {
		key := groupKey(promMetric(m.Metric), groupBy)
		groups[key] = append(groups[key], m)
	}

	// Need timestamp alignment for matrix aggregation.
	// Simple approach: only aggregate if there's exactly one sample, otherwise pass through.
	result := make([]promMatrixSample, 0)
	for _, group := range groups {
		for _, m := range group {
			result = append(result, m)
		}
	}
	return result
}

// parseHistogramQuantileWrapper extracts histogram_quantile(θ, inner_expr).
// Returns (inner_expression, θ). Returns ("", 0) if not a match.
func parseHistogramQuantileWrapper(s string) (inner string, theta float64) {
	const prefix = AggHistogramQuantile + "("
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, prefix) {
		return "", 0
	}

	content := strings.TrimSpace(s[len(prefix):])

	// Extract θ: the float before the first comma at depth 0.
	depth := 0
	commaIdx := -1
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				commaIdx = i
				goto found
			}
		}
	}
found:
	if commaIdx < 0 {
		return "", 0
	}

	thetaStr := strings.TrimSpace(content[:commaIdx])
	t, err := strconv.ParseFloat(thetaStr, 64)
	if err != nil {
		return "", 0
	}

	// Extract inner expression (strip trailing closing paren).
	innerExpr := strings.TrimSpace(content[commaIdx+1:])
	if len(innerExpr) > 0 && innerExpr[len(innerExpr)-1] == ')' {
		innerExpr = strings.TrimSpace(innerExpr[:len(innerExpr)-1])
	}

	return innerExpr, t
}

// parseTopKWrapper extracts the inner expression from topk(N, ...) or bottomk(N, ...).
// Returns (inner expression, K, isBottomK).
// Returns ("", 0, false) if the input is not a topk/bottomk wrapper, or if K is invalid.
func parseTopKWrapper(s string) (inner string, k int, isBottomK bool) {
	lower := strings.ToLower(s)

	isBottomK = false
	const topkPrefix = AggTopK + "("
	const bottomkPrefix = AggBottomK + "("

	prefix := topkPrefix
	if strings.HasPrefix(lower, topkPrefix) {
		// isBottomK stays false
	} else if strings.HasPrefix(lower, bottomkPrefix) {
		prefix = bottomkPrefix
		isBottomK = true
	} else {
		return "", 0, false
	}

	content := strings.TrimSpace(s[len(prefix):])

	// Extract K: the integer before the first comma at depth 0.
	depth := 0
	commaIdx := -1
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				commaIdx = i
				goto foundComma
			}
		}
	}
foundComma:
	if commaIdx < 0 {
		return "", 0, false
	}

	kStr := strings.TrimSpace(content[:commaIdx])
	parsedK, err := strconv.Atoi(kStr)
	if err != nil || parsedK <= 0 {
		return "", 0, false
	}

	// Extract inner expression: strip trailing closing paren after K.
	innerExpr := strings.TrimSpace(content[commaIdx+1:])
	if len(innerExpr) > 0 && innerExpr[len(innerExpr)-1] == ')' {
		innerExpr = strings.TrimSpace(innerExpr[:len(innerExpr)-1])
	}

	return innerExpr, parsedK, isBottomK
}

// ── PromQL Parser (simple subset) ──────────────────

// parsePromQL parses a PromQL expression string.
// Supported patterns:
//
//	metric_name
//	metric_name{label1="val", label2=~"regex"}
//	sum(metric_name{...}) by (label1, label2)
//	rate(metric_name{...}[5m])
//	sum(rate(metric_name{...}[5m])) by (label1, label2)
//	histogram_quantile(0.95, sum(rate(metric_bucket{...}[5m])) by (le))
//	topk(5, sum(rate(metric{...}[30m])) by (label))
//	bottomk(5, sum(rate(metric{...}[30m])) by (label))
//
// Histogram_quantile and topk/bottomk use recursive parsing so they can
// combine with any valid inner expression.
func parsePromQL(s string) (*promqlExpr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errInvalidPromQL("empty expression")
	}

	expr := &promqlExpr{}

	// Check for histogram_quantile(θ, ...) wrapper before aggregation.
	// This is a special two-arg function where the first arg is a float quantile
	// and the second is the inner expression.
	// Uses recursive parsing so that the inner expression is fully parsed
	// (e.g. histogram_quantile(0.95, sum(rate(x_bucket[5m])) by (le)))
	// and the outer wrapper is applied on top.
	if inner, theta := parseHistogramQuantileWrapper(s); inner != "" {
		expr, err := parsePromQL(inner)
		if err != nil {
			return nil, err
		}
		expr.Aggregation = AggHistogramQuantile
		expr.Quantile = theta
		return expr, nil
	}

	// Check for topk(N, ...) / bottomk(N, ...) wrapper.
	// Strips the outer wrapper and recursively parses the inner expression
	// so that topk can combine with any inner expression including
	// histogram_quantile, aggregation, function, or raw selector.
	if inner, k, isBk := parseTopKWrapper(s); inner != "" {
		expr, err := parsePromQL(inner)
		if err != nil {
			return nil, err
		}
		expr.TopK = k
		expr.IsBottomK = isBk
		return expr, nil
	}

	// Check for aggregation wrapper: sum(...) by (labels)
	if result, rest, agg, groupBy := parseAggWrapper(s); result != "" {
		// Preserve histogram_quantile as top-level aggregation.
		if expr.Aggregation == "" {
			expr.Aggregation = agg
		}
		expr.GroupBy = groupBy
		s = result // continue parsing inner expression
		if rest != "" {
			s = rest
		}
	}

	// Check for function wrapper: rate(...), increase(...), irate(...)
	if fn, inner, dur := parseFuncWrapper(s); fn != "" {
		expr.Function = fn
		expr.RangeDuration = dur
		s = inner
	}

	// Parse selector: metric_name{labels}
	name, labels, labelMatch, err := parseSelector(s)
	if err != nil {
		return nil, err
	}
	// Histogram sub-series detection: _sum and _bucket suffixes.
	// Prometheus exposes histograms as separate time series:
	//   traces_service_graph_request_server_seconds_sum
	//   traces_service_graph_request_server_seconds_bucket{le="0.005"}
	// ES stores the base metric name, so we strip the suffix and query the
	// underlying histogram data.
	expr.MetricName = name
	if sub, ok := detectHistogramSub(name); ok {
		expr.HistogramSub = sub
		expr.BaseMetric = name
		expr.MetricName = stripHistogramSuffix(name)
	}

	expr.Labels = labels
	expr.LabelMatch = labelMatch

	// Grafana Explore Metrics compatibility: when no explicit `by` clause
	// was found but the selector has the "__ignore_usage__" internal label
	// and all remaining label values equal the metric name, those labels
	// are intended as grouping dimensions, not filters.
	// e.g. avg({"span.name"="traces.spanmetrics.calls", __ignore_usage__="", "traces.spanmetrics.calls"})
	//   → avg by (span.name) (traces.spanmetrics.calls)
	exploreMetricsGroupByLabels(expr)

	return expr, nil
}

// exploreMetricsGroupByLabels detects the Grafana Explore Metrics selector pattern
// and converts labels that match the metric name into groupBy dimensions.
//
// Detection conditions (all must be true):
//  1. expr has an aggregation (sum/avg/max/min/count) but no groupBy yet
//  2. __ignore_usage__ label is present (Grafana-specific internal marker)
//  3. ALL remaining non-__ labels have values identical to the metric name
//
// This is zero-risk for normal PromQL because:
//   - Condition 2 requires __ignore_usage__, which only Grafana injects
//   - In standard PromQL, a filter value equalling the metric name has no semantic meaning
func exploreMetricsGroupByLabels(expr *promqlExpr) {
	if expr.Aggregation == "" || len(expr.GroupBy) > 0 {
		return // already has explicit groupBy, or no aggregation at all
	}
	if len(expr.Labels) == 0 {
		return
	}

	// Condition: __ignore_usage__ must be present (Grafana-only marker)
	if _, ok := expr.Labels[PromLabelIgnoreUsage]; !ok {
		return
	}

	// Collect label keys whose value matches the metric name
	var groupBy []string
	allMatch := true
	for k, v := range expr.Labels {
		if strings.HasPrefix(k, PromInternalLabelPrefix) {
			continue // skip internal labels
		}
		if v == expr.MetricName {
			groupBy = append(groupBy, k)
		} else {
			allMatch = false
		}
	}

	// Only activate when ALL non-internal labels match (no mixed filters)
	if allMatch && len(groupBy) > 0 {
		expr.GroupBy = groupBy
		// Remove promoted labels from filters
		for _, k := range groupBy {
			delete(expr.Labels, k)
		}
	}
}

// detectHistogramSub detects Prometheus histogram sub-series suffixes.
// Returns ("sum", true) for _sum suffixes and ("bucket", true) for _bucket suffixes.
//
// Both _sum and _bucket are standard Prometheus histogram sub-series suffixes.
// When detected, the suffix is stripped so the ES query uses the base metric name
// (e.g. "traces_service_graph_request_server_seconds").
//
// For histogram_quantile queries, only the _bucket suffix is relevant because
// quantile computation requires bucket data.
func detectHistogramSub(name string) (string, bool) {
	if strings.HasSuffix(name, HistogramSuffixSum) {
		return HistogramSubSum, true
	}
	if strings.HasSuffix(name, HistogramSuffixBucket) {
		return HistogramSubBucket, true
	}
	return "", false
}

// stripHistogramSuffix removes the Prometheus histogram sub-series suffix.
func stripHistogramSuffix(name string) string {
	if strings.HasSuffix(name, HistogramSuffixSum) {
		return name[:len(name)-len(HistogramSuffixSum)]
	}
	if strings.HasSuffix(name, HistogramSuffixBucket) {
		return name[:len(name)-len(HistogramSuffixBucket)]
	}
	return name
}

// resolveHistogramBucket returns the bucket count for the le label from the
// PromQL query. It matches the le value against the histogram's explicit_bounds
// and returns the corresponding bucket_counts entry.
func resolveHistogramBucket(dp observabilitystorageext.MetricDataPoint, expr *promqlExpr) float64 {
	leStr, ok := expr.Labels[PromLabelLe]
	if !ok {
		return dp.Value
	}
	leVal, err := strconv.ParseFloat(leStr, 64)
	if err != nil {
		return 0
	}

	for i, bound := range dp.ExplicitBounds {
		if math.Abs(leVal-bound) < 1e-9 && i < len(dp.BucketCounts) {
			return float64(dp.BucketCounts[i])
		}
	}
	return 0
}

// parsePromQLSelector parses a simple selector (no aggregation, no functions).
func parsePromQLSelector(s string) (*promqlExpr, error) {
	s = strings.TrimSpace(s)
	return parsePromQL(s)
}

// parseAggWrapper parses aggregation wrappers.
// Supports both formats:
//
//	avg(selector) by (label1, label2)     — PromQL standard
//	avg by (label1, label2) (selector)     — Grafana Explore Metrics
//
// Returns (inner_expression, rest, aggregation_func, groupBy_labels).
func parseAggWrapper(s string) (inner, rest, agg string, groupBy []string) {
	for _, fn := range AggFuncs {
		lower := strings.ToLower(s)

		// Pattern 1: fn by (...) (selector) — Grafana Explore Metrics style
		fnByPrefix := fn + " by ("
		if strings.HasPrefix(lower, fnByPrefix) {
			j := strings.IndexByte(s[len(fnByPrefix):], ')')
			if j > 0 {
				groupBy = parseLabelList(s[len(fnByPrefix) : len(fnByPrefix)+j])
				remainder := strings.TrimSpace(s[len(fnByPrefix)+j+1:])
				// Strip outer grouping parens: "({selector})" → "{selector}"
				if strings.HasPrefix(remainder, "(") && strings.HasSuffix(remainder, ")") {
					remainder = strings.TrimSpace(remainder[1 : len(remainder)-1])
				}
				return remainder, "", fn, groupBy
			}
			continue
		}

		// Pattern 2: fn(selector) [by (labels)] — standard PromQL
		prefix := fn + "("
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		// Find matching closing paren
		depth := 1
		i := len(prefix)
		for ; i < len(s) && depth > 0; i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return "", "", "", nil
		}
		inner = s[len(prefix) : i-1]
		rest = strings.TrimSpace(s[i:])

		// Check for "by (label1, label2)" or "without (label1, label2)"
		if strings.HasPrefix(strings.ToLower(rest), "by") {
			rest = strings.TrimSpace(rest[2:])
			if strings.HasPrefix(rest, "(") {
				j := strings.IndexByte(rest, ')')
				if j > 0 {
					groupBy = parseLabelList(rest[1:j])
					rest = strings.TrimSpace(rest[j+1:])
				}
			}
		}
		return inner, rest, fn, groupBy
	}
	return "", "", "", nil
}

// parseFuncWrapper parses function wrappers: rate(xxx[5m]), increase(xxx[5m]).
// Returns (function_name, inner_selector, duration).
func parseFuncWrapper(s string) (fn, inner string, dur time.Duration) {
	funcs := []string{FnRate, FnIncrease, FnIrate}
	for _, f := range funcs {
		prefix := f + "("
		if !strings.HasPrefix(strings.ToLower(s), prefix) {
			continue
		}
		// Find matching closing paren
		depth := 1
		i := len(prefix)
		for ; i < len(s) && depth > 0; i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return "", "", 0
		}
		content := s[len(prefix) : i-1]

		// Extract [duration] from the end
		bracketStart := strings.LastIndexByte(content, '[')
		if bracketStart < 0 {
			return f, content, 0
		}
		bracketEnd := strings.IndexByte(content[bracketStart:], ']')
		if bracketEnd < 0 {
			return f, content, 0
		}

		inner = strings.TrimSpace(content[:bracketStart])
		durStr := strings.TrimSpace(content[bracketStart+1 : bracketStart+bracketEnd])
		d, err := parsePrometheusDuration(durStr)
		if err != nil {
			return f, inner, 0
		}
		return f, inner, d
	}
	return "", "", 0
}

// parseSelector parses metric_name{key="val", key=~"regex"}.
// Also handles Grafana Explore Metrics format: {"metric_name", label="val"}
// where a bare quoted string inside braces is the metric name.
func parseSelector(s string) (name string, labels map[string]string, labelMatch map[string]string, err error) {
	s = strings.TrimSpace(s)

	// Find label block
	braceIdx := strings.IndexByte(s, '{')
	if braceIdx < 0 {
		return s, nil, nil, nil
	}

	name = strings.TrimSpace(s[:braceIdx])
	closeBrace := strings.LastIndexByte(s, '}')
	if closeBrace < 0 {
		return "", nil, nil, errInvalidPromQL("unclosed brace in selector")
	}

	labels = make(map[string]string)
	labelMatch = make(map[string]string)

	// Parse label pairs: key="value", key=~"regex"
	labelStr := s[braceIdx+1 : closeBrace]
	pairs := splitLabelPairs(labelStr)
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		// Split by = (handles = and =~)
		eqIdx := strings.Index(pair, "=")
		if eqIdx < 0 {
			// No = found: bare quoted string → treat as metric name
			// Grafana Explore Metrics sends: {"traces.spanmetrics.calls", label="val"}
			val := strings.Trim(pair, `"'`)
			if val != "" {
				name = val
			}
			continue
		}

		key := strings.TrimSpace(pair[:eqIdx])
		value := strings.TrimSpace(pair[eqIdx+1:])
		op := "="

		// Check for =~ (regex match) — before quote-stripping key
		if strings.HasSuffix(key, "~") {
			key = strings.TrimSpace(key[:len(key)-1])
			op = "=~"
		}
		// Check for != and !~
		if strings.HasPrefix(value, "~") {
			op = "=~"
			value = strings.TrimSpace(value[1:])
		}

		// Strip quotes from both key and value.
		// Grafana Builder mode quotes label names containing dots, e.g.:
		//   {"traces.spanmetrics.calls", "status.code"="STATUS_CODE_UNSET"}
		key = strings.Trim(key, `"'`)
		value = strings.Trim(value, `"'`)

		if op == "=~" {
			labelMatch[key] = value
		} else {
			labels[key] = value
		}
	}

	return name, labels, labelMatch, nil
}

// splitLabelPairs splits a comma-separated label string respecting quotes.
func splitLabelPairs(s string) []string {
	var pairs []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' || ch == '\'' {
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
			}
		}
		if ch == ',' && !inQuote {
			pairs = append(pairs, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		pairs = append(pairs, current.String())
	}
	return pairs
}

// parseLabelList parses a comma-separated label list inside parens.
func parseLabelList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// Strip optional quotes
		p = strings.Trim(p, "\"'")
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// ── Response writers ────────────────────────────────

func (e *Extension) writePromSuccess(w http.ResponseWriter, data any) {
	resp := promResponse{
		Status: "success",
		Data:   data,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (e *Extension) writePromSuccessLabelList(w http.ResponseWriter, values []string) {
	if values == nil {
		values = []string{}
	}
	resp := promResponse{
		Status: "success",
		Data:   values,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (e *Extension) writePromError(w http.ResponseWriter, errorType, message string) {
	resp := promResponse{
		Status:    "error",
		ErrorType: errorType,
		Error:     message,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Choose appropriate HTTP status code
	status := http.StatusBadRequest
	switch errorType {
	case "service_unavailable":
		status = http.StatusServiceUnavailable
	case "execution":
		status = http.StatusUnprocessableEntity
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// ── Time parsing helpers ───────────────────────────

// parsePrometheusTime parses a Prometheus time parameter.
// Supports RFC3339 and Unix timestamp (with optional decimals).
func parsePrometheusTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	// Try Unix timestamp (seconds, possibly with decimals)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec), nil
	}
	return time.Time{}, errInvalidPromQL("cannot parse time: " + s)
}

// parsePrometheusDuration parses a Prometheus duration string.
// Supports both Go-style durations ("5m", "30s", "1h") and bare float seconds
// ("15", "30.5"). Bare numbers are common when Grafana sends step derived from intervalMs.
func parsePrometheusDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	// Try Go duration format first: "15s", "5m", "1h30m"
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Fallback: bare float seconds (e.g., "15", "30.5")
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("cannot parse duration: %q", s)
}

// ── Utility helpers ────────────────────────────────

// getQueryParam gets a query parameter from either query string or form body.
func (e *Extension) getQueryParam(r *http.Request, key string) string {
	if err := r.ParseForm(); err != nil {
		return ""
	}
	if v := r.FormValue(key); v != "" {
		return v
	}
	return r.URL.Query().Get(key)
}

// parseTimeUnixMilli parses a UnixMilli string to int64.
func parseTimeUnixMilli(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// formatPromValue formats a float64 for Prometheus JSON output.
// NaN, Inf, -Inf are represented as quoted strings "NaN", "Inf", "-Inf".
func formatPromValue(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// decodePrometheusLabelName decodes a Prometheus URL-encoded label name.
// Prometheus encodes special characters in label names for URL paths:
//
//	U__  — prefix indicating the following name is encoded
//	__   — literal underscore _
//	_xx_ — hex-encoded byte (e.g., _2e_ → '.' , _2f_ → '/')
//
// Examples:
//
//	U__status_2e_code → status.code
//	U__http_2f_requests → http/requests
//	simple_label       → simple_label  (no prefix, no decoding needed)
func decodePrometheusLabelName(encoded string) string {
	if !strings.HasPrefix(encoded, "U__") {
		return encoded
	}
	// Strip U__ prefix
	s := encoded[3:]

	// Build decoded string
	var result strings.Builder
	result.Grow(len(s))

	i := 0
	for i < len(s) {
		if s[i] == '_' && i+3 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) && s[i+3] == '_' {
			// _xx_ → decode hex byte
			b := unhex(s[i+1])<<4 | unhex(s[i+2])
			result.WriteByte(b)
			i += 4
		} else if s[i] == '_' && i+1 < len(s) && s[i+1] == '_' {
			// __ → literal _
			result.WriteByte('_')
			i += 2
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// filterInternalLabels removes Prometheus/Grafana internal labels (prefix "__")
// from the label map. These labels (__name__, __ignore_usage__, etc.) are metadata
// used by the PromQL layer but should not be passed to the storage backend.
func filterInternalLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	filtered := make(map[string]string, len(labels))
	for k, v := range labels {
		if !strings.HasPrefix(k, PromInternalLabelPrefix) {
			filtered[k] = v
		}
	}
	return filtered
}



// ── Error helpers ──────────────────────────────────

type promqlParseError struct {
	msg string
}

func (e *promqlParseError) Error() string {
	return e.msg
}

func errInvalidPromQL(msg string) error {
	return &promqlParseError{msg: msg}
}
