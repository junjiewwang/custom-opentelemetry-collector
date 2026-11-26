// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectMetrics(t *testing.T, reader sdkmetric.Reader) map[string]metricdata.Aggregation {
	t.Helper()
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

func sumByResult(t *testing.T, agg metricdata.Aggregation) map[string]int64 {
	t.Helper()
	out := make(map[string]int64)
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		return out
	}
	for _, dp := range sum.DataPoints {
		v, found := dp.Attributes.Value(attribute.Key(metricAttrResult))
		if found {
			out[v.AsString()] += dp.Value
		}
	}
	return out
}

func histogramByAttribute(t *testing.T, agg metricdata.Aggregation, key string) map[string]uint64 {
	t.Helper()
	out := make(map[string]uint64)
	hist, ok := agg.(metricdata.Histogram[float64])
	if ok {
		for _, dp := range hist.DataPoints {
			v, found := dp.Attributes.Value(attribute.Key(key))
			if found {
				out[v.AsString()] += dp.Count
			}
		}
		return out
	}
	ihist, ok := agg.(metricdata.Histogram[int64])
	if ok {
		for _, dp := range ihist.DataPoints {
			v, found := dp.Attributes.Value(attribute.Key(key))
			if found {
				out[v.AsString()] += dp.Count
			}
		}
	}
	return out
}

func TestRelayMetricsClassifiesSilentAndOkSessions(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))

	silent := newRelayStats(time.Now().Add(-10 * time.Second))
	silent.record("browser", 39)
	m.recordSession(silent, 10*time.Second)

	ok := newRelayStats(time.Now().Add(-10 * time.Second))
	ok.record("tunnel", 126)
	m.recordSession(ok, 10*time.Second)

	metrics := collectMetrics(t, reader)
	require.Equal(t, int64(1), sumByResult(t, metrics[relaySessionsMetricName])["silent"])
	require.Equal(t, int64(1), sumByResult(t, metrics[relaySessionsMetricName])["ok"])
}

func TestRelayMetricsFirstFrameHistogram(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))

	ok := newRelayStats(time.Now().Add(-2 * time.Second))
	ok.record("tunnel", 126)
	m.recordSession(ok, 2*time.Second)

	silent := newRelayStats(time.Now().Add(-8 * time.Second))
	silent.record("browser", 39)
	m.recordSession(silent, 8*time.Second)

	counts := histogramByAttribute(t, collectMetrics(t, reader)[firstFrameMetricName], metricAttrResult)
	require.Equal(t, uint64(1), counts["ok"])
	require.Equal(t, uint64(1), counts["timeout"])
}

func TestRelayMetricsIgnoresShortSessionsAndNil(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))

	s := newRelayStats(time.Now().Add(-time.Second))
	m.recordSession(s, minCountableRelaySession-time.Nanosecond)
	m.recordSession(nil, time.Hour)
	var nilMetrics *relayMetrics
	nilMetrics.recordSession(s, time.Hour)

	metrics := collectMetrics(t, reader)
	require.Empty(t, sumByResult(t, metrics[relaySessionsMetricName]))
	require.Empty(t, histogramByAttribute(t, metrics[firstFrameMetricName], metricAttrResult))
}

func TestRelayMetricsActiveSessionsBalanced(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))

	m.relayStarted()
	m.relayStarted()
	m.relayFinished()
	s := newRelayStats(time.Now().Add(-4 * time.Second))
	s.record("tunnel", 10)
	m.recordSession(s, 4*time.Second)
	require.NoError(t, mp.ForceFlush(context.Background()))

	sum, ok := collectMetrics(t, reader)[activeRelaySessionsMetricName].(metricdata.Sum[int64])
	require.True(t, ok)
	total := int64(0)
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	require.Equal(t, int64(1), total)
}

func TestRelayMetricsGaugeUsesInjectedCount(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))
	m.setLocalAgentCount(func() int64 { return 7 })

	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	found := false
	for _, sm := range data.ScopeMetrics {
		for _, metric := range sm.Metrics {
			if metric.Name != registeredAgentsMetricName {
				continue
			}
			found = true
			gauge, ok := metric.Data.(metricdata.Gauge[int64])
			require.True(t, ok)
			require.Len(t, gauge.DataPoints, 1)
			require.Equal(t, int64(7), gauge.DataPoints[0].Value)
		}
	}
	require.True(t, found, "registered agents gauge must be registered")
}

func TestRelayMetricsPendingAndPhases(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := newRelayMetrics(mp.Meter("otelcol/arthas_tunnel"))

	m.pendingStarted("local")
	m.pendingStopped("local")
	m.recordPhase(phaseWaitAgent, 12*time.Millisecond)
	m.recordPhase(phaseWaitOpenTunnel, 34*time.Millisecond)
	m.recordFailure(stageTimeout)
	m.recordFailure(stageOpenTunnel)

	metrics := collectMetrics(t, reader)
	phases := histogramByAttribute(t, metrics[connectDurationMetricName], metricAttrPhase)
	require.Equal(t, uint64(1), phases[string(phaseWaitAgent)])
	require.Equal(t, uint64(1), phases[string(phaseWaitOpenTunnel)])

	failures, ok := metrics[connectFailuresMetricName].(metricdata.Sum[int64])
	require.True(t, ok)
	byStage := make(map[string]int64)
	for _, dp := range failures.DataPoints {
		v, found := dp.Attributes.Value(attribute.Key(metricAttrStage))
		if found {
			byStage[v.AsString()] += dp.Value
		}
	}
	require.Equal(t, int64(1), byStage[string(stageTimeout)])
	require.Equal(t, int64(1), byStage[string(stageOpenTunnel)])

	pending, ok := metrics[pendingConnectionsMetricName].(metricdata.Sum[int64])
	require.True(t, ok)
	total := int64(0)
	for _, dp := range pending.DataPoints {
		total += dp.Value
	}
	require.Zero(t, total, "pending counter must balance to zero")
}

func TestRelayMetricsNoopSafe(t *testing.T) {
	m := newRelayMetrics(nil)
	s := newRelayStats(time.Now().Add(-time.Hour))
	require.NotPanics(t, func() {
		m.recordSession(s, time.Hour)
		m.relayStarted()
		m.relayFinished()
		m.pendingStarted("local")
		m.pendingStopped("local")
		m.recordPhase(phaseRelaySetup, time.Millisecond)
		m.recordFailure(stageLookup)
		m.setLocalAgentCount(nil)
	})
	var nilMetrics *relayMetrics
	require.NotPanics(t, func() { nilMetrics.recordFailure(stageProxy) })
}

func TestWithRelayMetricsRegistersObserver(t *testing.T) {
	m := newRelayMetrics(nil)
	s := &arthasURICompat{ctx: context.Background()}
	withRelayMetrics(nil)(s)
	require.Empty(t, s.relayObservers)
	withRelayMetrics(m)(s)
	require.Same(t, m, s.metrics)
	require.Len(t, s.relayObservers, 1)
}
