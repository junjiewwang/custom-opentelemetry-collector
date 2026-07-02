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
	"go.opentelemetry.io/collector/custom/taskengine"
)

func newTestInstrumentationService(t *testing.T) (*InstrumentationService, *agentregistry.MemoryAgentRegistry, taskmanager.TaskManager) {
	t.Helper()

	logger := zap.NewNop()
	agentCfg := agentregistry.DefaultConfig()
	agentCfg.HeartbeatTTL = 2 * time.Millisecond
	agentCfg.OfflineCheckInterval = 1 * time.Millisecond
	agentReg := agentregistry.NewMemoryAgentRegistry(logger, agentCfg)
	require.NoError(t, agentReg.Start(context.Background()))

	engine := taskengine.NewEngine(taskengine.NewMemoryStore(), nil, logger, taskengine.DefaultEngineConfig())
	require.NoError(t, engine.Start(context.Background()))
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	taskMgr, err := taskmanager.NewTaskManager(logger, taskmanager.DefaultConfig(), engine)
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

	// 等待 agent 心跳过期变为 offline（HeartbeatTTL=2ms, OfflineCheckInterval=1ms）
	require.Eventually(t, func() bool {
		online, _ := registry.IsOnline(context.Background(), "agent-1")
		return !online
	}, time.Second, time.Millisecond, "agent-1 should be offline")

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

	engine := taskengine.NewEngine(taskengine.NewMemoryStore(), nil, logger, taskengine.DefaultEngineConfig())
	require.NoError(t, engine.Start(context.Background()))
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	tm, err := taskmanager.NewTaskManager(logger, taskmanager.DefaultConfig(), engine)
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
	target.LastDispatchAtMillis = 0
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

// TestReconcileSkipsOfflineNewInstance 验证 reconcile 发现新实例时，如果 agent 离线则跳过（不创建 target 记录）。
func TestReconcileSkipsOfflineNewInstance(t *testing.T) {
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

	// 注册 agent-2，然后让它心跳超时变为 offline
	registerTestAgent(t, registry, "agent-2")

	// 等待 agent-2 心跳过期变为 offline（HeartbeatTTL=2ms, OfflineCheckInterval=1ms）
	require.Eventually(t, func() bool {
		online, _ := registry.IsOnline(context.Background(), "agent-2")
		return !online
	}, time.Second, time.Millisecond, "agent-2 should be offline")

	// 执行 reconcile
	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	// 验证：只有 agent-1 的 target，agent-2 没有被创建 target
	targets, err := svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	assert.Len(t, targets, 1)
	assert.Nil(t, findTargetByAgent(targets, "agent-2"), "offline agent-2 should NOT have a target record")

	// 验证：当 agent-2 恢复在线后，下一轮 reconcile 会正常发现并 dispatch
	registerTestAgent(t, registry, "agent-2")
	require.Eventually(t, func() bool {
		online, _ := registry.IsOnline(context.Background(), "agent-2")
		return online
	}, time.Second, time.Millisecond)

	require.NoError(t, svc.reconcileRule(context.Background(), rule))

	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	assert.Len(t, targets, 2)
	newTarget := findTargetByAgent(targets, "agent-2")
	require.NotNil(t, newTarget, "agent-2 should be discovered after coming back online")
	assert.Equal(t, TargetStateDispatched, newTarget.State)
}

// TestRefreshTargetStatusSkipsOfflineTarget 验证 refreshTargetStatus 不会用旧 task 结果覆盖 offline target 的状态。
// 这是 BUG FIX 回归测试：之前离线 target 的旧 TaskID 指向一个 SUCCESS 结果，
// refreshTargetStatus 会错误地将 offline target 的 state 映射为 applied。
func TestRefreshTargetStatusSkipsOfflineTarget(t *testing.T) {
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

	// 获取当前 target（state=applied，有 TaskID 指向 SUCCESS result）
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	assert.Equal(t, TargetStateApplied, target.State)
	assert.NotEmpty(t, target.TaskID)
	oldTaskID := target.TaskID

	// 手动将 target 标记为 offline（模拟 reconcileExistingTarget 的行为）
	target.State = TargetStateOffline
	target.UpdatedAtMillis = time.Now().UnixMilli()
	// 注意：保留 TaskID 不清空，这是修复前的情况
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 调用 refreshRule（通过 GetRule 触发）
	refreshedRule, err := svc.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	_ = refreshedRule

	// 验证：target 状态应该仍然是 offline，而非被旧 task SUCCESS 覆盖为 applied
	targets, err = svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	target = findTargetByAgent(targets, "agent-1")
	require.NotNil(t, target)
	assert.Equal(t, TargetStateOffline, target.State, "offline target state should NOT be overridden by stale task result")
	assert.Equal(t, oldTaskID, target.TaskID, "TaskID should be preserved for audit trail")
}

// TestResumeRuleDoesNotShowOfflineTargetsAsApplied 验证完整场景：
// 规则 Active→Pause→Resume 后，离线 agent 的 target 不会错误显示为 applied。
func TestResumeRuleDoesNotShowOfflineTargetsAsApplied(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	registerTestAgent(t, registry, "agent-1")
	registerTestAgent(t, registry, "agent-2")

	// 1. 创建规则，两个 agent 都成功 apply
	rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "demo.OrderService",
		MethodName:     "submit",
		InstrumentType: InstrumentTypeTrace,
	})
	require.NoError(t, err)
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-2")

	// 2. Pause 规则
	rule, err = svc.PauseRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// 3. 两个 agent 的 Remove 任务都成功
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-2")

	// 验证：两个 target 都是 removed 状态
	targets, err := svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	for _, target := range targets {
		assert.Equal(t, TargetStateRemoved, target.State)
	}

	// 4. agent-2 离线
	require.Eventually(t, func() bool {
		online, _ := registry.IsOnline(context.Background(), "agent-2")
		return !online
	}, time.Second, time.Millisecond, "agent-2 should be offline")

	// 保持 agent-1 在线
	require.NoError(t, registry.Heartbeat(context.Background(), "agent-1", nil))

	// 5. Resume 规则
	rule, err = svc.ResumeRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// 6. 验证结果：agent-1 应该是 dispatched，agent-2 应该是 offline
	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	t1 := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, t1)
	assert.Equal(t, TargetStateDispatched, t1.State, "online agent-1 should be dispatched")

	t2 := findTargetByAgent(targets, "agent-2")
	require.NotNil(t, t2)
	assert.Equal(t, TargetStateOffline, t2.State, "offline agent-2 should be offline, NOT applied")
	assert.Empty(t, t2.TaskID, "offline target should have cleared TaskID")

	// 7. agent-1 任务成功后，再次查询 — agent-2 仍然不应该变成 applied
	markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

	targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)

	t1 = findTargetByAgent(targets, "agent-1")
	require.NotNil(t, t1)
	assert.Equal(t, TargetStateApplied, t1.State, "online agent-1 task success → applied")

	t2 = findTargetByAgent(targets, "agent-2")
	require.NotNil(t, t2)
	assert.Equal(t, TargetStateOffline, t2.State, "offline agent-2 should remain offline")
}

