// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	relaySessionsMetricName       = "otelcol_arthas_relay_sessions"
	firstFrameMetricName          = "otelcol_arthas_relay_first_frame_seconds"
	activeRelaySessionsMetricName = "otelcol_arthas_active_relay_sessions"
	registeredAgentsMetricName    = "otelcol_arthas_registered_agents"
	pendingConnectionsMetricName  = "otelcol_arthas_pending_connections"
	connectDurationMetricName     = "otelcol_arthas_connect_duration_ms"
	connectFailuresMetricName     = "otelcol_arthas_connect_failures"

	// Attribute keys are low-cardinality by design. Never put agent IDs,
	// app names, session IDs or free-form reasons on a metric.
	metricAttrResult = "result"
	metricAttrPhase  = "phase"
	metricAttrStage  = "stage"
	metricAttrMode   = "mode"

	// minCountableRelaySession excludes sessions where the user closed the
	// panel immediately, which would otherwise dilute the silent-session rate.
	minCountableRelaySession = 3 * time.Second
)

// connectPhase identifies a stage of handleConnectArthas.
type connectPhase string

const (
	phaseWaitAgent      connectPhase = "wait_agent"
	phaseStartTunnel    connectPhase = "start_tunnel"
	phaseWaitOpenTunnel connectPhase = "wait_open_tunnel"
	phaseRelaySetup     connectPhase = "relay_setup"
)

// connectStage identifies a failure stage. Values are a fixed enum so the
// metric stays low-cardinality; the original error text only goes to logs.
type connectStage string

const (
	stageLookup      connectStage = "lookup"
	stageStartTunnel connectStage = "start_tunnel"
	stageOpenTunnel  connectStage = "open_tunnel"
	stageTimeout     connectStage = "timeout"
	stageProxy       connectStage = "proxy"
)

// relaySessionObserver is notified once when a relay session ends.
// Keeping it as a function makes the metric layer independently testable and
// leaves every existing relayWebSocketPair caller unchanged.
type relaySessionObserver func(stats *relayStats, duration time.Duration)

// relayMetrics holds self-observability instruments for the Arthas tunnel.
// All instruments are no-op safe when no MeterProvider is configured.
type relayMetrics struct {
	sessions           metric.Int64Counter
	firstFrameSeconds  metric.Float64Histogram
	activeSessions     metric.Int64UpDownCounter
	pendingConnections metric.Int64UpDownCounter
	connectDuration    metric.Int64Histogram
	connectFailures    metric.Int64Counter

	// localAgentCount is supplied by the extension after compat is built.
	// The gauge callback reads it directly (O(1), no network IO).
	localAgentCount atomic.Pointer[func() int64]
}

