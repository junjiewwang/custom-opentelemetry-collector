// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

func TestDetectHistogramSub(t *testing.T) {
	tests := []struct {
		input   string
		wantSub string
		wantOK  bool
	}{
		{"traces_service_graph_request_server_seconds_sum", HistogramSubSum, true},
		{"traces_service_graph_request_server_seconds_bucket", HistogramSubBucket, true},
		{"traces_spanmetrics_calls_total", "", false},
		{"traces_service_graph_request_total", "", false},
		{"traces_service_graph_request_server_seconds", "", false},
		{"my_custom_summary", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			sub, ok := detectHistogramSub(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantSub, sub)
		})
	}
}

func TestStripHistogramSuffix(t *testing.T) {
	tests := []struct{ input, want string }{
		{"t_s_seconds_sum", "t_s_seconds"},
		{"t_s_seconds_bucket", "t_s_seconds"},
		{"t_s_seconds", "t_s_seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, stripHistogramSuffix(tt.input))
		})
	}
}

func TestResolveHistogramBucket(t *testing.T) {
	dp := observabilitystorageext.MetricDataPoint{
		Value:          100.0,
		ExplicitBounds: []float64{0.005, 0.01, 0.05},
		BucketCounts:   []int64{5, 3, 2},
	}

	t.Run("match first bucket", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{PromLabelLe: "0.005"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(5), v)
	})

	t.Run("match second bucket", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{PromLabelLe: "0.05"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(2), v)
	})

	t.Run("no le label returns sum", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, 100.0, v)
	})

	t.Run("le not found in bounds", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{PromLabelLe: "0.001"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(0), v)
	})

	t.Run("invalid le format", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{PromLabelLe: "abc"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(0), v)
	})
}

func TestDispatchLabelExplore(t *testing.T) {
	tests := []struct {
		name     string
		expr     *promqlExpr
		wantKeys []string
	}{
		{
			name:     "single label group",
			expr:     &promqlExpr{MetricName: "test_metric", GroupBy: []string{"client"}},
			wantKeys: []string{"client"},
		},
		{
			name:     "multi label group",
			expr:     &promqlExpr{MetricName: "test_metric", GroupBy: []string{"client", "server", "connection_type"}},
			wantKeys: []string{"client", "server", "connection_type"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantKeys, tt.expr.GroupBy)
		})
	}
}

func TestDispatchLabelExplore_NoGroupBy(t *testing.T) {
	expr := &promqlExpr{
		MetricName:  "test_metric",
		GroupBy:     []string{},
		Aggregation: AggSum,
	}
	assert.Empty(t, expr.GroupBy)
	assert.NotEmpty(t, expr.Aggregation, "should NOT trigger label explore")
}

