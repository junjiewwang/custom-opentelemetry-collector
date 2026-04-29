// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

func newTestInstrumentationService(t *testing.T) (*InstrumentationService, *agentregistry.MemoryAgentRegistry, taskmanager.TaskManager) {
	t.Helper()

	logger := zap.NewNop()
	agentCfg := agentregistry.DefaultConfig()
	agentCfg.HeartbeatTTL = 2 * time.Millisecond
	agentCfg.OfflineCheckInterval = 1 * time.Millisecond
	agentReg := agentregistry.NewMemoryAgentRegistry(logger, agentCfg)
	require.NoError(t, agentReg.Start(context.Background()))

	taskMgr, err := taskmanager.NewTaskManager(logger, taskmanager.DefaultConfig(), nil)
	require.NoError(t, err)
	require.NoError(t, taskMgr.Start(context.Background()))

	cfg := DefaultConfig()
	cfg.ReconcileRetryInterval = 1
	service := NewInstrumentationService(logger, cfg, NewMemoryRuleStore(), agentReg, taskMgr)

	t.Cleanup(func() {
		require.NoError(t, taskMgr.Close())
		require.NoError(t, agentReg.Close())
	})

	return service, agentReg, taskMgr
}

func registerTestAgent(t *testing.T, registry *agentregistry.MemoryAgentRegistry, agentID string) {
	t.Helper()
	require.NoError(t, registry.Register(context.Background(), &agentregistry.AgentInfo{
		AgentID:     agentID,
		Token:       "token-a",
		AppID:       "app-a",
		ServiceName: "svc-a",
		Hostname:    agentID + ".host",
		IP:          "127.0.0.1",
	}))
}

func markTargetTaskSuccess(t *testing.T, svc *InstrumentationService, tm taskmanager.TaskManager, ruleID, agentID string) {
	t.Helper()
	targets, err := svc.ListTargetStatuses(context.Background(), ruleID)
	require.NoError(t, err)
	for _, target := range targets {
		if target != nil && target.AgentID == agentID && target.TaskID != "" {
			require.NoError(t, tm.ReportTaskResult(context.Background(), &model.TaskResult{
				TaskID:  target.TaskID,
				AgentID: target.AgentID,
				Status:  model.TaskStatusSuccess,
			}))
			_, err = svc.GetRule(context.Background(), ruleID)
			require.NoError(t, err)
			return
		}
	}
	t.Fatalf("target %s not found for rule %s", agentID, ruleID)
}

func findTargetByAgent(targets []*RuleTargetStatus, agentID string) *RuleTargetStatus {
	for _, target := range targets {
		if target != nil && target.AgentID == agentID {
			return target
		}
	}
	return nil
}

func TestReconcileRuleAddsNewServiceTargetAndDispatchesApply(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	registerTestAgent(t, registry, "agent-1")

	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	registerTestAgent(t, registry, "agent-2")
	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err := svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	newTarget := findTargetByAgent(targets, "agent-2")
	require.NotNil(t, newTarget)
	assert.Equal(t, TargetStateDispatched, newTarget.State)
	assert.Equal(t, "dynamic_instrument", newTarget.TaskType)
	assert.NotZero(t, newTarget.LastDispatchAtMillis)

	updatedRule, err := svc.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	require.NotEmpty(t, updatedRule.RecentAudits)
	assert.Equal(t, AuditSourceReconcile, updatedRule.RecentAudits[0].Source)
	assert.Equal(t, AuditActionTargetDiscover, updatedRule.RecentAudits[0].Action)
	assert.Equal(t, "agent-2", updatedRule.RecentAudits[0].AgentID)
}

func TestReconcileRuleReappliesRecoveredOnlineTarget(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	registerTestAgent(t, registry, "agent-1")

	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	recoveredTarget := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, recoveredTarget)
	recoveredTarget.State = TargetStateOffline
	recoveredTarget.TaskID = ""
	recoveredTarget.TaskStatus = ""
	recoveredTarget.UpdatedAtMillis = time.Now().UnixMilli()
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	time.Sleep(2 * time.Millisecond)
	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	recoveredTarget = findTargetByAgent(targets, "agent-1")
	require.NotNil(t, recoveredTarget)
	assert.Equal(t, TargetStateDispatched, recoveredTarget.State)
	assert.Equal(t, "dynamic_instrument", recoveredTarget.TaskType)
	assert.NotZero(t, recoveredTarget.LastDispatchAtMillis)

	updatedRule, err := svc.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	require.NotEmpty(t, updatedRule.RecentAudits)
	assert.Equal(t, AuditSourceReconcile, updatedRule.RecentAudits[0].Source)
	assert.Equal(t, AuditActionApply, updatedRule.RecentAudits[0].Action)
	assert.Equal(t, "agent-1", updatedRule.RecentAudits[0].AgentID)
}

