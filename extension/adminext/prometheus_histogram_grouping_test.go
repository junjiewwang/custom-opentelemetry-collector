package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// histogram_quantile is evaluated inside out: the aggregation nested inside it
// decides how many series come out. `sum by (le)` collapses everything to one
// series; `sum by (le, service_name)` yields one per service; no inner
// aggregation leaves the input series untouched. `le` is always consumed by the
// quantile itself and must never appear in the output labels.
func TestHistogramGroupLabels(t *testing.T) {
	// ES stores labels in dot form; PromQL uses underscores.
	sampleLabels := map[string]string{
		"service.name": "checkout",
		"span.kind":    "Server",
		"le":           "0.5",
	}

	tests := []struct {
		name     string
		expr     *promqlExpr
		expected map[string]string
	}{
		{
			name:     "bare sum collapses to a single series",
			expr:     &promqlExpr{InnerAgg: "sum"},
			expected: map[string]string{},
		},
		{
			name:     "sum by (le) collapses too — le is consumed by the quantile",
			expr:     &promqlExpr{InnerAgg: "sum", GroupBy: []string{"le"}},
			expected: map[string]string{},
		},
		{
			name:     "sum by (le, service_name) keeps the service dimension",
			expr:     &promqlExpr{InnerAgg: "sum", GroupBy: []string{"le", "service_name"}},
			expected: map[string]string{"service_name": "checkout"},
		},
		{
			name:     "grouping label absent from the series is omitted",
			expr:     &promqlExpr{InnerAgg: "sum", GroupBy: []string{"le", "nonexistent"}},
			expected: map[string]string{},
		},
		{
			name: "no inner aggregation keeps all labels but drops le",
			expr: &promqlExpr{},
			expected: map[string]string{
				"service_name": "checkout",
				"span_kind":    "Server",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, histogramGroupLabels(tc.expr, sampleLabels))
		})
	}
}

// The inner aggregation must survive parsing: histogram_quantile overwrites
// Aggregation with its own marker, so without InnerAgg the grouping intent of
// `sum by (le) (...)` would be lost and every bucket series returned raw.
func TestParsePromQL_HistogramQuantilePreservesInnerAgg(t *testing.T) {
	tests := []struct {
		query    string
		innerAgg string
		groupBy  []string
	}{
		{`histogram_quantile(0.99, sum by (le) (rate(m_bucket[5m])))`, "sum", []string{"le"}},
		{`histogram_quantile(0.95, sum(rate(m[5m])))`, "sum", nil},
		{`histogram_quantile(0.5, sum by (le, service_name) (rate(m_bucket[5m])))`, "sum", []string{"le", "service_name"}},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			expr, err := parsePromQL(tc.query)
			assert.NoError(t, err)
			assert.Equal(t, AggHistogramQuantile, expr.Aggregation)
			assert.Equal(t, tc.innerAgg, expr.InnerAgg)
			assert.Equal(t, tc.groupBy, expr.GroupBy)
		})
	}
}

// The quantile θ must reach the expression regardless of the metric name, since
// Grafana Metrics Drilldown emits the native-histogram form against a base
// metric with no _bucket suffix.
func TestParsePromQL_QuantileParsedWithoutBucketSuffix(t *testing.T) {
	expr, err := parsePromQL(`histogram_quantile(0.99, sum(rate(traces_spanmetrics_latency[5m])))`)
	assert.NoError(t, err)
	assert.InDelta(t, 0.99, expr.Quantile, 1e-9)
	assert.Empty(t, expr.HistogramSub, "no _bucket suffix to strip")
	assert.Equal(t, "traces_spanmetrics_latency", expr.MetricName)
}

// The histogram_quantile executors are selected by Aggregation, never by the
// Quantile field: its zero value is 0.0 (not NaN), so a `!math.IsNaN(Quantile)`
// gate matches EVERY expression and silently routes ordinary rate queries into
// the histogram path, where they return nothing for lack of bucket data.
func TestParsePromQL_PlainRateIsNotAHistogramQuantile(t *testing.T) {
	queries := []string{
		`sum by (span_kind) (rate(traces_spanmetrics_calls_total[5m]))`,
		`sum(rate(traces_spanmetrics_calls_total[5m]))`,
		`avg by (jvm_memory_type) (rate(m[5m]))`,
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			expr, err := parsePromQL(q)
			assert.NoError(t, err)
			assert.NotEqual(t, AggHistogramQuantile, expr.Aggregation,
				"plain rate query must not be routed to the histogram_quantile path")
			assert.Empty(t, expr.InnerAgg, "InnerAgg is only set by histogram_quantile")
		})
	}
}