func TestExtractMetricNameFromMatch(t *testing.T) {
	tests := []struct {
		name    string
		matches []string
		want    string
	}{
		{"single match", []string{`{__name__="traces_service_graph_request_total"}`}, "traces_service_graph_request_total"},
		{"client seconds", []string{`{__name__="traces_service_graph_request_client_seconds"}`}, "traces_service_graph_request_client_seconds"},
		{"regex match", []string{`{__name__=~".*traces_service_graph_request_client_seconds.*"}`}, "traces_service_graph_request_client_seconds"},
		{"regex with prefix only", []string{`{__name__=~"traces_service_graph.*"}`}, "traces_service_graph"},
		// Regex with | alternation: multiple metrics — return "" (no single metric name filter).
		{"regex alternation multi-metric", []string{`{__name__=~"traces_service_graph_request_server_seconds_sum|traces_service_graph_request_total|traces_service_graph_request_failed_total"}`}, ""},
		{"empty matches", []string{}, ""},
		{"multiple matches takes first", []string{`{__name__="metric_a"}`, `{__name__="metric_b"}`}, "metric_a"},
		{"no name label", []string{`{server="test"}`}, ""},
		{"empty string", []string{""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMetricNameFromMatch(tt.matches)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseHistogramQuantileWrapper(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantInner   string
		wantTheta   float64
	}{
		{
			name:      "basic",
			input:     `histogram_quantile(0.9, sum(rate(metric_bucket[5m])) by (le))`,
			wantInner: `sum(rate(metric_bucket[5m])) by (le)`,
			wantTheta: 0.9,
		},
		{
			name:      "with server filter",
			input:     `histogram_quantile(0.99, sum(rate(traces_service_graph_request_server_seconds_bucket{server="test-java-market-service"}[1m0s])) by (le, client, server))`,
			wantInner: `sum(rate(traces_service_graph_request_server_seconds_bucket{server="test-java-market-service"}[1m0s])) by (le, client, server)`,
			wantTheta: 0.99,
		},
		{
			name:      "p50",
			input:     `histogram_quantile(0.5, rate(metric_bucket[5m]))`,
			wantInner: `rate(metric_bucket[5m])`,
			wantTheta: 0.5,
		},
		{
			name:    "no match",
			input:   `sum(rate(metric[5m])) by (label)`,
		},
		{
			name:    "empty",
			input:   ``,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner, theta := parseHistogramQuantileWrapper(tt.input)
			assert.Equal(t, tt.wantTheta, theta)
			if tt.wantInner != "" {
				assert.Equal(t, tt.wantInner, inner)
			} else {
				assert.Empty(t, inner)
			}
		})
	}
}

// ── counterIncrease / computeRateInWindow tests ──────────

func makeSamples(vals ...float64) []observabilitystorageext.MetricSample {
	base := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC).UnixMilli()
	samples := make([]observabilitystorageext.MetricSample, len(vals))
	for i, v := range vals {
		samples[i] = observabilitystorageext.MetricSample{
			TimestampMs: base + int64(i*15000),
			Value:       v,
		}
	}
	return samples
}

func TestCounterIncrease_Normal(t *testing.T) {
	samples := makeSamples(100, 120, 150)
	got := counterIncrease(samples, 0, 2)
	assert.Equal(t, float64(50), got)
}

func TestCounterIncrease_SingleReset(t *testing.T) {
	samples := makeSamples(100, 150, 10, 50)
	got := counterIncrease(samples, 0, 3)
	assert.Equal(t, float64(100), got)
}

func TestCounterIncrease_MultipleResets(t *testing.T) {
	samples := makeSamples(100, 150, 20, 80, 5, 60)
	got := counterIncrease(samples, 0, 5)
	assert.Equal(t, float64(190), got)
}

func TestCounterIncrease_PostResetStart(t *testing.T) {
	samples := makeSamples(10, 30, 50)
	got := counterIncrease(samples, 0, 2)
	assert.Equal(t, float64(40), got)
}

func TestCounterIncrease_AllNegative(t *testing.T) {
	// All values decreasing: algorithm treats each drop as a reset.
	// Compensated: (10-100) + 100 + 50 = 60
	samples := makeSamples(100, 50, 10)
	got := counterIncrease(samples, 0, 2)
	assert.Equal(t, float64(60), got)
}

func TestComputeRateInWindow_WithReset(t *testing.T) {
	samples := makeSamples(100, 150, 10, 50)
	got := computeRateInWindow(samples,
		samples[0].TimestampMs, samples[3].TimestampMs, "rate")
	assert.InDelta(t, 100.0/45.0, got, 0.001)
}

func TestComputeRateInWindow_NoReset(t *testing.T) {
	samples := makeSamples(100, 150)
	got := computeRateInWindow(samples,
		samples[0].TimestampMs, samples[1].TimestampMs, "rate")
	assert.InDelta(t, 50.0/15.0, got, 0.001)
}

func TestComputeRateInWindow_IncreaseWithReset(t *testing.T) {
	samples := makeSamples(100, 150, 10, 50)
	got := computeRateInWindow(samples,
		samples[0].TimestampMs, samples[3].TimestampMs, "increase")
	assert.Equal(t, float64(100), got)
}

func TestComputeRateInWindow_OutOfWindow(t *testing.T) {
	samples := makeSamples(100, 120, 150)
	got := computeRateInWindow(samples, 0, 100, "rate")
	assert.True(t, math.IsNaN(got))
}

func TestComputeRateInWindow_InsufficientSamples(t *testing.T) {
	samples := makeSamples(100)
	got := computeRateInWindow(samples,
		samples[0].TimestampMs, samples[0].TimestampMs, "rate")
	assert.True(t, math.IsNaN(got))
}

// ── parseTopKWrapper tests ──────────────────────────

func TestParseTopKWrapper(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantInner   string
		wantK       int
		wantIsBk    bool
	}{
		{
			name:      "topk basic",
			input:     `topk(5, metric_name{label="val"})`,
			wantInner: `metric_name{label="val"}`,
			wantK:     5,
			wantIsBk:  false,
		},
		{
			name:      "bottomk basic",
			input:     `bottomk(10, sum(rate(x[5m])))`,
			wantInner: `sum(rate(x[5m]))`,
			wantK:     10,
			wantIsBk:  true,
		},
		{
			name:      "topk with whitespace",
			input:     `topk( 3 ,  metric_name  )`,
			wantInner: `metric_name`,
			wantK:     3,
			wantIsBk:  false,
		},
		{
			name:      "topk with rate and aggregation",
			input:     `topk(5, sum(rate(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER"}[1800s])) by (span_name))`,
			wantInner: `sum(rate(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER"}[1800s])) by (span_name)`,
			wantK:     5,
			wantIsBk:  false,
		},
		{
			name:    "not topk or bottomk",
			input:   `sum(rate(x[5m]))`,
		},
		{
			name:    "empty",
			input:   ``,
		},
		{
			name:    "topk with zero K (invalid)",
			input:   `topk(0, expr)`,
		},
		{
			name:    "topk with negative K (invalid)",
			input:   `topk(-5, expr)`,
		},
		{
			name:    "topk with non-integer K (invalid)",
			input:   `topk(abc, expr)`,
		},
		{
			name:    "topk without comma (malformed)",
			input:   `topk(5)`,
		},
		{
			name:    "bottomk with aggregation inner",
			input:   `bottomk(3, avg(rate(http_requests_total[1m])) by (status_code))`,
			wantInner: `avg(rate(http_requests_total[1m])) by (status_code)`,
			wantK:     3,
			wantIsBk:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner, k, isBk := parseTopKWrapper(tt.input)
			assert.Equal(t, tt.wantK, k)
			assert.Equal(t, tt.wantIsBk, isBk)
			if tt.wantInner != "" {
				assert.Equal(t, tt.wantInner, inner)
			} else {
				assert.Empty(t, inner)
			}
		})
	}
}