func TestReconcileRulePrunesTargetsOutsideServiceScope(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	registerTestAgent(t, registry, "agent-1")

	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	require.NoError(t, registry.Unregister(context.Background(), "agent-1"))
	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err := svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	assert.Len(t, targets, 0)

	updatedRule, err := svc.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	require.NotEmpty(t, updatedRule.RecentAudits)
	assert.Equal(t, AuditActionTargetPrune, updatedRule.RecentAudits[0].Action)
	assert.Equal(t, "agent-1", updatedRule.RecentAudits[0].AgentID)
}

// TestReconcileOfflineTargetExpiresToExpired 验证 offline target 超时后自动变为 expired。
func TestReconcileOfflineTargetExpiresToExpired(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	svc.config.ReconcileTargetExpireTimeout = 1 // 1ms，极短超时

	registerTestAgent(t, registry, "agent-1")
	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	// 手动设置 target 为 offline + 过去的时间戳
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	target.State = TargetStateOffline
	target.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
	target.TaskID = ""
	target.TaskStatus = ""
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 让 agent 心跳过期变为 offline（HeartbeatTTL=2ms）
	time.Sleep(3 * time.Millisecond)

	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target = findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	assert.Equal(t, TargetStateExpired, target.State)
}

// TestReconcileExpiredTargetRecoversWhenOnline 验证 expired target 在 agent 恢复在线后被重新 dispatch。
func TestReconcileExpiredTargetRecoversWhenOnline(t *testing.T) {
	// 使用较长的 HeartbeatTTL 避免 offline detection 竞态
	logger := zap.NewNop()
	agentCfg := agentregistry.DefaultConfig()
	agentCfg.HeartbeatTTL = 5 * time.Second
	agentCfg.OfflineCheckInterval = 1 * time.Second
	registry := agentregistry.NewMemoryAgentRegistry(logger, agentCfg)
	require.NoError(t, registry.Start(context.Background()))

	tm, err := taskmanager.NewTaskManager(logger, taskmanager.DefaultConfig(), nil)
	require.NoError(t, err)
	require.NoError(t, tm.Start(context.Background()))

	cfg := DefaultConfig()
	cfg.ReconcileRetryInterval = 1
	svc := NewInstrumentationService(logger, cfg, NewMemoryRuleStore(), registry, tm)

	t.Cleanup(func() {
		require.NoError(t, tm.Close())
		require.NoError(t, registry.Close())
	})

	registerTestAgent(t, registry, "agent-1")
	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	// 手动设置 target 为 expired
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	target.State = TargetStateExpired
	target.TaskID = ""
	target.TaskStatus = ""
	target.UpdatedAtMillis = time.Now().UnixMilli()
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 重新注册 agent（模拟恢复在线）
	registerTestAgent(t, registry, "agent-1")

	// 确保 agent 在 reconcile 时是 online 的
	require.Eventually(t, func() bool {
		online, err := registry.IsOnline(context.Background(), "agent-1")
		if err != nil || !online {
			// 如果 offline detection 抢先标记了 offline，再次 heartbeat 恢复
			_ = registry.Heartbeat(context.Background(), "agent-1", nil)
			online, _ = registry.IsOnline(context.Background(), "agent-1")
		}
		return online
	}, time.Second, time.Millisecond)

	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target = findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	assert.Equal(t, TargetStateDispatched, target.State) // 被重新 dispatch
}

// TestEverApplySucceededSetOnFirstApply 验证 target 成功 apply 后 EverApplySucceeded 被设为 true。
func TestEverApplySucceededSetOnFirstApply(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	registerTestAgent(t, registry, "agent-1")

	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	assert.False(t, rule.EverApplySucceeded) // 初始为 false

	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	rule, err = svc.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	assert.True(t, rule.EverApplySucceeded) // 成功 apply 后为 true
}