func newRelayMetrics(meter metric.Meter) *relayMetrics {
	if meter == nil {
		meter = noop.Meter{}
	}
	m := &relayMetrics{}
	var err error

	if m.sessions, err = meter.Int64Counter(
		relaySessionsMetricName,
		metric.WithDescription("Arthas terminal relay sessions, labeled by whether the agent sent frames"),
		metric.WithUnit("{session}"),
	); err != nil {
		m.sessions, _ = noop.Meter{}.Int64Counter(relaySessionsMetricName)
	}

	if m.firstFrameSeconds, err = meter.Float64Histogram(
		firstFrameMetricName,
		metric.WithDescription("Time from relay start to the first frame written by the agent"),
		metric.WithUnit("s"),
	); err != nil {
		m.firstFrameSeconds, _ = noop.Meter{}.Float64Histogram(firstFrameMetricName)
	}

	if m.activeSessions, err = meter.Int64UpDownCounter(
		activeRelaySessionsMetricName,
		metric.WithDescription("Number of Arthas terminal relay sessions currently active on this node"),
		metric.WithUnit("{session}"),
	); err != nil {
		m.activeSessions, _ = noop.Meter{}.Int64UpDownCounter(activeRelaySessionsMetricName)
	}

	if m.pendingConnections, err = meter.Int64UpDownCounter(
		pendingConnectionsMetricName,
		metric.WithDescription("Number of in-flight connectArthas requests on this node"),
		metric.WithUnit("{request}"),
	); err != nil {
		m.pendingConnections, _ = noop.Meter{}.Int64UpDownCounter(pendingConnectionsMetricName)
	}

	if m.connectDuration, err = meter.Int64Histogram(
		connectDurationMetricName,
		metric.WithDescription("Duration of connectArthas stages"),
		metric.WithUnit("ms"),
	); err != nil {
		m.connectDuration, _ = noop.Meter{}.Int64Histogram(connectDurationMetricName)
	}

	if m.connectFailures, err = meter.Int64Counter(
		connectFailuresMetricName,
		metric.WithDescription("Failed connectArthas attempts by stage"),
		metric.WithUnit("{request}"),
	); err != nil {
		m.connectFailures, _ = noop.Meter{}.Int64Counter(connectFailuresMetricName)
	}

	_, err = meter.Int64ObservableGauge(
		registeredAgentsMetricName,
		metric.WithDescription("Healthy agents registered with the Arthas tunnel on this node"),
		metric.WithUnit("{agent}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			count := int64(0)
			if fn := m.localAgentCount.Load(); fn != nil {
				count = (*fn)()
			}
			o.Observe(count)
			return nil
		}),
	)
	if err != nil {
		// A gauge registration failure must not break terminal connections.
		_, _ = noop.Meter{}.Int64ObservableGauge(registeredAgentsMetricName)
	}

	return m
}

// setLocalAgentCount wires the gauge source without making metrics depend on
// the compat implementation.
func (m *relayMetrics) setLocalAgentCount(fn func() int64) {
	if m == nil || fn == nil {
		return
	}
	m.localAgentCount.Store(&fn)
}

// recordSession classifies a completed relay session as ok/silent and records
// the first-frame latency (or a timeout sample when the agent never replied).
func (m *relayMetrics) recordSession(stats *relayStats, duration time.Duration) {
	if m == nil || stats == nil {
		return
	}
	latency, ok := stats.firstFrameLatency()
	if ok {
		m.firstFrameSeconds.Record(context.Background(), latency.Seconds(),
			metric.WithAttributes(attribute.String(metricAttrResult, "ok")))
	} else if duration >= minCountableRelaySession {
		m.firstFrameSeconds.Record(context.Background(), duration.Seconds(),
			metric.WithAttributes(attribute.String(metricAttrResult, "timeout")))
	}

	if duration < minCountableRelaySession {
		return
	}
	result := "ok"
	if stats.tunnelFrames.Load() == 0 {
		result = "silent"
	}
	m.sessions.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String(metricAttrResult, result)))
}

// relayStarted/relayFinished must be paired at the relay call site so the
// gauge stays balanced even if relayWebSocketPair returns before notifying
// observers (e.g. a nil connection).
func (m *relayMetrics) relayStarted() {
	if m == nil {
		return
	}
	m.activeSessions.Add(context.Background(), 1)
}

func (m *relayMetrics) relayFinished() {
	if m == nil {
		return
	}
	m.activeSessions.Add(context.Background(), -1)
}

func (m *relayMetrics) pendingStarted(mode string) {
	if m == nil {
		return
	}
	m.pendingConnections.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String(metricAttrMode, mode)))
}

func (m *relayMetrics) pendingStopped(mode string) {
	if m == nil {
		return
	}
	m.pendingConnections.Add(context.Background(), -1,
		metric.WithAttributes(attribute.String(metricAttrMode, mode)))
}

func (m *relayMetrics) recordPhase(phase connectPhase, d time.Duration) {
	if m == nil {
		return
	}
	m.connectDuration.Record(context.Background(), d.Milliseconds(),
		metric.WithAttributes(attribute.String(metricAttrPhase, string(phase))))
}

func (m *relayMetrics) recordFailure(stage connectStage) {
	if m == nil {
		return
	}
	m.connectFailures.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String(metricAttrStage, string(stage))))
}