// ── parsePromQL with topk/bottomk tests ─────────────

func TestParsePromQL_TopK(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantTopK   int
		wantIsBk   bool
		wantMetric string
		wantFunc   string
		wantAgg    string
		wantGB     []string
		wantDur    time.Duration
	}{
		{
			name:       "topk with sum/rate/by",
			input:      `topk(5, sum(rate(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER"}[1800s])) by (span_name))`,
			wantTopK:   5,
			wantIsBk:   false,
			wantMetric: "traces_spanmetrics_calls_total",
			wantFunc:   FnRate,
			wantAgg:    AggSum,
			wantGB:     []string{"span_name"},
			wantDur:    1800 * time.Second,
		},
		{
			name:       "bottomk with avg/rate/by",
			input:      `bottomk(3, avg(rate(http_requests_total{status_code="200"}[1m])) by (status_code))`,
			wantTopK:   3,
			wantIsBk:   true,
			wantMetric: "http_requests_total",
			wantFunc:   FnRate,
			wantAgg:    AggAvg,
			wantGB:     []string{"status_code"},
			wantDur:    1 * time.Minute,
		},
		{
			name:       "topk without aggregation (raw metric)",
			input:      `topk(10, metric_name{app="test"})`,
			wantTopK:   10,
			wantIsBk:   false,
			wantMetric: "metric_name",
			wantFunc:   "",
			wantAgg:    "",
			wantDur:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := parsePromQL(tt.input)
			assert.NoError(t, err)
			assert.NotNil(t, expr)

			assert.Equal(t, tt.wantTopK, expr.TopK, "TopK mismatch")
			assert.Equal(t, tt.wantIsBk, expr.IsBottomK, "IsBottomK mismatch")
			assert.Equal(t, tt.wantMetric, expr.MetricName, "MetricName mismatch")
			assert.Equal(t, tt.wantFunc, expr.Function, "Function mismatch")
			assert.Equal(t, tt.wantAgg, expr.Aggregation, "Aggregation mismatch")
			assert.Equal(t, tt.wantGB, expr.GroupBy, "GroupBy mismatch")
			assert.Equal(t, tt.wantDur, expr.RangeDuration, "RangeDuration mismatch")
		})
	}
}

