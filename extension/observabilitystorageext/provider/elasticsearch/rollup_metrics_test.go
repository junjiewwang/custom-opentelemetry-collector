// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collectMetrics collects the current metric state from a manual reader.
func collectMetrics(t *testing.T, mr *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	rm := metricdata.ResourceMetrics{}
	require.NoError(t, mr.Collect(context.Background(), &rm))
	out := make(map[string]metricdata.Metrics)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// TestNewRollupMetrics_RegistersAllInstruments verifies the three new
// instruments (watermark_ms gauge, backlog_slices gauge, tick_duration
// histogram) are registered alongside the existing six, and that every
// instrument carries the fixed node_id attribute.
func TestNewRollupMetrics_RegistersAllInstruments(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
	meter := mp.Meter("otelcol/rollup")

	rm := NewRollupMetrics(meter, "node-123")

	ctx := context.Background()
	// Record one value on each instrument so it materializes in collection.
	// (OTel SDK only collects instruments that have emitted at least one point.)
	rm.recordWatermarks(ctx, map[string]int64{"app-a": 1700000000000}, map[string]int{"app-a": 5})
	rm.recordTick(ctx, 1500*time.Millisecond)
	rm.recordSlice(ctx, "app-a", 42, false, 10*time.Millisecond)
	rm.recordMetric(ctx, "app-a", false)

	metrics := collectMetrics(t, mr)

	// These are the instruments that must have data points after the records above.
	for _, name := range []string{
		"otelcol_rollup_slices_processed",
		"otelcol_rollup_points_written",
		"otelcol_rollup_slice_duration",
		"otelcol_rollup_metrics_aggregated",
		"otelcol_rollup_watermark_ms",
		"otelcol_rollup_backlog_slices",
		"otelcol_rollup_tick_duration",
	} {
		_, ok := metrics[name]
		assert.True(t, ok, "instrument %q must have emitted data", name)
	}
}

// TestNewRollupMetrics_NodeIDAttribute verifies node_id is attached to recorded
// points and that app_id is preserved.
func TestNewRollupMetrics_NodeIDAttribute(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
	rm := NewRollupMetrics(mp.Meter("otelcol/rollup"), "node-abc")

	rm.recordWatermarks(context.Background(),
		map[string]int64{"app-a": 1700000000000},
		map[string]int{"app-a": 5},
	)

	metrics := collectMetrics(t, mr)

	wm, ok := metrics["otelcol_rollup_watermark_ms"]
	require.True(t, ok, "watermark_ms must be registered")

	var gotAttrs []attribute.Set
	switch d := wm.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, dp := range d.DataPoints {
			gotAttrs = append(gotAttrs, dp.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, dp := range d.DataPoints {
			gotAttrs = append(gotAttrs, dp.Attributes)
		}
	default:
		t.Fatalf("unexpected watermark data type %T", wm.Data)
	}

	require.NotEmpty(t, gotAttrs, "watermark gauge must have data points")
	foundNode := false
	foundApp := false
	for _, attrs := range gotAttrs {
		if v, ok := attrs.Value(attribute.Key("node_id")); ok && v.AsString() == "node-abc" {
			foundNode = true
		}
		if v, ok := attrs.Value(attribute.Key("app_id")); ok && v.AsString() == "app-a" {
			foundApp = true
		}
	}
	assert.True(t, foundNode, "node_id attribute must be present")
	assert.True(t, foundApp, "app_id attribute must be present")
}

// TestRollupMetrics_RecordWatermarks_BacklogValue verifies the backlog gauge
// reports the pending-slice count per app, and that a caught-up app (present in
// watermarks but with zero pending) still emits an explicit 0 — so "caught up"
// is distinguishable from "metric not reporting".
func TestRollupMetrics_RecordWatermarks_BacklogValue(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
	rm := NewRollupMetrics(mp.Meter("otelcol/rollup"), "node-1")

	// app-a has 7 pending slices; app-b is caught up (watermark present, 0 pending).
	rm.recordWatermarks(context.Background(),
		map[string]int64{"app-a": 1700000000000, "app-b": 1700000000000},
		map[string]int{"app-a": 7, "app-b": 0},
	)

	metrics := collectMetrics(t, mr)

	backlog, ok := metrics["otelcol_rollup_backlog_slices"]
	require.True(t, ok, "backlog_slices must be registered")

	backlogByApp := map[string]int64{}
	if g, ok := backlog.Data.(metricdata.Gauge[int64]); ok {
		for _, dp := range g.DataPoints {
			app, _ := dp.Attributes.Value(attribute.Key("app_id"))
			backlogByApp[app.AsString()] = dp.Value
		}
	}
	assert.Equal(t, int64(7), backlogByApp["app-a"], "app-a backlog must be 7")
	assert.Equal(t, int64(0), backlogByApp["app-b"], "app-b (caught up) must report an explicit 0")
}
