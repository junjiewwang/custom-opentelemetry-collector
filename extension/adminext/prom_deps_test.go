// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

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
