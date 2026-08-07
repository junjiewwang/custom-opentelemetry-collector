package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The storage layer keys metrics by name (a term query on the name field) and
// has no "__name__" label. Leaving __name__ in Labels therefore filters on a
// label that never exists, which ES ignores — the query returns every metric
// unfiltered instead of erroring. Grafana emits this form for metric names that
// are not valid bare identifiers, so it must resolve to MetricName.
func TestParsePromQL_NameLabelRoutesToMetricName(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		metricName string
		labels     map[string]string
	}{
		{
			name:       "exact __name__ selector",
			query:      `{__name__="traces_spanmetrics_calls_total"}`,
			metricName: "traces_spanmetrics_calls_total",
			labels:     map[string]string{},
		},
		{
			name:       "dotted metric name",
			query:      `{__name__="jvm.memory.used"}`,
			metricName: "jvm.memory.used",
			labels:     map[string]string{},
		},
		{
			name:       "__name__ alongside other labels",
			query:      `{__name__="m", service_name="checkout"}`,
			metricName: "m",
			labels:     map[string]string{"service_name": "checkout"},
		},
		{
			name:       "bare name takes precedence over __name__",
			query:      `real_metric{__name__="ignored"}`,
			metricName: "real_metric",
			labels:     map[string]string{},
		},
		{
			name:       "wrapped in an aggregation",
			query:      `sum(rate({__name__="m"}[5m]))`,
			metricName: "m",
			labels:     map[string]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := parsePromQL(tc.query)
			assert.NoError(t, err)
			assert.Equal(t, tc.metricName, expr.MetricName)
			assert.Equal(t, tc.labels, expr.Labels)
			assert.NotContains(t, expr.Labels, PromLabelName,
				"__name__ must never reach the storage layer as a label")
		})
	}
}

// "!~" is the only PromQL matcher containing no "=", so a parser that splits on
// "=" first drops the pair entirely and returns unfiltered results.
func TestParsePromQL_NotRegexMatcherIsParsed(t *testing.T) {
	tests := []struct {
		query    string
		notMatch map[string]string
	}{
		{`m{service_name!~"test-java.*"}`, map[string]string{"service_name": "test-java.*"}},
		{`sum(rate(m{span_kind!~"Client|Server"}[5m]))`, map[string]string{"span_kind": "Client|Server"}},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			expr, err := parsePromQL(tc.query)
			assert.NoError(t, err)
			assert.Equal(t, tc.notMatch, expr.LabelNotMatch)
			assert.Empty(t, expr.Labels, "!~ must not be mistaken for an exact match")
		})
	}
}

// Each matcher kind must land in its own map: conflating them silently changes
// the filter's meaning (e.g. != read as =) rather than failing.
func TestParsePromQL_MatcherKindsAreDistinct(t *testing.T) {
	expr, err := parsePromQL(`m{a="1", b=~"2", c!="3", d!~"4"}`)
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "1"}, expr.Labels)
	assert.Equal(t, map[string]string{"b": "2"}, expr.LabelMatch)
	assert.Equal(t, map[string]string{"c": "3"}, expr.LabelNot)
	assert.Equal(t, map[string]string{"d": "4"}, expr.LabelNotMatch)
}
