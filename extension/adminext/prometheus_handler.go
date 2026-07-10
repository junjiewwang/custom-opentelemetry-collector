// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
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
		attribute.String("promql.expr", queryStr),
		attribute.String("promql.metric", expr.MetricName),
		attribute.String("promql.aggregation", expr.Aggregation),
		attribute.StringSlice("promql.group_by", expr.GroupBy),
		attribute.String("promql.function", expr.Function),
	)

	labels := mergeLabels(expr.Labels, expr.LabelMatch)
	if result := e.dispatchInstantQuery(r, expr, evalTime, labels); result != nil {
		e.writePromSuccess(w, result)
	}
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
		attribute.String("promql.expr", queryStr),
		attribute.String("promql.metric", expr.MetricName),
		attribute.String("promql.aggregation", expr.Aggregation),
		attribute.StringSlice("promql.group_by", expr.GroupBy),
		attribute.String("promql.function", expr.Function),
	)

	labels := mergeLabels(expr.Labels, expr.LabelMatch)
	if result := e.dispatchRangeQuery(r, expr, start, end, step, labels); result != nil {
		e.writePromSuccess(w, result)
	}
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

	names, err := e.storageMetricReader.ListLabelNames(r.Context(), tr)
	if err != nil {
		e.writePromError(w, "execution", err.Error())
		return
	}

	// Always include __name__
	hasName := false
	for _, n := range names {
		if n == "__name__" {
			hasName = true
			break
		}
	}
	if !hasName {
		names = append(names, "__name__")
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
	if labelName == "__name__" {
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

	values, err := e.storageMetricReader.ListLabelValues(r.Context(), labelName, tr)
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
			m := promMetric{"__name__": expr.MetricName}
			for k, v := range dp.Labels {
				m[k] = v
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

// dispatchInstantQuery routes to the appropriate backend call based on the parsed expression.
func (e *Extension) dispatchInstantQuery(r *http.Request, expr *promqlExpr, evalTime time.Time, labels map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	if expr.Function != "" && expr.RangeDuration > 0 {
		defer span.SetAttributes(attribute.Int("promql.series_count", 0))
		return e.execRateInstant(r, expr, evalTime, labels)
	}

	query := observabilitystorageext.MetricQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		Time:       evalTime,
	}

	// Record the effective ES query labels on the span
	span.SetAttributes(
		attribute.StringSlice("es.labels", flattenLabels(query.Labels)),
	)

	result, err := e.storageMetricReader.Query(r.Context(), query)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("error.type", "query"))
		e.logger.Error("prom instant query failed", zap.Error(err))
		return nil
	}

	span.SetAttributes(attribute.Int("promql.series_count", len(result.Data)))

	vectors := make([]promVectorSample, 0, len(result.Data))
	for _, dp := range result.Data {
		m := promMetric{"__name__": expr.MetricName}
		for k, v := range dp.Labels {
			m[k] = v
		}
		vectors = append(vectors, promVectorSample{
			Metric: m,
			Value:  []any{float64(parseTimeUnixMilli(dp.TimeUnixMilli)) / 1000.0, formatPromValue(dp.Value)},
		})
	}

	if expr.Aggregation != "" {
		vectors = applyAggregation(expr.Aggregation, expr.GroupBy, vectors)
		span.SetAttributes(attribute.Int("promql.aggregated_count", len(vectors)))
		// Standard Prometheus behavior: aggregation results only retain groupBy labels
		stripMetricToGroupBy(vectors, expr.GroupBy)
	}

	return &promQueryData{
		ResultType: "vector",
		Result:     vectors,
	}
}

// dispatchRangeQuery routes to the appropriate backend call based on the parsed expression.
func (e *Extension) dispatchRangeQuery(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	if expr.Function != "" && expr.RangeDuration > 0 {
		// rate/increase/irate path: use QueryRaw to get original samples
		defer span.SetAttributes(attribute.Int("promql.series_count", 0))
		return e.execRateRange(r, expr, start, end, step, labels)
	}

	query := observabilitystorageext.MetricRangeQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		TimeRange:  observabilitystorageext.TimeRange{Start: start, End: end},
		Step:       step,
		Aggregation: expr.Aggregation,
		GroupBy:     expr.GroupBy,
	}
	if query.Aggregation == "" {
		query.Aggregation = "avg"
	}

	// Record the effective ES query labels on the span
	span.SetAttributes(
		attribute.StringSlice("es.labels", flattenLabels(query.Labels)),
		attribute.StringSlice("es.group_by_full", query.GroupBy),
	)

	result, err := e.storageMetricReader.QueryRange(r.Context(), query)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("error.type", "query_range"))
		e.logger.Error("prom range query failed", zap.Error(err))
		return nil
	}

	span.SetAttributes(attribute.Int("promql.series_count", len(result.Data)))

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
		ResultType: "matrix",
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
func (e *Extension) execRateRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels map[string]string) *promQueryData {
	// Extend time range back by the range duration for lookback.
	lookbackStart := start.Add(-expr.RangeDuration)

	rawQuery := observabilitystorageext.MetricRawQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	rawSeries, err := e.storageMetricReader.QueryRaw(r.Context(), rawQuery)
	if err != nil {
		e.logger.Error("prom query_raw failed", zap.Error(err))
		return nil
	}

	matrix := make([]promMatrixSample, 0, len(rawSeries))
	for _, s := range rawSeries {
		if len(s.Samples) < 2 {
			continue
		}

		m := promMetric{"__name__": expr.MetricName}
		for k, v := range s.Labels {
			m[k] = v
		}

		values := e.computeRate(s.Samples, start, end, step, expr)
		matrix = append(matrix, promMatrixSample{
			Metric: m,
			Values: values,
		})
	}

	if expr.Aggregation != "" && len(expr.GroupBy) > 0 {
		// Post-process aggregation on the computed rate values
		// Simple approach: group by labels and aggregate per timestamp
		matrix = aggregateMatrix(expr.Aggregation, expr.GroupBy, matrix)
		// Standard Prometheus behavior: aggregation results only retain groupBy labels
		stripMatrixMetricToGroupBy(matrix, expr.GroupBy)
	}

	return &promQueryData{
		ResultType: "matrix",
		Result:     matrix,
	}
}

