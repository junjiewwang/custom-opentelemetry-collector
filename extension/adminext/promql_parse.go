// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"math"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// This file holds the simple-subset PromQL parser and label/selector helpers,
// extracted from prometheus_handler.go for cohesion. Pure free functions.

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
