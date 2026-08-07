// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"slices"
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
	Status    string   `json:"status"`
	Data      any      `json:"data,omitempty"`
	ErrorType string   `json:"errorType,omitempty"`
	Error     string   `json:"error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type promQueryData struct {
	ResultType string `json:"resultType"`
	Result     any    `json:"result"`
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
	LabelNot      map[string]string // not-equal (!=)
	LabelNotMatch map[string]string // not-regex (!~)
	Aggregation   string            // sum, avg, max, min, count, ""
	GroupBy       []string          // by (label1, label2)
	InnerAgg      string            // aggregation nested inside histogram_quantile, "" if none
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
func (h *promHandlers) handlePromQuery(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	queryStr := h.getQueryParam(r, "query")
	if queryStr == "" {
		h.writePromError(w, "bad_data", "parameter 'query' is required")
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
		h.writePromError(w, "bad_data", err.Error())
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

	result := h.dispatchInstantQuery(r, expr, evalTime, expr.Labels, expr.LabelMatch)
	if result == nil {
		result = &promQueryData{ResultType: ResultTypeVector, Result: []promVectorSample{}}
	}
	h.writePromSuccess(w, result)
}

// ── Handler: query_range ───────────────────────────

// handlePromQueryRange handles GET/POST /api/v1/query_range.
func (h *promHandlers) handlePromQueryRange(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	queryStr := h.getQueryParam(r, "query")
	if queryStr == "" {
		h.writePromError(w, "bad_data", "parameter 'query' is required")
		return
	}

	start, err := parsePrometheusTime(r.FormValue("start"))
	if err != nil {
		h.writePromError(w, "bad_data", "invalid 'start' parameter: "+err.Error())
		return
	}
	end, err := parsePrometheusTime(r.FormValue("end"))
	if err != nil {
		h.writePromError(w, "bad_data", "invalid 'end' parameter: "+err.Error())
		return
	}
	step, err := parsePrometheusDuration(r.FormValue("step"))
	if err != nil {
		h.writePromError(w, "bad_data", "invalid 'step' parameter: "+err.Error())
		return
	}

	expr, err := parsePromQL(queryStr)
	if err != nil {
		h.writePromError(w, "bad_data", err.Error())
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

	result := h.dispatchRangeQuery(r, expr, start, end, step, expr.Labels, expr.LabelMatch)
	if result == nil {
		result = &promQueryData{ResultType: ResultTypeMatrix, Result: []promMatrixSample{}}
	}

	// topk/bottomk post-processing. Applied here rather than inside
	// dispatchRangeQuery because that function returns a matrix from several
	// different branches; ranking once at the join covers all of them.
	if expr.TopK > 0 {
		if matrix, ok := result.Result.([]promMatrixSample); ok {
			matrix = applyTopKMatrix(expr.TopK, expr.IsBottomK, matrix)
			result.Result = matrix
			span.SetAttributes(attribute.Int(SpanAttrPromQLTopKCount, len(matrix)))
		}
	}

	h.writePromSuccess(w, result)
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
func (h *promHandlers) handlePromLabels(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
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
				n, err := h.metricReader.ListLabelNames(r.Context(), tr, mn)
				if err != nil {
					h.logger.Debug("list label names skipped",
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
			names, err = h.metricReader.ListLabelNames(r.Context(), tr, "")
			if err != nil {
				h.writePromError(w, "execution", err.Error())
				return
			}
		}
	} else {
		var err error
		names, err = h.metricReader.ListLabelNames(r.Context(), tr, metricName)
		if err != nil {
			h.writePromError(w, "execution", err.Error())
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

	h.writePromSuccessLabelList(w, names)
}

// ── Handler: label values ──────────────────────────

// handlePromLabelValues handles GET /api/v1/label/{labelName}/values.
func (h *promHandlers) handlePromLabelValues(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	labelName := decodePrometheusLabelName(chi.URLParam(r, "labelName"))
	if labelName == "" {
		h.writePromError(w, "bad_data", "parameter 'labelName' is required")
		return
	}

	// Parse explicitly: r.Form is only populated once the request has been
	// parsed, and both branches below read r.Form["match[]"] directly. Relying
	// on an earlier FormValue call to parse as a side effect is what silently
	// emptied match[] in /series.
	if err := r.ParseForm(); err != nil {
		h.writePromError(w, "bad_data", "failed to parse request parameters")
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
		names, err := h.metricReader.ListMetricNames(r.Context(), tr)
		if err != nil {
			h.writePromError(w, "execution", err.Error())
			return
		}
		// Apply match[] __name__ filtering (Prometheus anchored regex / exact),
		// OR'd across selectors. See filterMetricNamesByMatch for semantics.
		names = filterMetricNamesByMatch(names, r.Form["match[]"])
		h.writePromSuccessLabelList(w, names)
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

	// Scope to the metric named by match[], mirroring /labels. Without this the
	// breakdown UI lists values drawn from every metric, so picking one yields
	// an empty panel. Only the __name__ constraint is honoured; other matchers
	// in the selector are not applied.
	metricName := extractMetricNameFromMatch(r.Form["match[]"])

	values, err := h.metricReader.ListLabelValuesForMetric(r.Context(), esLabelName, metricName, tr)
	if err != nil {
		h.writePromError(w, "execution", err.Error())
		return
	}

	h.writePromSuccessLabelList(w, values)
}

// ── Handler: series ────────────────────────────────

// handlePromSeries handles GET/POST /api/v1/series.
func (h *promHandlers) handlePromSeries(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	// r.Form is only populated once the request has been parsed. The other
	// handlers happen to touch FormValue first, which parses as a side effect;
	// this one reads match[] straight away, so parse explicitly. Without it
	// r.Form was always empty and every /series call failed with
	// "parameter 'match[]' is required".
	if err := r.ParseForm(); err != nil {
		h.writePromError(w, "bad_data", "failed to parse request parameters")
		return
	}

	// Parse match[] parameters
	matchParams := r.Form["match[]"]
	if len(matchParams) == 0 {
		h.writePromError(w, "bad_data", "parameter 'match[]' is required")
		return
	}

	tr := observabilitystorageext.TimeRange{}
	if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
		tr.Start = start
	}
	if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
		tr.End = end
	}

	// Selectors are OR'd; dedup the unioned series by their label set.
	seen := make(map[string]struct{})
	var allSeries []promMetric
	for _, matchStr := range matchParams {
		plan, err := planSeriesMatchFull(matchStr)
		if err != nil {
			continue // skip malformed selector
		}
		names, matchAllNames := plan.Names, plan.MatchAllNames

		query := observabilitystorageext.MetricQuery{
			Labels:        plan.Labels,
			LabelMatch:    plan.LabelMatch,
			LabelNot:      plan.LabelNot,
			LabelNotMatch: plan.LabelNotMatch,
			Time:          tr.End,
		}
		if query.Time.IsZero() {
			query.Time = time.Now()
		}

		// When the selector constrains __name__, issue one query per resolved
		// metric name. When it doesn't, issue a single name-agnostic query.
		queryNames := names
		if matchAllNames {
			queryNames = []string{""}
		}
		for _, n := range queryNames {
			q := query
			q.MetricName = n
			result, err := h.metricReader.Query(r.Context(), q)
			if err != nil {
				continue
			}
			for _, dp := range result.Data {
				m := promMetric{}
				name := n
				if name == "" {
					name = dp.Metric
				}
				if name != "" {
					m[PromLabelName] = name
				}
				for k, v := range dp.Labels {
					m[translateLabelToPromQL(k)] = v
				}
				key := seriesKey(m)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				allSeries = append(allSeries, m)
			}
		}
	}

	h.writePromSuccess(w, allSeries)
}

// ── Handler: metadata ──────────────────────────────

// handlePromBuildInfo handles GET /api/v1/status/buildinfo.
//
// Grafana calls this once when a Prometheus datasource is initialised and uses
// the reported version to decide which API flavour to speak (for example which
// label-discovery calls Metrics Drilldown may issue). Returning 404 left the
// datasource on its most conservative code path, so report a version that
// advertises the endpoints we actually implement.
func (h *promHandlers) handlePromBuildInfo(w http.ResponseWriter, _ *http.Request) {
	h.writePromSuccess(w, map[string]string{
		"version":   "2.48.0",
		"revision":  "unknown",
		"branch":    "main",
		"buildUser": "customcol",
		"buildDate": "unknown",
		"goVersion": runtime.Version(),
	})
}

// handlePromMetadata handles GET /api/v1/metadata.
func (h *promHandlers) handlePromMetadata(w http.ResponseWriter, r *http.Request) {
	if h.metricReader == nil {
		h.writePromError(w, "service_unavailable", "metric reader not available")
		return
	}

	tr := observabilitystorageext.TimeRange{}
	if start, err := parsePrometheusTime(r.FormValue("start")); err == nil {
		tr.Start = start
	}
	if end, err := parsePrometheusTime(r.FormValue("end")); err == nil {
		tr.End = end
	}

	names, err := h.metricReader.ListMetricNames(r.Context(), tr)
	if err != nil {
		h.writePromError(w, "execution", err.Error())
		return
	}

	// Metric type drives Grafana Metrics Drilldown's visualization choice and
	// whether it wraps a query in rate(), so report the stored OTel type rather
	// than labelling everything a gauge. A backend that cannot supply types
	// returns an empty map and every metric falls back to "gauge".
	types, err := h.metricReader.ListMetricTypes(r.Context(), tr)
	if err != nil {
		h.logger.Warn("prom metadata: metric type lookup failed, defaulting to gauge", zap.Error(err))
		types = nil
	}

	// Prometheus supports narrowing to one metric via ?metric=<name>.
	if want := r.FormValue("metric"); want != "" {
		filtered := names[:0:0]
		for _, name := range names {
			if name == want {
				filtered = append(filtered, name)
				break
			}
		}
		names = filtered
	}

	metadata := make(map[string][]map[string]string, len(names))
	for _, name := range names {
		metadata[name] = []map[string]string{
			{"type": promMetricType(types[name]), "help": "", "unit": ""},
		}
	}

	h.writePromSuccess(w, metadata)
}

// promMetricType maps a stored OTel metric type to the Prometheus metadata
// vocabulary. storedmodel already writes "gauge"/"counter"/"histogram"/
// "summary", so this mostly guards unknown or missing values.
func promMetricType(stored string) string {
	switch stored {
	case "counter", "gauge", "histogram", "summary":
		return stored
	default:
		return "gauge"
	}
}

// ── Query dispatch ─────────────────────────────────

// dispatchLabelExplore handles PromQL "group by" label exploration queries.
// Uses ES terms aggregation to return unique label value combinations.
func (h *promHandlers) dispatchLabelExplore(r *http.Request, expr *promqlExpr) *promQueryData {
	query := observabilitystorageext.LabelCombinationsQuery{
		MetricName: expr.MetricName,
		LabelKeys:  expr.GroupBy,
	}

	result, err := h.metricReader.ListLabelCombinations(r.Context(), query)
	if err != nil {
		h.logger.Error("label explore failed", zap.Error(err))
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
func (h *promHandlers) dispatchInstantQuery(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	// Label exploration: "group by" without aggregation or rate function.
	// Example: group by (client, connection_type, server) (traces_service_graph_request_total)
	if len(expr.GroupBy) > 0 && expr.Aggregation == "" && expr.Function == "" {
		return h.dispatchLabelExplore(r, expr)
	}

	// ── Data fetching ─────────────────────────────────────

	var vectors []promVectorSample

	if expr.Function != "" && expr.RangeDuration > 0 {
		// rate/increase/irate path: QueryFlat → group → compute rate.
		// histogram_quantile is handled inside execRateInstant.
		vectors = h.execRateInstant(r, expr, evalTime, labels, labelMatch)
		if vectors == nil {
			vectors = []promVectorSample{}
		}
	} else {
		// Plain instant query.
		query := observabilitystorageext.MetricQuery{
			MetricName:    expr.MetricName,
			Labels:        filterInternalLabels(labels),
			LabelMatch:    labelMatch,
			LabelNot:      expr.LabelNot,
			LabelNotMatch: expr.LabelNotMatch,
			Time:          evalTime,
		}

		// Record the effective ES query labels on the span
		span.SetAttributes(
			attribute.StringSlice("es.labels", flattenLabels(query.Labels)),
		)

		result, err := h.metricReader.Query(r.Context(), query)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String(SpanAttrErrorType, "query"))
			h.logger.Error("prom instant query failed", zap.Error(err))
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
func (h *promHandlers) dispatchRangeQuery(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	span := trace.SpanFromContext(r.Context())

	if expr.Function != "" && expr.RangeDuration > 0 {
		// histogram_quantile range query. Gated on the aggregation marker — see
		// the matching comment in execRateInstant for why a _bucket suffix is
		// not required (native-histogram form from Grafana Metrics Drilldown).
		if expr.Aggregation == AggHistogramQuantile {
			defer span.SetAttributes(attribute.Int("promql.series_count", 0))
			result := h.execHistogramQuantileRange(r, expr, start, end, step, labels, labelMatch)
			if result == nil {
				return &promQueryData{ResultType: "matrix", Result: []promMatrixSample{}}
			}
			return result
		}
		// Heatmap: `sum by (le) (rate(m[5m]))`. Must precede the plain rate path,
		// which would group by a label ES does not store and collapse every
		// bucket into one series.
		if expr.Aggregation != "" && groupsByLe(expr.GroupBy) {
			defer span.SetAttributes(attribute.Int("promql.series_count", 0))
			result := h.execHistogramBucketRange(r, expr, start, end, step, labels, labelMatch)
			if result == nil {
				return &promQueryData{ResultType: ResultTypeMatrix, Result: []promMatrixSample{}}
			}
			return result
		}
		// rate/increase/irate path: use QueryRaw to get original samples
		defer span.SetAttributes(attribute.Int("promql.series_count", 0))
		result := h.execRateRange(r, expr, start, end, step, labels, labelMatch)
		if result == nil {
			return &promQueryData{ResultType: ResultTypeMatrix, Result: []promMatrixSample{}}
		}
		return result
	}

	// No aggregation and no rate/increase function → a bare metric selector
	// (e.g. "traces_service_graph_request_total" or "metric{job=\"x\"}").
	// Return one series per distinct label set with full labels, matching
	// Prometheus semantics and the instant-query path. With GroupBy empty the
	// ES path uses a simple date_histogram that collapses every series into
	// one (only __name__, no dimension labels). Discover the metric's label
	// keys and use them as GroupBy so QueryRange emits per-series buckets via
	// a composite aggregation — full time range, no QueryFlat doc-cap
	// truncation. buildSeriesMetric keeps all labels because expr.Aggregation
	// is still "".
	//
	// missing_bucket=true is essential here: the auto-discovered GroupBy may
	// contain label keys that not every series has (e.g. http_route vs
	// rpc_method). With missing_bucket=false, ES composite drops any series
	// lacking even one of the grouped labels — silently returning 0 series.
	// missing_bucket=true puts such series into a null-key bucket, so every
	// series appears in the output regardless of its label set.
	bareMetric := expr.Aggregation == "" && expr.Function == "" && len(expr.GroupBy) == 0
	if bareMetric {
		if labelKeys, derr := h.metricReader.ListLabelNames(r.Context(),
			observabilitystorageext.TimeRange{Start: start, End: end}, expr.MetricName); derr == nil {
			groupBy := make([]string, 0, len(labelKeys))
			for _, k := range labelKeys {
				if promKey := translateLabelToPromQL(k); promKey != "" {
					groupBy = append(groupBy, promKey)
				}
			}
			expr.GroupBy = groupBy
		} else {
			h.logger.Warn("bare-metric range query: failed to discover label keys, falling back to collapsed single-series",
				zap.String("metric", expr.MetricName), zap.Error(derr))
		}
	}

	query := observabilitystorageext.MetricRangeQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: start, End: end},
		Step:          step,
		Aggregation:   expr.Aggregation,
		GroupBy:       expr.GroupBy,
		MissingBucket: bareMetric, // bare-metric: include all series; explicit by(): drop missing
	}
	if query.Aggregation == "" {
		query.Aggregation = AggAvg
	}

	// Record the effective ES query labels on the span
	span.SetAttributes(
		attribute.StringSlice(SpanAttrESLabels, flattenLabels(query.Labels)),
		attribute.StringSlice(SpanAttrESGroupByFull, query.GroupBy),
	)

	result, err := h.metricReader.QueryRange(r.Context(), query)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String(SpanAttrErrorType, "query_range"))
		h.logger.Error("prom range query failed", zap.Error(err))
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
func (h *promHandlers) execRateRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	// Extend time range back by the range duration for lookback.
	lookbackStart := start.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	flatResult, err := h.concurrentQueryFlat(r.Context(), flatQuery, h.logger)
	if err != nil {
		h.logger.Error("prom query_flat failed", zap.Error(err))
		return nil
	}
	if flatResult == nil || len(flatResult.Samples) == 0 {
		return nil
	}

	h.checkFlatTruncation(flatResult)

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

		values := h.computeRate(sg.Samples, start, end, step, expr)
		matrix = append(matrix, promMatrixSample{
			Metric: m,
			Values: values,
		})
	}

	if expr.Aggregation != "" {
		// Also runs with an empty GroupBy: bare `sum(rate(m[5m]))` must collapse
		// every series into one, exactly as the instant path does.
		matrix = aggregateMatrix(expr.Aggregation, expr.GroupBy, matrix)
		stripMatrixMetricToGroupBy(matrix, expr.GroupBy)
	}

	return &promQueryData{
		ResultType: ResultTypeMatrix,
		Result:     matrix,
	}
}

// execRateInstant handles rate/increase/irate instant queries.
func (h *promHandlers) execRateInstant(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) []promVectorSample {
	// histogram_quantile → aggregate bucket_counts and compute the quantile.
	// Gated on the aggregation marker, not on a _bucket name suffix: ES stores
	// the buckets on the base metric document, so Grafana Metrics Drilldown's
	// native-histogram form — histogram_quantile(θ, sum(rate(m[5m]))) with no
	// _bucket series and no by(le) — must take this path too. Without it the
	// query fell through to the plain rate path and returned raw per-series
	// rates, identical for every θ.
	//
	// Note: Quantile's zero value is 0.0, not NaN, so it cannot itself gate this.
	if expr.Aggregation == AggHistogramQuantile {
		return h.execHistogramQuantileInstant(r, expr, evalTime, labels, labelMatch)
	}

	// For instant query, look back from evalTime by the range duration.
	lookbackStart := evalTime.Add(-expr.RangeDuration)

	// Use QueryFlat (same as execRateRange) to avoid ES top_hits limit and
	// painless script hardcoded label fields. Data is grouped by labels in Go.
	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: lookbackStart, End: evalTime},
	}

	flatResult, err := h.concurrentQueryFlat(r.Context(), flatQuery, h.logger)
	if err != nil {
		h.logger.Error("prom query_flat failed", zap.Error(err))
		return nil
	}
	if flatResult == nil || len(flatResult.Samples) == 0 {
		return nil
	}

	h.checkFlatTruncation(flatResult)

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

// histogramGroupLabels projects a histogram series' labels onto the grouping
// implied by the aggregation nested inside histogram_quantile.
//
// PromQL evaluates `histogram_quantile(θ, sum by (le) (rate(m[5m])))` inside
// out: the inner `sum by (le)` collapses every series to the `le` dimension, so
// the quantile yields ONE series. `sum by (le, service)` yields one per service.
// `le` itself is consumed by the quantile and never appears in the output.
//
// Returns nil when the inner aggregation collapses everything to a single
// series, so callers group all buckets together.
func histogramGroupLabels(expr *promqlExpr, labels map[string]string) map[string]string {
	if expr.InnerAgg == "" {
		// No inner aggregation: one output series per input series, labels intact.
		out := make(map[string]string, len(labels))
		for k, v := range labels {
			if k == PromLabelLe {
				continue
			}
			out[translateLabelToPromQL(k)] = v
		}
		return out
	}

	out := make(map[string]string, len(expr.GroupBy))
	for _, g := range expr.GroupBy {
		if g == PromLabelLe {
			continue // consumed by the quantile computation
		}
		// GroupBy is in PromQL form; sample labels are in ES dot form.
		for k, v := range labels {
			if translateLabelToPromQL(k) == g {
				out[g] = v
				break
			}
		}
	}
	return out
}

// execHistogramQuantileInstant handles histogram_quantile(θ, rate(...))
// by querying ES histogram documents via QueryFlat, grouping by labels in Go,
// aggregating bucket_counts across time, and computing quantiles via linear interpolation.
func (h *promHandlers) execHistogramQuantileInstant(r *http.Request, expr *promqlExpr, evalTime time.Time, labels, labelMatch map[string]string) []promVectorSample {
	lookbackStart := evalTime.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: lookbackStart, End: evalTime},
	}

	result, err := h.concurrentQueryFlat(r.Context(), flatQuery, h.logger)
	if err != nil {
		h.logger.Error("histogram_quantile query_flat failed", zap.Error(err))
		return nil
	}
	if result == nil || len(result.Samples) == 0 {
		return nil
	}

	h.checkFlatTruncation(result)

	// Group samples by labels in Go (replaces ES-side composite+painless script).
	groups := groupSamplesByLabels(result.Samples)

	buckets := make([]HistogramBucket, 0, len(groups))
	for _, samples := range groups {
		hb := HistogramBucket{}
		// Project onto the grouping implied by the inner aggregation, so that
		// AggregateHistogramBuckets (which groups by label set) collapses the
		// series exactly as PromQL would.
		if len(samples) > 0 {
			hb.Labels = histogramGroupLabels(expr, samples[0].Labels)
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
		// An aggregated result carries no metric name, matching PromQL.
		m := promMetric{}
		if expr.InnerAgg == "" {
			m[PromLabelName] = expr.MetricName
		}
		// Labels are already projected and PromQL-formatted.
		for k, v := range hb.Labels {
			m[k] = v
		}
		vectors = append(vectors, promVectorSample{
			Metric: m,
			Value:  []any{float64(evalTime.UnixMilli()) / 1000.0, formatPromValue(q)},
		})
	}

	return vectors
}

// execHistogramBucketRange handles `sum by (le) (rate(m[5m]))` — the heatmap
// query Grafana Metrics Drilldown issues for histogram metrics.
//
// ES has no "le" label: bucket data lives in the bucket_counts/explicit_bounds
// arrays on each document. Grouping by a label that does not exist collapses
// every bucket into one series, so the heatmap renders a single row instead of
// a distribution. This synthesises the le dimension from the arrays.
//
// Emits Prometheus-style CUMULATIVE buckets (each le carries the count of all
// buckets up to and including it) plus the +Inf bucket, since that is what a
// heatmap consumer expects from `_bucket` series. Values are per-second rates
// over the window, matching the rate() the caller asked for.
func (h *promHandlers) execHistogramBucketRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	lookbackStart := start.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	result, err := h.concurrentQueryFlat(r.Context(), flatQuery, h.logger)
	if err != nil {
		h.logger.Error("histogram bucket range query_flat failed", zap.Error(err))
		return nil
	}
	if result == nil || len(result.Samples) == 0 {
		return nil
	}
	h.checkFlatTruncation(result)

	// Non-le grouping dimensions requested alongside le, e.g. sum by (le, service).
	extraGroupBy := make([]string, 0, len(expr.GroupBy))
	for _, g := range expr.GroupBy {
		if g != PromLabelLe {
			extraGroupBy = append(extraGroupBy, g)
		}
	}

	// Merge the raw per-series groups down to the requested grouping.
	type bucketGroup struct {
		labels  map[string]string
		samples []HistogramSample
	}
	merged := make(map[string]*bucketGroup)
	for _, samples := range groupSamplesByLabels(result.Samples) {
		if len(samples) == 0 {
			continue
		}
		projected := make(map[string]string, len(extraGroupBy))
		for _, g := range extraGroupBy {
			for k, v := range samples[0].Labels {
				if translateLabelToPromQL(k) == g {
					projected[g] = v
					break
				}
			}
		}
		key := sortedLabelKey(projected)
		g, ok := merged[key]
		if !ok {
			g = &bucketGroup{labels: projected}
			merged[key] = g
		}
		g.samples = append(g.samples, samples...)
	}

	stepMs := step.Milliseconds()
	rangeMs := expr.RangeDuration.Milliseconds()
	rangeSecs := expr.RangeDuration.Seconds()
	if rangeSecs <= 0 {
		return nil
	}

	// One output series per (group, le) pair; points accumulate across steps.
	series := make(map[bucketSeriesKey]*promMatrixSample)
	var order []bucketSeriesKey

	for gk, g := range merged {
		for t := start.UnixMilli(); t <= end.UnixMilli(); t += stepMs {
			hb := AggregateHistogramSamples(g.samples, t-rangeMs, t)
			if hb.TotalCount == 0 || len(hb.Bounds) == 0 {
				continue
			}

			var cumulative int64
			for i, bound := range hb.Bounds {
				if i < len(hb.BucketCounts) {
					cumulative += hb.BucketCounts[i]
				}
				le := formatPromFloat(bound)
				addBucketPoint(series, &order, bucketSeriesKey{gk, le},
					g.labels, le, t, float64(cumulative)/rangeSecs)
			}
			// +Inf carries the full count; Prometheus always emits it.
			addBucketPoint(series, &order, bucketSeriesKey{gk, "+Inf"},
				g.labels, "+Inf", t, float64(hb.TotalCount)/rangeSecs)
		}
	}

	if len(series) == 0 {
		return nil
	}

	matrix := make([]promMatrixSample, 0, len(series))
	for _, k := range order {
		matrix = append(matrix, *series[k])
	}
	return &promQueryData{ResultType: ResultTypeMatrix, Result: matrix}
}

// groupsByLe reports whether a `by (...)` clause includes the le dimension,
// which marks the query as a histogram bucket (heatmap) query.
func groupsByLe(groupBy []string) bool {
	return slices.Contains(groupBy, PromLabelLe)
}

// bucketSeriesKey identifies one heatmap output series: a grouping key plus the
// le bound it represents.
type bucketSeriesKey struct {
	group string
	le    string
}

// addBucketPoint appends one sample to the (group, le) series, creating it on
// first use and recording insertion order for stable output.
func addBucketPoint(series map[bucketSeriesKey]*promMatrixSample, order *[]bucketSeriesKey,
	key bucketSeriesKey, groupLabels map[string]string, le string, tsMs int64, value float64) {
	s, ok := series[key]
	if !ok {
		m := promMetric{PromLabelLe: le}
		for k, v := range groupLabels {
			m[k] = v
		}
		s = &promMatrixSample{Metric: m}
		series[key] = s
		*order = append(*order, key)
	}
	s.Values = append(s.Values, []any{float64(tsMs) / 1000.0, formatPromValue(value)})
}

// formatPromFloat renders a bucket bound the way Prometheus writes le labels:
// the shortest representation that round-trips.
func formatPromFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// execHistogramQuantileRange handles histogram_quantile(θ, rate(_bucket[...]))
// range queries by computing quantile at each step from a sliding window.
func (h *promHandlers) execHistogramQuantileRange(r *http.Request, expr *promqlExpr, start, end time.Time, step time.Duration, labels, labelMatch map[string]string) *promQueryData {
	lookbackStart := start.Add(-expr.RangeDuration)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:    expr.MetricName,
		Labels:        filterInternalLabels(labels),
		LabelMatch:    labelMatch,
		LabelNot:      expr.LabelNot,
		LabelNotMatch: expr.LabelNotMatch,
		TimeRange:     observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
	}

	result, err := h.concurrentQueryFlat(r.Context(), flatQuery, h.logger)
	if err != nil {
		h.logger.Error("histogram_quantile range query_flat failed", zap.Error(err))
		return nil
	}
	if result == nil || len(result.Samples) == 0 {
		return nil
	}

	h.checkFlatTruncation(result)

	// Group samples by labels in Go (replaces ES-side composite+painless script).
	groups := groupSamplesByLabels(result.Samples)

	// Re-key by the grouping the inner aggregation implies, merging the raw
	// per-series groups so a bare `sum by (le)` collapses to a single series.
	// Iteration order of the raw map does not matter: the merge is additive and
	// each output key is built from the projected labels alone.
	type histGroup struct {
		labels  map[string]string
		samples []HistogramSample
	}
	merged := make(map[string]*histGroup, len(groups))
	for _, samples := range groups {
		if len(samples) == 0 {
			continue
		}
		projected := histogramGroupLabels(expr, samples[0].Labels)
		key := sortedLabelKey(projected)
		g, ok := merged[key]
		if !ok {
			g = &histGroup{labels: projected}
			merged[key] = g
		}
		g.samples = append(g.samples, samples...)
	}

	stepMs := step.Milliseconds()
	rangeMs := expr.RangeDuration.Milliseconds()

	// Build matrix: one series per group, values at each step.
	matrix := make([]promMatrixSample, 0, len(merged))
	for _, g := range merged {
		histSamples := g.samples
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

		// An aggregated result carries no metric name, matching PromQL.
		m := promMetric{}
		if expr.InnerAgg == "" {
			m[PromLabelName] = expr.MetricName
		}
		// Labels are already projected and PromQL-formatted.
		for k, v := range g.labels {
			m[k] = v
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
func (h *promHandlers) checkFlatTruncation(result *observabilitystorageext.MetricFlatResult) {
	if result.Total > int64(len(result.Samples)) {
		h.logger.Warn("QueryFlat data truncated by MaxDocs limit",
			zap.Int64("total_matching_in_es", result.Total),
			zap.Int("returned", len(result.Samples)),
		)
	}
}

// computeRate computes rate/increase/irate at each step point.
func (h *promHandlers) computeRate(samples []observabilitystorageext.MetricSample, start, end time.Time, step time.Duration, expr *promqlExpr) [][]any {
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

// applyTopKMatrix selects the K highest (or lowest) series from a matrix.
//
// PromQL evaluates topk independently at each timestamp, so a series can enter
// and leave the set over the range. Grafana charts a fixed set of lines, so
// rank each series once by its extreme value over the whole range — the K
// series that peak highest for topk, that dip lowest for bottomk. This keeps
// the line count at K instead of returning every series unranked.
func applyTopKMatrix(k int, isBottomK bool, matrix []promMatrixSample) []promMatrixSample {
	if k <= 0 || k >= len(matrix) {
		return matrix
	}

	rank := func(m promMatrixSample) float64 {
		best := math.NaN()
		for _, pair := range m.Values {
			if len(pair) < 2 {
				continue
			}
			s, ok := pair[1].(string)
			if !ok {
				continue
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(v) {
				continue
			}
			if math.IsNaN(best) || (isBottomK && v < best) || (!isBottomK && v > best) {
				best = v
			}
		}
		return best
	}

	ranks := make(map[int]float64, len(matrix))
	for i, m := range matrix {
		ranks[i] = rank(m)
	}
	idx := make([]int, len(matrix))
	for i := range idx {
		idx[i] = i
	}

	// Series with no usable point rank last regardless of direction.
	sort.SliceStable(idx, func(a, b int) bool {
		va, vb := ranks[idx[a]], ranks[idx[b]]
		if math.IsNaN(va) {
			return false
		}
		if math.IsNaN(vb) {
			return true
		}
		if isBottomK {
			return va < vb
		}
		return va > vb
	})

	out := make([]promMatrixSample, 0, k)
	for _, i := range idx[:k] {
		out = append(out, matrix[i])
	}
	return out
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
	// An aggregation over nothing is nothing, not one empty sample. Without this
	// guard a rate window shorter than the scrape interval (fewer than two
	// samples, so no vectors) emitted {"metric":null,"value":null} — a malformed
	// series that breaks strict JSON consumers instead of reading as "no data".
	if len(vectors) == 0 {
		return nil
	}

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

	// Collect the usable values once; every branch below needs the same set.
	// NaN/Inf are excluded: strconv.ParseFloat accepts "NaN"/"+Inf", and letting
	// them through makes sum/avg/stddev collapse to NaN for the whole group.
	values := make([]float64, 0, len(vectors))
	for _, v := range vectors {
		s, ok := v.Value[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		values = append(values, f)
	}

	val := 0.0
	switch fn {
	case AggSum:
		for _, f := range values {
			val += f
		}
	case AggAvg:
		for _, f := range values {
			val += f
		}
		if len(values) > 0 {
			val /= float64(len(values))
		}
	case AggMax:
		val = math.Inf(-1)
		for _, f := range values {
			if f > val {
				val = f
			}
		}
	case AggMin:
		val = math.Inf(1)
		for _, f := range values {
			if f < val {
				val = f
			}
		}
	case AggCount:
		// Counts series, not parseable values: an unparseable sample is still a
		// member of the group.
		val = float64(len(vectors))
	case AggStddev, AggStdvar:
		// Population (not sample) variance, matching Prometheus.
		if len(values) > 0 {
			mean := 0.0
			for _, f := range values {
				mean += f
			}
			mean /= float64(len(values))
			for _, f := range values {
				d := f - mean
				val += d * d
			}
			val /= float64(len(values))
			if fn == AggStddev {
				val = math.Sqrt(val)
			}
		}
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
//
// Series are grouped by the groupBy labels (all series collapse into a single
// group when groupBy is empty, matching PromQL's bare `sum(...)`), then reduced
// per timestamp. The rate/increase/irate path evaluates every series on the same
// start/end/step grid, so samples align on their timestamps and can be combined
// directly; a timestamp missing from a series simply does not contribute to that
// point, as in Prometheus.
//
// This previously returned the input unchanged, so range queries like
// `sum(rate(m[5m]))` came back as hundreds of un-aggregated series while the
// instant-query path returned the correct single series.
func aggregateMatrix(fn string, groupBy []string, matrix []promMatrixSample) []promMatrixSample {
	if len(matrix) == 0 {
		return matrix
	}

	// Preserve input order of groups so output is stable across calls.
	groups := make(map[string][]promMatrixSample)
	var order []string
	for _, m := range matrix {
		key := groupKey(promMetric(m.Metric), groupBy)
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], m)
	}

	result := make([]promMatrixSample, 0, len(order))
	for _, key := range order {
		group := groups[key]
		metric := promMetric{}
		if len(groupBy) > 0 {
			metric = filterMetricByKeys(promMetric(group[0].Metric), groupBy)
		}
		result = append(result, promMatrixSample{
			Metric: metric,
			Values: aggregateMatrixValues(fn, group),
		})
	}
	return result
}

// aggregateMatrixValues reduces a group of aligned series to one value per
// timestamp, applying fn across the series present at that timestamp.
func aggregateMatrixValues(fn string, group []promMatrixSample) [][]any {
	// Collect values per timestamp, keeping first-seen timestamp order so the
	// result stays chronological (the rate path emits ascending timestamps).
	byTs := make(map[float64][]float64)
	var tsOrder []float64

	for _, m := range group {
		for _, tv := range m.Values {
			if len(tv) != 2 {
				continue
			}
			ts, ok := toFloat(tv[0])
			if !ok {
				continue
			}
			v, ok := toFloat(tv[1])
			if !ok {
				// NaN/Inf are formatted as strings; skip so they do not poison
				// the aggregate, matching how the vector path parses values.
				continue
			}
			if _, seen := byTs[ts]; !seen {
				tsOrder = append(tsOrder, ts)
			}
			byTs[ts] = append(byTs[ts], v)
		}
	}

	sort.Float64s(tsOrder)

	values := make([][]any, 0, len(tsOrder))
	for _, ts := range tsOrder {
		values = append(values, []any{ts, formatPromValue(reduceSamples(fn, byTs[ts]))})
	}
	return values
}

// reduceSamples applies a PromQL aggregation operator to the values observed at
// one timestamp.
func reduceSamples(fn string, vals []float64) float64 {
	if len(vals) == 0 {
		return math.NaN()
	}
	switch fn {
	case AggSum:
		total := 0.0
		for _, v := range vals {
			total += v
		}
		return total
	case AggAvg:
		total := 0.0
		for _, v := range vals {
			total += v
		}
		return total / float64(len(vals))
	case AggMax:
		out := math.Inf(-1)
		for _, v := range vals {
			if v > out {
				out = v
			}
		}
		return out
	case AggMin:
		out := math.Inf(1)
		for _, v := range vals {
			if v < out {
				out = v
			}
		}
		return out
	case AggCount:
		return float64(len(vals))
	case AggStddev, AggStdvar:
		// Population (not sample) variance, matching Prometheus.
		mean := 0.0
		for _, v := range vals {
			mean += v
		}
		mean /= float64(len(vals))
		variance := 0.0
		for _, v := range vals {
			d := v - mean
			variance += d * d
		}
		variance /= float64(len(vals))
		if fn == AggStdvar {
			return variance
		}
		return math.Sqrt(variance)
	default:
		return vals[0]
	}
}

// toFloat reads a matrix cell, which holds timestamps as float64 and values as
// the strings produced by formatPromValue.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
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

// ── Response writers ────────────────────────────────

func (h *promHandlers) writePromSuccess(w http.ResponseWriter, data any) {
	resp := promResponse{
		Status: "success",
		Data:   data,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *promHandlers) writePromSuccessLabelList(w http.ResponseWriter, values []string) {
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

func (h *promHandlers) writePromError(w http.ResponseWriter, errorType, message string) {
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
func (h *promHandlers) getQueryParam(r *http.Request, key string) string {
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
