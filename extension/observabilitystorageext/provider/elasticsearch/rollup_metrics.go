// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// rollupMetrics holds the self-monitoring instruments for the 5m rollup engine.
// All fields are optional (nil-safe): when the extension has no MeterProvider,
// a no-op meter is used so callers never need to nil-check.
type rollupMetrics struct {
	// nodeAttr is the fixed node_id attribute applied to every instrument. It
	// lets a distributed deployment tell which replica is doing the work —
	// node_id is the same identifier held in Redis claim/watermark records, so
	// metrics can be cross-referenced against the coordination store.
	nodeAttr attribute.KeyValue

	// slicesProcessed counts completed hour-slice aggregations (success only).
	slicesProcessed metric.Int64Counter
	// slicesFailed counts hour-slice aggregations that errored.
	slicesFailed metric.Int64Counter
	// metricsAggregated counts metric-name aggregations (success only).
	metricsAggregated metric.Int64Counter
	// metricsFailed counts metric-name aggregations that errored.
	metricsFailed metric.Int64Counter
	// pointsWritten counts rollup documents written.
	pointsWritten metric.Int64Counter
	// sliceDuration records per-hour-slice aggregation latency.
	sliceDuration metric.Int64Histogram

	// watermarkMs reports, per app, the durable rollup watermark (unix ms) —
	// "everything older than this is durably rolled up". Reported once per tick
	// from the same GetAllWatermarks snapshot the planner already read, so it
	// costs nothing extra. `time()*1000 - watermark_ms` is the catch-up lag.
	watermarkMs metric.Int64Gauge
	// backlogSlices reports, per app, how many ready-but-unprocessed hour slices
	// remain at the start of a tick. 0 = caught up; 24 = one full day behind.
	backlogSlices metric.Int64Gauge
	// tickDuration records the wall-clock duration of a full rollup tick. It
	// balloons while the engine is catching up a backlog, so it is a direct
	// signal of "is this a normal 1-slice tick or a multi-slice backfill".
	tickDuration metric.Int64Histogram
}

// NewRollupMetrics builds rollup self-monitoring instruments from the meter.
// nodeID is the replica identifier (same as the Redis claim/watermark holder),
// applied as a fixed attribute on every metric.
func NewRollupMetrics(meter metric.Meter, nodeID string) *rollupMetrics {
	rm := &rollupMetrics{}
	rm.nodeAttr = attribute.String("node_id", nodeID)
	rm.slicesProcessed, _ = meter.Int64Counter(
		"otelcol_rollup_slices_processed",
		metric.WithDescription("Number of 5m rollup hour-slices aggregated successfully"),
	)
	rm.slicesFailed, _ = meter.Int64Counter(
		"otelcol_rollup_slices_failed",
		metric.WithDescription("Number of 5m rollup hour-slices that failed"),
	)
	rm.metricsAggregated, _ = meter.Int64Counter(
		"otelcol_rollup_metrics_aggregated",
		metric.WithDescription("Number of metric-name aggregations that succeeded"),
	)
	rm.metricsFailed, _ = meter.Int64Counter(
		"otelcol_rollup_metrics_failed",
		metric.WithDescription("Number of metric-name aggregations that failed"),
	)
	rm.pointsWritten, _ = meter.Int64Counter(
		"otelcol_rollup_points_written",
		metric.WithDescription("Number of rollup documents written"),
	)
	rm.sliceDuration, _ = meter.Int64Histogram(
		"otelcol_rollup_slice_duration",
		metric.WithDescription("Rollup hour-slice aggregation latency"),
		metric.WithUnit("ms"),
	)
	rm.watermarkMs, _ = meter.Int64Gauge(
		"otelcol_rollup_watermark_ms",
		metric.WithDescription("Per-app rollup watermark (unix ms): everything older is durably rolled up"),
	)
	rm.backlogSlices, _ = meter.Int64Gauge(
		"otelcol_rollup_backlog_slices",
		metric.WithDescription("Per-app count of ready-but-unprocessed hour slices at tick start"),
	)
	rm.tickDuration, _ = meter.Int64Histogram(
		"otelcol_rollup_tick_duration",
		metric.WithDescription("Wall-clock duration of a full rollup tick"),
		metric.WithUnit("ms"),
	)
	return rm
}

// attrAppID returns the "target app" attribute used on all rollup metrics.
//
// It is named rollup_target_app_id (NOT app_id) on purpose: the value is the
// app whose slice the engine is currently processing — a work dimension, not
// the resource identity. Naming it app_id collides with the OTel resource
// attribute app_id (which tokenauth injects as the collector's OWN app and
// which routes the doc to a per-app ES index). That collision made every
// rollup self-monitoring doc land in the collector-self index regardless of the
// target app, and made `sum by (app_id)` show phantom data for all apps.
func attrAppID(appID string) attribute.KeyValue {
	return attribute.String("rollup_target_app_id", appID)
}

// recordSlice records the outcome of one hour-slice aggregation.
func (m *rollupMetrics) recordSlice(ctx context.Context, appID string, points int, failed bool, dur time.Duration) {
	attrs := metric.WithAttributes(attrAppID(appID), m.nodeAttr)
	if failed {
		m.slicesFailed.Add(ctx, 1, attrs)
		return
	}
	m.slicesProcessed.Add(ctx, 1, attrs)
	m.pointsWritten.Add(ctx, int64(points), attrs)
	m.sliceDuration.Record(ctx, dur.Milliseconds(), attrs)
}

// recordMetric records the outcome of one metric-name aggregation.
func (m *rollupMetrics) recordMetric(ctx context.Context, appID string, failed bool) {
	attrs := metric.WithAttributes(attrAppID(appID), m.nodeAttr)
	if failed {
		m.metricsFailed.Add(ctx, 1, attrs)
		return
	}
	m.metricsAggregated.Add(ctx, 1, attrs)
}

// recordWatermarks reports, per app, the current watermark and the pending
// (ready-but-unprocessed) hour-slice count. Both are tick-start snapshots taken
// from the planner's own GetAllWatermarks result, so this adds no extra I/O.
func (m *rollupMetrics) recordWatermarks(ctx context.Context, watermarks map[string]int64, pendingByApp map[string]int) {
	for appID, wm := range watermarks {
		attrs := metric.WithAttributes(attrAppID(appID), m.nodeAttr)
		m.watermarkMs.Record(ctx, wm, attrs)
	}
	for appID, n := range pendingByApp {
		attrs := metric.WithAttributes(attrAppID(appID), m.nodeAttr)
		m.backlogSlices.Record(ctx, int64(n), attrs)
	}
}

// recordTick records the wall-clock duration of a full rollup tick.
func (m *rollupMetrics) recordTick(ctx context.Context, dur time.Duration) {
	m.tickDuration.Record(ctx, dur.Milliseconds(), metric.WithAttributes(m.nodeAttr))
}