func TestParsePromQL_HistogramQuantile_StillWorks(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantAgg      string
		wantTheta    float64
		wantMetric   string // after _bucket/_sum suffix stripping
		wantBaseName string // original metric name before stripping
		wantSub      string
	}{
		{
			name:         "basic histogram_quantile",
			input:        `histogram_quantile(0.9, sum(rate(metric_bucket[5m])) by (le))`,
			wantAgg:      AggHistogramQuantile,
			wantTheta:    0.9,
			wantMetric:   "metric",
			wantBaseName: "metric_bucket",
			wantSub:      HistogramSubBucket,
		},
		{
			name:         "histogram_quantile with server filter",
			input:        `histogram_quantile(0.99, sum(rate(traces_service_graph_request_server_seconds_bucket{server="test-java-market-service"}[1m0s])) by (le, client, server))`,
			wantAgg:      AggHistogramQuantile,
			wantTheta:    0.99,
			wantMetric:   "traces_service_graph_request_server_seconds",
			wantBaseName: "traces_service_graph_request_server_seconds_bucket",
			wantSub:      HistogramSubBucket,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := parsePromQL(tt.input)
			assert.NoError(t, err)
			assert.NotNil(t, expr)
			assert.Equal(t, tt.wantAgg, expr.Aggregation)
			assert.Equal(t, tt.wantTheta, expr.Quantile)
			assert.Equal(t, tt.wantMetric, expr.MetricName)
			assert.Equal(t, tt.wantBaseName, expr.BaseMetric)
			assert.Equal(t, tt.wantSub, expr.HistogramSub)
		})
	}
}

func TestParsePromQL_NoTopK_DefaultsZero(t *testing.T) {
	// Verify backward compatibility: TopK defaults to 0 for non-topk queries.
	expr, err := parsePromQL(`sum(rate(metric_name{app="test"}[5m])) by (app)`)
	assert.NoError(t, err)
	assert.Equal(t, 0, expr.TopK, "TopK should default to 0 for non-topk queries")
	assert.False(t, expr.IsBottomK, "IsBottomK should default to false for non-topk queries")
	assert.Equal(t, "metric_name", expr.MetricName)
	assert.Equal(t, FnRate, expr.Function)
	assert.Equal(t, AggSum, expr.Aggregation)
	assert.Equal(t, []string{"app"}, expr.GroupBy)
}

// ── applyTopK / parseVectorValue tests ───────────────

func makeVectorSample(name string, labels map[string]string, value float64) promVectorSample {
	m := promMetric{PromLabelName: name}
	for k, v := range labels {
		m[k] = v
	}
	return promVectorSample{
		Metric: m,
		Value:  []any{float64(1620000000), formatPromValue(value)},
	}
}

