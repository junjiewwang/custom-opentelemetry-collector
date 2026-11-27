// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

func minimalRuleForStore(id string) *Rule {
	return &Rule{
		ID:              id,
		Name:            "verify",
		AppID:           "app-a",
		ServiceName:     "svc-a",
		ClassName:       "demo.OrderService",
		MethodName:      "submit",
		InstrumentType:  InstrumentTypeTrace,
		DesiredState:    RuleDesiredStateActive,
		CreatedAtMillis: time.Now().UnixMilli(),
		UpdatedAtMillis: time.Now().UnixMilli(),
	}
}

func TestCommitRuleCASMemory(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRuleStore()
	rule := minimalRuleForStore("rule-1")
	require.NoError(t, store.SaveRule(ctx, rule, true))
	require.Equal(t, int64(1), rule.Revision)

	first, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	second, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)

	targets := []*RuleTargetStatus{{RuleID: rule.ID, AgentID: "agent-1", State: TargetStateDispatched}}
	require.NoError(t, store.CommitRule(ctx, first, targets, first.Revision))
	assert.Equal(t, int64(2), first.Revision)

	// A stale writer must not win, and must not write its targets either.
	assert.ErrorIs(t, store.CommitRule(ctx, second, []*RuleTargetStatus{}, second.Revision), ErrRevisionConflict)
	storedTargets, err := store.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	require.Len(t, storedTargets, 1)
	assert.Equal(t, TargetStateDispatched, storedTargets[0].State)

	// Re-read and retry succeeds.
	latest, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	require.NoError(t, store.CommitRule(ctx, latest, nil, latest.Revision))
	assert.Equal(t, int64(3), latest.Revision)
}

func TestCommitRuleCASRedis(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	store := NewRedisRuleStore(zap.NewNop(), NewRedisCmd(client), "otel:verify")
	require.NoError(t, store.Start(ctx))

	rule := minimalRuleForStore("rule-redis")
	require.NoError(t, store.SaveRule(ctx, rule, true))
	require.Equal(t, int64(1), rule.Revision)

	first, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	second, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)

	require.NoError(t, store.CommitRule(ctx, first, []*RuleTargetStatus{{RuleID: rule.ID, AgentID: "agent-1", State: TargetStateApplied}}, first.Revision))
	assert.ErrorIs(t, store.CommitRule(ctx, second, nil, second.Revision), ErrRevisionConflict)

	storedTargets, err := store.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	require.Len(t, storedTargets, 1)
	assert.Equal(t, TargetStateApplied, storedTargets[0].State)
}

// Regression for the previously reproduced 8-15% race: a refreshRule that
// started before DeleteRule used to write a stale DesiredState back afterwards.
func TestConcurrentRefreshAndDeleteNoStaleOverwrite(t *testing.T) {
	ctx := context.Background()
	conflicts := 0
	staleOverwrites := 0
	const rounds = 60

	for i := 0; i < rounds; i++ {
		svc, registry, tm := newTestInstrumentationServiceWithAgentTTL(t, 5*time.Second, time.Second)
		registerTestAgent(t, registry, "agent-v")
		created, err := svc.CreateRule(ctx, &CreateRuleRequest{
			Name: "verify-race", AppID: "app-a", ServiceName: "svc-a",
			ClassName: "demo.OrderService", MethodName: "submit", InstrumentType: InstrumentTypeTrace,
		})
		require.NoError(t, err)

		targets, err := svc.store.ListTargetStatuses(ctx, created.ID)
		require.NoError(t, err)
		require.NotEmpty(t, targets)
		require.NoError(t, tm.ReportTaskResult(ctx, &model.TaskResult{
			TaskID: targets[0].TaskID, AgentID: targets[0].AgentID, Status: model.TaskStatusSuccess,
		}))

		start := make(chan struct{})
		var wg sync.WaitGroup
		var deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = svc.refreshRule(ctx, created)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deleteErr = svc.DeleteRule(ctx, created.ID)
		}()
		close(start)
		wg.Wait()

		if isRevisionConflict(deleteErr) {
			conflicts++
		}
		stored, err := svc.store.GetRule(ctx, created.ID)
		require.NoError(t, err)
		// A successful delete must never end up active: that was the bug.
		if deleteErr == nil && stored.DesiredState != RuleDesiredStateDeleted {
			staleOverwrites++
		}
	}
	t.Logf("VERIFY concurrent refresh+delete: rounds=%d conflicts=%d stale_overwrites=%d", rounds, conflicts, staleOverwrites)
	assert.Zero(t, staleOverwrites, "CAS must prevent a stale refresh from overwriting a committed delete")
}

func isRevisionConflict(err error) bool {
	return errors.Is(err, ErrRevisionConflict)
}