// TestDispatchOperationToAgentClearsTaskIDWhenOffline 验证 dispatchOperationToAgent
// 在 agent 离线时清空 TaskID 和 TaskStatus，防止旧 task 残留。
func TestDispatchOperationToAgentClearsTaskIDWhenOffline(t *testing.T) {
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

	// 获取当前 target（有 TaskID）
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	current := findTargetByAgent(targets, "agent-1")
	require.NotNil(t, current)
	assert.NotEmpty(t, current.TaskID)

	// 构造一个 offline agent
	offlineAgent := &agentregistry.AgentInfo{
		AgentID:     "agent-1",
		Hostname:    "agent-1.host",
		IP:          "127.0.0.1",
		AppID:       "app-a",
		ServiceName: "svc-a",
		Status:      &agentregistry.AgentStatus{State: agentregistry.AgentStateOffline},
	}

	// 直接调用 dispatchOperationToAgent
	next, _, _ := svc.dispatchOperationToAgent(
		context.Background(), rule, OperationTypeApply,
		current, offlineAgent, time.Now().UnixMilli(),
		AuditSourceManual, "test offline dispatch",
	)

	require.NotNil(t, next)
	assert.Equal(t, TargetStateOffline, next.State)
	assert.Empty(t, next.TaskID, "TaskID should be cleared for offline agent")
	assert.Empty(t, next.TaskStatus, "TaskStatus should be cleared for offline agent")
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