// execRateInstant handles rate/increase/irate instant queries.
func (e *Extension) execRateInstant(r *http.Request, expr *promqlExpr, evalTime time.Time, labels map[string]string) *promQueryData {
	// For instant query, look back from evalTime by the range duration.
	lookbackStart := evalTime.Add(-expr.RangeDuration)

	rawQuery := observabilitystorageext.MetricRawQuery{
		MetricName: expr.MetricName,
		Labels:     filterInternalLabels(labels),
		TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: evalTime},
	}

	rawSeries, err := e.storageMetricReader.QueryRaw(r.Context(), rawQuery)
	if err != nil {
		e.logger.Error("prom query_raw failed", zap.Error(err))
		return nil
	}

	vectors := make([]promVectorSample, 0, len(rawSeries))
	for _, s := range rawSeries {
		if len(s.Samples) < 2 {
			continue
		}

		m := promMetric{"__name__": expr.MetricName}
		for k, v := range s.Labels {
			m[k] = v
		}

		val := computeRateAtTime(s.Samples, evalTime, expr.RangeDuration, expr.Function)
		if !math.IsNaN(val) {
			vectors = append(vectors, promVectorSample{
				Metric: m,
				Value:  []any{float64(evalTime.UnixMilli()) / 1000.0, formatPromValue(val)},
			})
		}
	}

	return &promQueryData{
		ResultType: "vector",
		Result:     vectors,
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
func computeRateInWindow(samples []observabilitystorageext.MetricSample, windowStart, windowEnd int64, fn string) float64 {
	if fn == "irate" {
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

	first := samples[firstIdx]
	last := samples[lastIdx]

	if fn == "increase" {
		return last.Value - first.Value
	}

	// rate: (last - first) / duration in seconds
	durationSec := float64(last.TimestampMs-first.TimestampMs) / 1000.0
	if durationSec <= 0 {
		return math.NaN()
	}
	return (last.Value - first.Value) / durationSec
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
		result = append(result, agg)
	}
	return result
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
	case "sum":
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				val += f
			}
		}
	case "avg":
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				val += f
			}
		}
		if len(vectors) > 0 {
			val /= float64(len(vectors))
		}
	case "max":
		val = math.Inf(-1)
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				if f > val {
					val = f
				}
			}
		}
	case "min":
		val = math.Inf(1)
		for _, v := range vectors {
			if f, ok := strconv.ParseFloat(v.Value[1].(string), 64); ok == nil {
				if f < val {
					val = f
				}
			}
		}
	case "count":
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
			if v, ok := seriesLabels[k]; ok {
				m[k] = v
			}
		}
		return m
	}
	// Non-aggregated: include __name__ and all labels
	m := promMetric{"__name__": expr.MetricName}
	for k, v := range seriesLabels {
		m[k] = v
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

// ── PromQL Parser (simple subset) ──────────────────

// parsePromQL parses a PromQL expression string.
// Supported patterns:
//
//	metric_name
//	metric_name{label1="val", label2=~"regex"}
//	sum(metric_name{...}) by (label1, label2)
//	rate(metric_name{...}[5m])
//	sum(rate(metric_name{...}[5m])) by (label1, label2)
func parsePromQL(s string) (*promqlExpr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errInvalidPromQL("empty expression")
	}

	expr := &promqlExpr{}

	// Check for aggregation wrapper: sum(...) by (labels)
	if result, rest, agg, groupBy := parseAggWrapper(s); result != "" {
		expr.Aggregation = agg
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
	expr.MetricName = name
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
	if _, ok := expr.Labels["__ignore_usage__"]; !ok {
		return
	}

	// Collect label keys whose value matches the metric name
	var groupBy []string
	allMatch := true
	for k, v := range expr.Labels {
		if strings.HasPrefix(k, "__") {
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
	aggFuncs := []string{"sum", "avg", "max", "min", "count"}
	for _, fn := range aggFuncs {
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
	funcs := []string{"rate", "increase", "irate"}
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
		if !strings.HasPrefix(k, "__") {
			filtered[k] = v
		}
	}
	return filtered
}

// mergeLabels merges exact match and regex match labels into a single map.
// Exact matches go to Labels, regex matches go to LabelMatch.
func mergeLabels(labels, labelMatch map[string]string) map[string]string {
	if labels == nil && labelMatch == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range labels {
		result[k] = v
	}
	for k, v := range labelMatch {
		if _, ok := result[k]; !ok {
			result[k] = v
		}
	}
	return result
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
