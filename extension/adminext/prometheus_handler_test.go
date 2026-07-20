// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

func TestDetectHistogramSub(t *testing.T) {
	tests := []struct {
		input   string
		wantSub string
		wantOK  bool
	}{
		{"traces_service_graph_request_server_seconds_sum", "sum", true},
		{"traces_service_graph_request_server_seconds_bucket", "bucket", true},
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
		expr := &promqlExpr{Labels: map[string]string{"le": "0.005"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(5), v)
	})

	t.Run("match second bucket", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{"le": "0.05"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(2), v)
	})

	t.Run("no le label returns sum", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, 100.0, v)
	})

	t.Run("le not found in bounds", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{"le": "0.001"}}
		v := resolveHistogramBucket(dp, expr)
		assert.Equal(t, float64(0), v)
	})

	t.Run("invalid le format", func(t *testing.T) {
		expr := &promqlExpr{Labels: map[string]string{"le": "abc"}}
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
		Aggregation: "sum",
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
