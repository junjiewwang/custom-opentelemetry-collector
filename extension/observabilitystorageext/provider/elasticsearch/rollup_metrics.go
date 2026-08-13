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
}

// NewRollupMetrics builds rollup self-monitoring instruments from the meter.
func NewRollupMetrics(meter metric.Meter) *rollupMetrics {
	rm := &rollupMetrics{}
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
	return rm
}

// attrAppID returns the appID attribute used on all rollup metrics.
func attrAppID(appID string) attribute.KeyValue {
	return attribute.String("app_id", appID)
}

// recordSlice records the outcome of one hour-slice aggregation.
func (m *rollupMetrics) recordSlice(ctx context.Context, appID string, points int, failed bool, dur time.Duration) {
	attr := attrAppID(appID)
	if failed {
		m.slicesFailed.Add(ctx, 1, metric.WithAttributes(attr))
		return
	}
	m.slicesProcessed.Add(ctx, 1, metric.WithAttributes(attr))
	m.pointsWritten.Add(ctx, int64(points), metric.WithAttributes(attr))
	m.sliceDuration.Record(ctx, dur.Milliseconds(), metric.WithAttributes(attr))
}

// recordMetric records the outcome of one metric-name aggregation.
func (m *rollupMetrics) recordMetric(ctx context.Context, appID string, failed bool) {
	attr := attrAppID(appID)
	if failed {
		m.metricsFailed.Add(ctx, 1, metric.WithAttributes(attr))
		return
	}
	m.metricsAggregated.Add(ctx, 1, metric.WithAttributes(attr))
}
