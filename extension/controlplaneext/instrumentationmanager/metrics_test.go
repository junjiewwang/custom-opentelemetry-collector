// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestUpdateUnconfirmedMetricsClassification(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestInstrumentationServiceWithAgentTTL(t, 5*time.Second, time.Second)

	makeRule := func(id string, deleted, everApply, confirmed bool, withTargets bool) *Rule {
		r := minimalRuleForStore(id)
		if deleted {
			r.DesiredState = RuleDesiredStateDeleted
		}
		r.EverApplySucceeded = everApply
		r.UninstallConfirmed = confirmed
		require.NoError(t, svc.store.SaveRule(ctx, r, true))
		if withTargets {
			require.NoError(t, svc.store.SaveTargetStatuses(ctx, id, []*RuleTargetStatus{
				{RuleID: id, AgentID: "agent-1", State: TargetStateRemoved},
			}))
		}
		return r
	}

	// manual_review: no targets, but the rule once applied successfully, so
	// the control plane cannot prove the probe is clean.
	manual := makeRule("rule-manual-1", true, true, false, false)
	manual2 := makeRule("rule-manual-2", true, true, false, false)
	// evidence_pending: still awaiting backfill / a snapshot round.
	pendingNeverApplied := makeRule("rule-pending-1", true, false, false, false)
	pendingWithTargets := makeRule("rule-pending-2", true, true, false, true)
	// Not counted at all.
	confirmed := makeRule("rule-confirmed", true, true, true, false)
	active := makeRule("rule-active", false, true, false, false)

	rules := []*Rule{manual, manual2, pendingNeverApplied, pendingWithTargets, confirmed, active, nil}
	manualReview, pending := svc.updateUnconfirmedMetrics(ctx, rules)
	assert.Equal(t, int64(2), manualReview)
	assert.Equal(t, int64(2), pending)

	gotManual, gotPending := svc.metrics.counts()
	assert.Equal(t, int64(2), gotManual)
	assert.Equal(t, int64(2), gotPending)

	// Once a rule is confirmed it drops out of the gauge on the next sweep.
	manual.UninstallConfirmed = true
	require.NoError(t, svc.store.SaveRule(ctx, manual, false))
	all, err := svc.store.ListRules(ctx, ListRulesQuery{IncludeDeleted: true})
	require.NoError(t, err)
	svc.updateUnconfirmedMetrics(ctx, all)
	gotManual, gotPending = svc.metrics.counts()
	assert.Equal(t, int64(1), gotManual)
	assert.Equal(t, int64(2), gotPending)
}

func TestInstrumentationMetricsNilProviderIsSafe(t *testing.T) {
	m := newInstrumentationMetrics(nil)
	m.setUnconfirmedCounts(3, 7)
	a, b := m.counts()
	assert.Equal(t, int64(3), a)
	assert.Equal(t, int64(7), b)
	var nilMetrics *instrumentationMetrics
	nilMetrics.setUnconfirmedCounts(1, 1)
	a, b = nilMetrics.counts()
	assert.Equal(t, int64(0), a)
	assert.Equal(t, int64(0), b)
}

func TestUnconfirmedRulesGaugeExportsBothReasons(t *testing.T) {
	ctx := context.Background()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	defer func() { _ = mp.Shutdown(ctx) }()

	m := newInstrumentationMetrics(mp)
	m.setUnconfirmedCounts(4, 1)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	observed := map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		if scope.Scope.Name != "otelcol/instrumentation" {
			continue
		}
		for _, mtr := range scope.Metrics {
			if mtr.Name != unconfirmedRulesMetricName {
				continue
			}
			gauge, ok := mtr.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "expected an Int64 gauge")
			for _, point := range gauge.DataPoints {
				reason, ok := point.Attributes.Value("reason")
				require.True(t, ok)
				observed[reason.AsString()] = point.Value
			}
		}
	}
	assert.Equal(t, int64(4), observed[reasonManualReview])
	assert.Equal(t, int64(1), observed[reasonEvidencePending])
}
