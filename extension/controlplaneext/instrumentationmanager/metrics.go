// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	// unconfirmedRulesMetricName is an observable gauge with a low-cardinality
	// "reason" attribute. Per-rule detail deliberately stays out of metrics:
	// the console's "Unconfirmed (N)" filter already presents it.
	unconfirmedRulesMetricName = "instrumentation_unconfirmed_rules"

	reasonManualReview    = "manual_review"
	reasonEvidencePending = "evidence_pending"
)

// instrumentationMetrics holds the latest unconfirmed-rule breakdown. Values
// are refreshed by the periodic GC sweep on every replica, so the observable
// gauge callback never performs store I/O while metrics are being collected.
type instrumentationMetrics struct {
	manualReview    atomic.Int64
	evidencePending atomic.Int64
}

// newInstrumentationMetrics registers the gauge. A nil MeterProvider yields a
// no-op holder so unit tests and embedders never need nil checks.
func newInstrumentationMetrics(mp metric.MeterProvider) *instrumentationMetrics {
	m := &instrumentationMetrics{}
	if mp == nil {
		return m
	}
	meter := mp.Meter("otelcol/instrumentation")
	_, err := meter.Int64ObservableGauge(
		unconfirmedRulesMetricName,
		metric.WithDescription("Number of deleted instrumentation rules still lacking uninstall evidence"),
		metric.WithUnit("{rule}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.manualReview.Load(), metric.WithAttributes(attribute.String("reason", reasonManualReview)))
			o.Observe(m.evidencePending.Load(), metric.WithAttributes(attribute.String("reason", reasonEvidencePending)))
			return nil
		}),
	)
	if err != nil {
		// Instrument registration must never break rule management.
		_, _ = noop.Meter{}.Int64ObservableGauge(unconfirmedRulesMetricName)
	}
	return m
}

func (m *instrumentationMetrics) setUnconfirmedCounts(manualReview, evidencePending int64) {
	if m == nil {
		return
	}
	m.manualReview.Store(manualReview)
	m.evidencePending.Store(evidencePending)
}

func (m *instrumentationMetrics) counts() (int64, int64) {
	if m == nil {
		return 0, 0
	}
	return m.manualReview.Load(), m.evidencePending.Load()
}

// WithMeterProvider wires the collector's MeterProvider into the rule manager.
// Without it the manager keeps a no-op holder and emits no metrics.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(s *InstrumentationService) {
		if mp != nil {
			s.metrics = newInstrumentationMetrics(mp)
		}
	}
}
