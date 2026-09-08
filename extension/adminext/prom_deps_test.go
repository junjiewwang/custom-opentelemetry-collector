// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestIsComplexPromQL_HistogramSubSeriesBypass verifies that histogram _bucket/_sum
// sub-series queries bypass the PromQL engine (routed to the subset parser, which
// has the correct delta-aware expansion), while _count-suffixed names and plain
// rate queries still reach the engine.
func TestIsComplexPromQL_HistogramSubSeriesBypass(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		// Histogram _bucket/_sum sub-series → bypass engine (false).
		{"rate bucket", "sum by (le) (rate(otelcol_rollup_tick_duration_bucket[5m]))", false},
		{"rate sum", "rate(otelcol_rollup_tick_duration_sum[5m])", false},
		{"bare bucket selector", "otelcol_rollup_tick_duration_bucket", false},
		// _count is NOT bypassed: it must reach the engine so jvm_thread_count →
		// jvm.thread.count maps correctly (collides with non-histogram gauges).
		{"rate count still engine", "rate(jvm_thread_count[5m])", true},
		// Non-histogram rate still engine.
		{"plain rate", "rate(otelcol_rollup_slices_processed[5m])", true},
		{"increase", "increase(kafka_consumer_bytes_consumed_total[5m])", true},
		{"increase wrapped", "sum(increase(kafka_consumer_bytes_consumed_total[5m]))", true},
		{"division", "a / b", true},
		// No range function, no division, no suffix → subset parser.
		{"plain selector", "otelcol_rollup_slices_processed", false},
		{"aggregation", "sum by (x) (some_metric)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isComplexPromQL(tt.query))
		})
	}
}

func TestIsHistogramSubSeriesQuery(t *testing.T) {
	assert.True(t, isHistogramSubSeriesQuery("rate(x_bucket[5m])"))
	assert.True(t, isHistogramSubSeriesQuery("rate(x_sum[5m])"))
	assert.False(t, isHistogramSubSeriesQuery("rate(x_count[5m])")) // _count excluded
	assert.False(t, isHistogramSubSeriesQuery("rate(x[5m])"))
}

func TestIsRateFunc(t *testing.T) {
	for _, fn := range []string{"rate", "increase", "irate", "delta", "deriv", "idelta"} {
		assert.True(t, isRateFunc(fn), "%s must be raw-only", fn)
	}
	for _, fn := range []string{"avg", "sum", "max", "min", "count", "", "count_over_time", "histogram_quantile"} {
		assert.False(t, isRateFunc(fn), "%s must NOT be raw-only (gauge aggregation needs rollup)", fn)
	}
}

// TestTryPromQL_HistogramSubSeriesShortCircuits verifies tryPromQL returns nil
// immediately (without invoking the engine) for _bucket/_sum queries, so the
// instant path no longer logs a wasted parse ERROR for every histogram_quantile
// sub-query before falling through to the subset parser.
func TestTryPromQL_HistogramSubSeriesShortCircuits(t *testing.T) {
	h := &promHandlers{engine: nil, queryable: nil} // nil engine: would panic if it tried to use it
	// A histogram_quantile over _bucket — must short-circuit before NewInstantQuery.
	result, failReason := h.tryPromQL(context.Background(),
		`histogram_quantile(.9, sum(rate(traces_spanmetrics_latency_bucket{span_name=~"opentelemetry\\.proto\\.collector\\.logs\\.v1\\.LogsService/Export"}[3600s])) by (le))`,
		time.Now())
	assert.Nil(t, result, "histogram _bucket query must short-circuit without calling the engine")
	assert.Empty(t, failReason, "short-circuit is a shape decision, not a cancellation")
}

// TestNormalizeQueryForPromQL_PreservesLabelValues verifies that dots inside
// quoted label values are NOT rewritten to underscores (they are not metric-name
// separators). A span_name like "market.MarketService/GetAllProductInfo" must
// survive normalization unchanged, or the exact label matcher built downstream
// matches nothing and the query returns empty.
func TestNormalizeQueryForPromQL_PreservesLabelValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "label value with dots preserved",
			in:   `traces_spanmetrics_calls_total{span_name="market.MarketService/GetAllProductInfo"}`,
			want: `traces_spanmetrics_calls_total{span_name="market.MarketService/GetAllProductInfo"}`,
		},
		{
			name: "dotted metric name still normalized",
			in:   `jvm.memory.used{service="a"}`,
			want: `jvm_memory_used{service="a"}`,
		},
		{
			name: "regex label value preserved",
			in:   `traces_spanmetrics_calls_total{span_name=~".*GetAllProductInfo.*"}`,
			want: `traces_spanmetrics_calls_total{span_name=~".*GetAllProductInfo.*"}`,
		},
		{
			name: "rate query with dotted label value preserved",
			in:   `sum(rate(traces_spanmetrics_calls_total{span_name="market.MarketService/GetAllProductInfo"}[1m]))`,
			want: `sum(rate(traces_spanmetrics_calls_total{span_name="market.MarketService/GetAllProductInfo"}[1m]))`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeQueryForPromQL(tt.in))
		})
	}
}
