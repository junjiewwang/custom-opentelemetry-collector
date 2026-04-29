// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGCPhysicallyDeletesEligibleRule 验证满足三个条件（deleted + 所有 target 终态 + retention 已过期）的规则被物理删除。
func TestGCPhysicallyDeletesEligibleRule(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	svc.config.DeletedRuleRetention = 1 // 1ms retention，极短

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

	// 删除规则
	_, err = svc.DeleteRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// 手动设置所有 target 为终态（removed）
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	for _, tgt := range targets {
		tgt.State = TargetStateRemoved
		tgt.TaskID = ""
		tgt.TaskStatus = ""
	}
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 设置 rule.UpdatedAtMillis 为过去时间，确保 retention 已过期
	rule, err = svc.store.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	rule.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
	require.NoError(t, svc.store.SaveRule(context.Background(), rule, false))

	// 执行一次 GC
	svc.gcOnce(context.Background())

	// 验证规则已被物理删除
	_, err = svc.store.GetRule(context.Background(), rule.ID)
	assert.ErrorIs(t, err, ErrRuleNotFound)
}

// TestGCSkipsRuleWithinRetention 验证 retention 未到期时规则不被删除。
func TestGCSkipsRuleWithinRetention(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	svc.config.DeletedRuleRetention = 999999999 // 很长的 retention

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

	// 删除规则
	_, err = svc.DeleteRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// 手动设置所有 target 为终态
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	for _, tgt := range targets {
		tgt.State = TargetStateRemoved
		tgt.TaskID = ""
		tgt.TaskStatus = ""
	}
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 执行 GC
	svc.gcOnce(context.Background())

	// 验证规则仍存在（retention 未到期）
	_, err = svc.store.GetRule(context.Background(), rule.ID)
	assert.NoError(t, err)
}

// TestGCSkipsRuleWithNonTerminalTarget 验证有非终态 target 的规则不被删除。
func TestGCSkipsRuleWithNonTerminalTarget(t *testing.T) {
	svc, registry, tm := newTestInstrumentationService(t)
	svc.config.DeletedRuleRetention = 1 // 1ms retention

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

	// 删除规则
	_, err = svc.DeleteRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// 关键：保留一个 target 为 TargetStateRunning（非终态）
	targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
	require.NoError(t, err)
	require.NotEmpty(t, targets)
	targets[0].State = TargetStateRunning
	require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

	// 设置过期时间
	rule, err = svc.store.GetRule(context.Background(), rule.ID)
	require.NoError(t, err)
	rule.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
	require.NoError(t, svc.store.SaveRule(context.Background(), rule, false))

	// 执行 GC
	svc.gcOnce(context.Background())

	// 验证规则仍存在（因为有非终态 target）
	_, err = svc.store.GetRule(context.Background(), rule.ID)
	assert.NoError(t, err)
}

// TestMemoryRuleStorePhysicalDelete 验证 MemoryRuleStore 的 PhysicalDeleteRule 方法。
func TestMemoryRuleStorePhysicalDelete(t *testing.T) {
	store := NewMemoryRuleStore()
	ctx := context.Background()

	rule := newTestRule("rule-pd")
	require.NoError(t, store.SaveRule(ctx, rule, true))
	require.NoError(t, store.SaveTargetStatuses(ctx, rule.ID, newTestTargets(rule.ID)))

	// 验证数据存在
	loadedRule, err := store.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "rule-pd", loadedRule.ID)

	loadedTargets, err := store.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	assert.Len(t, loadedTargets, 1)

	// 物理删除
	require.NoError(t, store.PhysicalDeleteRule(ctx, rule.ID))

	// 验证规则已被删除
	_, err = store.GetRule(ctx, rule.ID)
	assert.ErrorIs(t, err, ErrRuleNotFound)

	// 验证 target 也被清理（GetRule 返回 ErrRuleNotFound，ListTargetStatuses 也应返回 ErrRuleNotFound）
	_, err = store.ListTargetStatuses(ctx, rule.ID)
	assert.ErrorIs(t, err, ErrRuleNotFound)

	// 重复删除应返回 ErrRuleNotFound
	err = store.PhysicalDeleteRule(ctx, rule.ID)
	assert.ErrorIs(t, err, ErrRuleNotFound)
}

// TestAllTargetsTerminal 验证 allTargetsTerminal 辅助函数的各种边界情况。
func TestAllTargetsTerminal(t *testing.T) {
	tests := []struct {
		name     string
		targets  []*RuleTargetStatus
		expected bool
	}{
		{
			name:     "空列表视为终态",
			targets:  nil,
			expected: true,
		},
		{
			name: "所有 target 为 removed",
			targets: []*RuleTargetStatus{
				{State: TargetStateRemoved},
				{State: TargetStateRemoved},
			},
			expected: true,
		},
		{
			name: "混合终态（removed + failed + expired + offline）",
			targets: []*RuleTargetStatus{
				{State: TargetStateRemoved},
				{State: TargetStateFailed},
				{State: TargetStateExpired},
				{State: TargetStateOffline},
			},
			expected: true,
		},
		{
			name: "包含 running 非终态",
			targets: []*RuleTargetStatus{
				{State: TargetStateRemoved},
				{State: TargetStateRunning},
			},
			expected: false,
		},
		{
			name: "包含 dispatched 非终态",
			targets: []*RuleTargetStatus{
				{State: TargetStateDispatched},
			},
			expected: false,
		},
		{
			name: "包含 applied 非终态",
			targets: []*RuleTargetStatus{
				{State: TargetStateApplied},
			},
			expected: false,
		},
		{
			name: "包含 pending 非终态",
			targets: []*RuleTargetStatus{
				{State: TargetStatePending},
			},
			expected: false,
		},
		{
			name: "nil target 被跳过",
			targets: []*RuleTargetStatus{
				nil,
				{State: TargetStateRemoved},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, allTargetsTerminal(tt.targets))
		})
	}
}