func TestParseVectorValue(t *testing.T) {
	tests := []struct {
		name  string
		input promVectorSample
		want  float64
	}{
		{"normal", makeVectorSample("metric", nil, 42.5), 42.5},
		{"zero", makeVectorSample("metric", nil, 0), 0},
		{"negative", makeVectorSample("metric", nil, -10.0), -10.0},
		{"NaN", promVectorSample{Value: []any{float64(0), "NaN"}}, math.NaN()},
		{"empty value", promVectorSample{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVectorValue(tt.input)
			if math.IsNaN(tt.want) {
				assert.True(t, math.IsNaN(got))
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestApplyTopK(t *testing.T) {
	// Helper to create labeled vectors
	v := func(name string, val float64) promVectorSample {
		return makeVectorSample(name, nil, val)
	}

	t.Run("topk with k less than len", func(t *testing.T) {
		vectors := []promVectorSample{
			v("a", 10), v("b", 30), v("c", 20), v("d", 50), v("e", 40),
		}
		got := applyTopK(3, false, vectors)
		assert.Len(t, got, 3)
		// Should be top 3 by value: 50, 40, 30
		assert.Equal(t, float64(50), parseVectorValue(got[0]))
		assert.Equal(t, float64(40), parseVectorValue(got[1]))
		assert.Equal(t, float64(30), parseVectorValue(got[2]))
	})

	t.Run("bottomk with k less than len", func(t *testing.T) {
		vectors := []promVectorSample{
			v("a", 10), v("b", 30), v("c", 20), v("d", 50), v("e", 40),
		}
		got := applyTopK(3, true, vectors)
		assert.Len(t, got, 3)
		// Should be bottom 3 by value: 10, 20, 30
		assert.Equal(t, float64(10), parseVectorValue(got[0]))
		assert.Equal(t, float64(20), parseVectorValue(got[1]))
		assert.Equal(t, float64(30), parseVectorValue(got[2]))
	})

	t.Run("k greater than len returns all", func(t *testing.T) {
		vectors := []promVectorSample{v("a", 10), v("b", 30), v("c", 20)}
		got := applyTopK(10, false, vectors)
		assert.Len(t, got, 3)
	})

	t.Run("k equals len returns all", func(t *testing.T) {
		vectors := []promVectorSample{v("a", 10), v("b", 30), v("c", 20)}
		got := applyTopK(3, false, vectors)
		assert.Len(t, got, 3)
	})

	t.Run("empty vectors", func(t *testing.T) {
		got := applyTopK(5, false, []promVectorSample{})
		assert.Len(t, got, 0)
	})

	t.Run("zero k returns all", func(t *testing.T) {
		vectors := []promVectorSample{v("a", 10), v("b", 30)}
		got := applyTopK(0, false, vectors)
		assert.Len(t, got, 2)
	})

	t.Run("stable sort for equal values", func(t *testing.T) {
		vectors := []promVectorSample{
			v("first", 10), v("second", 10), v("third", 10),
		}
		got := applyTopK(2, false, vectors)
		assert.Len(t, got, 2)
		// Stable sort preserves original order for equal values
		assert.Equal(t, "first", got[0].Metric[PromLabelName])
		assert.Equal(t, "second", got[1].Metric[PromLabelName])
	})
}

// ── applyAggregation regression: labels preserved after groupBy ──

func TestApplyAggregation_PreservesGroupByLabels(t *testing.T) {
	tests := []struct {
		name     string
		fn       string
		groupBy  []string
		input    []promVectorSample
		wantKeys []string // expected label keys after stripMetricToGroupBy
	}{
		{
			name:    "sum by single label",
			fn:      AggSum,
			groupBy: []string{"client"},
			input: []promVectorSample{
				makeVectorSample("metric", map[string]string{"client": "a", "server": "x"}, 10),
				makeVectorSample("metric", map[string]string{"client": "a", "server": "y"}, 20),
				makeVectorSample("metric", map[string]string{"client": "b", "server": "z"}, 30),
			},
			wantKeys: []string{"client"},
		},
		{
			name:    "sum by two labels",
			fn:      AggSum,
			groupBy: []string{"client", "server"},
			input: []promVectorSample{
				makeVectorSample("metric", map[string]string{"client": "a", "server": "x"}, 10),
				makeVectorSample("metric", map[string]string{"client": "a", "server": "x"}, 20),
				makeVectorSample("metric", map[string]string{"client": "b", "server": "y"}, 30),
			},
			wantKeys: []string{"client", "server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyAggregation(tt.fn, tt.groupBy, tt.input)
			assert.NotEmpty(t, result)

			// Verify aggregated values are correct
			if tt.name == "sum by single label" {
				assert.Len(t, result, 2) // two groups: client=a and client=b
				for _, v := range result {
					assert.Contains(t, v.Metric, "client", "client label should exist after aggregation")
				}
			}
			if tt.name == "sum by two labels" {
				assert.Len(t, result, 2) // two groups: (a,x) and (b,y)
				for _, v := range result {
					assert.Contains(t, v.Metric, "client", "client label should exist")
					assert.Contains(t, v.Metric, "server", "server label should exist")
				}
			}

			// stripMetricToGroupBy should keep only groupBy labels
			stripMetricToGroupBy(result, tt.groupBy)
			for _, v := range result {
				assert.Len(t, v.Metric, len(tt.groupBy), "stripMetricToGroupBy should keep exactly groupBy labels")
				for _, k := range tt.wantKeys {
					assert.Contains(t, v.Metric, k, "label %s should be preserved", k)
				}
			}
		})
	}
}

func TestFilterMetricByKeys(t *testing.T) {
	m := promMetric{"client": "a", "server": "b", "extra": "c"}
	got := filterMetricByKeys(m, []string{"client", "server"})
	assert.Len(t, got, 2)
	assert.Equal(t, "a", got["client"])
	assert.Equal(t, "b", got["server"])
	assert.NotContains(t, got, "extra")
}