// GC must keep unconfirmed deleted rules so reconcile can still compensate.
func TestGCKeepsRulesWithoutUninstallEvidence(t *testing.T) {
	ctx := context.Background()
	svc, registry, _ := newTestInstrumentationServiceWithAgentTTL(t, 5*time.Second, time.Second)
	svc.config.DeletedRuleRetention = 1
	registerTestAgent(t, registry, "agent-v")

	rule, err := svc.CreateRule(ctx, &CreateRuleRequest{
		Name: "verify-gc", AppID: "app-a", ServiceName: "svc-a",
		ClassName: "demo.OrderService", MethodName: "submit", InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	_, err = svc.DeleteRule(ctx, rule.ID)
	require.NoError(t, err)

	targets, err := svc.store.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	for _, target := range targets {
		target.State = TargetStateFailed
	}
	require.NoError(t, svc.store.SaveTargetStatuses(ctx, rule.ID, targets))

	stored, err := svc.store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	stored.UpdatedAtMillis = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	require.NoError(t, svc.store.SaveRule(ctx, stored, false))

	svc.gcOnce(ctx)
	_, err = svc.store.GetRule(ctx, rule.ID)
	require.NoError(t, err, "failed/unconfirmed rules must be retained")

	// Rule-level evidence makes it purgeable.
	stored, err = svc.store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	stored.UninstallConfirmed = true
	stored.UninstallEvidenceSource = EvidenceSourceRuntimeSnapshot
	require.NoError(t, svc.store.SaveRule(ctx, stored, false))
	svc.gcOnce(ctx)
	_, err = svc.store.GetRule(ctx, rule.ID)
	assert.ErrorIs(t, err, ErrRuleNotFound)
}

// Batch backfill: deleted rules that were never instrumented and have no
// targets get rule-level evidence without a probe query.
func TestVerifyUninstallNeverInstrumentedBackfill(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestInstrumentationServiceWithAgentTTL(t, 5*time.Second, time.Second)
	created, err := svc.CreateRule(ctx, &CreateRuleRequest{
		Name: "verify-backfill", AppID: "app-a", ServiceName: "svc-a",
		ClassName: "demo.OrderService", MethodName: "submit", InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	// No agent registered => no targets, no successful apply.
	_, err = svc.DeleteRule(ctx, created.ID)
	require.NoError(t, err)

	updated, err := svc.VerifyUninstall(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, updated.UninstallConfirmed)
	assert.Equal(t, EvidenceSourceNeverInstrumented, updated.UninstallEvidenceSource)
}

// Keeping the record (i.e. not running GC) lets a returning agent self-heal.
func TestRetainedUnconfirmedRuleSelfHeals(t *testing.T) {
	ctx := context.Background()
	svc, registry, tm := newTestInstrumentationServiceWithAgentTTL(t, 5*time.Second, time.Second)
	registerTestAgent(t, registry, "agent-v")
	created, err := svc.CreateRule(ctx, &CreateRuleRequest{
		Name: "verify-retained", AppID: "app-a", ServiceName: "svc-a",
		ClassName: "demo.OrderService", MethodName: "submit", InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, created.ID, "agent-v")

	targets, err := svc.store.ListTargetStatuses(ctx, created.ID)
	require.NoError(t, err)
	targets[0].State = TargetStateOffline
	targets[0].TaskID = ""
	targets[0].TaskStatus = ""
	targets[0].TaskType = ""
	targets[0].LastDispatchAtMillis = time.Now().Add(-20 * time.Second).UnixMilli()
	require.NoError(t, svc.store.SaveTargetStatuses(ctx, created.ID, targets))

	rule, err := svc.store.GetRule(ctx, created.ID)
	require.NoError(t, err)
	rule.DesiredState = RuleDesiredStateDeleted
	rule.UpdatedAtMillis = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	require.NoError(t, svc.store.SaveRule(ctx, rule, false))

	svc.reconcileOnce(ctx)

	after, err := tm.GetPendingTasks(ctx, "agent-v")
	require.NoError(t, err)
	uninstall := 0
	for _, task := range after {
		if task.TypeName == taskTypeForOperation(OperationTypeRemove) {
			uninstall++
		}
	}
	assert.Greater(t, uninstall, 0, "reconcile must re-dispatch remove while the record is retained")
}

// The batch lease is a globally elected lock: with two replicas sharing the
// same Redis, only one may run an evidence scan cycle.
func TestEvidenceBatchLeaseElectsSingleReplica(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cmd := NewRedisCmd(client)

	storeA := newRedisRuntimeSnapshotStore(zap.NewNop(), cmd, "otel:verify:evidence", "instance-a", time.Second)
	storeB := newRedisRuntimeSnapshotStore(zap.NewNop(), cmd, "otel:verify:evidence", "instance-b", time.Second)

	acquiredA, err := storeA.TryAcquireRefreshLease(ctx, evidenceBatchLeaseKey, "instance-a", 2*time.Minute)
	require.NoError(t, err)
	acquiredB, err := storeB.TryAcquireRefreshLease(ctx, evidenceBatchLeaseKey, "instance-b", 2*time.Minute)
	require.NoError(t, err)
	assert.True(t, acquiredA)
	assert.False(t, acquiredB, "only one replica may own a scan cycle")
}
