// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// queryMetrics holds the self-monitoring instruments for the Prometheus query
// layer. All fields are optional (nil-safe): when the extension has no
// MeterProvider, a no-op meter is used so callers never need to nil-check.
//
// Modeled on the rollup engine's rollupMetrics (elasticsearch/rollup_metrics.go),
// which these metrics are meant to be queried alongside: both carry the same
// node_id attribute so a distributed deployment can tell which replica served
// which query.
//
// Scope is deliberately narrow: only query latency (the one signal a Grafana
// user directly perceives). ES request count / 429 retries live in the
// observabilitystorageext MetricReader (shared by rollup + query paths), so
// instrumenting them here would conflate the two consumers and blur the
// attribution we actually want.
type queryMetrics struct {
	// nodeAttr is the fixed node_id attribute applied to every instrument.
	nodeAttr attribute.KeyValue

	// queryDuration records the wall-clock latency of a Prometheus query
	// (instant or range), labeled by datasource and route (engine vs subset).
	queryDuration metric.Int64Histogram
}

// newQueryMetrics builds query-layer self-monitoring instruments from the meter.
// A nil meter is tolerated (no-op instruments) so the handler can be built even
// when the collector runs without a MeterProvider.
func newQueryMetrics(meter metric.Meter, nodeID string) *queryMetrics {
	if meter == nil {
		meter = noop.Meter{}
	}
	qm := &queryMetrics{}
	qm.nodeAttr = attribute.String("node_id", nodeID)
	qm.queryDuration, _ = meter.Int64Histogram(
		"otelcol_query_duration_ms",
		metric.WithDescription("Latency of Prometheus-compatible query requests"),
		metric.WithUnit("ms"),
	)
	return qm
}

// recordQuery records one Prometheus query's latency, labeled by datasource and
// the routing path that served it (engine = PromQL engine, subset = subset parser).
func (m *queryMetrics) recordQuery(ctx context.Context, datasource, route string, dur time.Duration) {
	attrs := metric.WithAttributes(
		attribute.String("datasource", datasource),
		attribute.String("route", route),
		m.nodeAttr,
	)
	m.queryDuration.Record(ctx, dur.Milliseconds(), attrs)
}
