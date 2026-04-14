// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

func newTestRedisRuntimeSnapshotStore(t *testing.T, client RedisClient, prefix, instanceID string, sharedSyncInterval time.Duration) *redisRuntimeSnapshotStore {
	t.Helper()
	store := newRedisRuntimeSnapshotStore(zap.NewNop(), client, prefix, instanceID, sharedSyncInterval)
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func newTestRuntimeSnapshotEntry(agentID, ruleID string) *agentRuntimeSnapshotCacheEntry {
	now := time.Now().UnixMilli()
	return &agentRuntimeSnapshotCacheEntry{
		AgentID:           agentID,
		Summary:           dynamicInstrumentListSummary{InstrumentationAvailable: true, EnhancementCapability: true, ActiveTransformerCount: 1},
		Items:             []dynamicInstrumentListItem{{RuleID: ruleID, RuntimeStatus: "active", IsApplied: true, IsEffective: true}},
		HasPayload:        true,
		SourceTaskID:      "task-runtime-snapshot",
		RefreshedAtMillis: now,
		ExpiresAtMillis:   now + 10_000,
		Dirty:             false,
		LastRefreshStatus: RuntimeRefreshStatusSuccess,
		UpdatedAtMillis:   now,
	}
}

func TestRedisRuntimeSnapshotStore_SharesAcrossInstancesAndRestart(t *testing.T) {
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:runtime-snapshot:%s", sanitizeRuleStoreKeyPart(t.Name()))
	ctx := context.Background()

	storeA := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-a", 50*time.Millisecond)
	entry := newTestRuntimeSnapshotEntry("agent-1", "rule-1")
	_, err := storeA.Upsert(ctx, entry.AgentID, func(*agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		return cloneAgentRuntimeSnapshotCacheEntry(entry)
	})
	require.NoError(t, err)

	storeB := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-b", 50*time.Millisecond)
	loaded, err := storeB.Get(ctx, entry.AgentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, entry.AgentID, loaded.AgentID)
	assert.Equal(t, entry.SourceTaskID, loaded.SourceTaskID)
	assert.Equal(t, entry.Items[0].RuleID, loaded.Items[0].RuleID)
	assert.False(t, loaded.Dirty)

	require.NoError(t, storeA.Close())
	storeRestarted := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-restarted", 50*time.Millisecond)
	reloaded, err := storeRestarted.Get(ctx, entry.AgentID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, entry.RefreshedAtMillis, reloaded.RefreshedAtMillis)
	assert.Equal(t, entry.Items[0].RuleID, reloaded.Items[0].RuleID)
}

func TestRedisRuntimeSnapshotStore_DirtyBroadcastsToOtherInstances(t *testing.T) {
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:runtime-snapshot:%s", sanitizeRuleStoreKeyPart(t.Name()))
	ctx := context.Background()

	storeA := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-a", 2*time.Second)
	storeB := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-b", 2*time.Second)
	entry := newTestRuntimeSnapshotEntry("agent-1", "rule-1")
	_, err := storeA.Upsert(ctx, entry.AgentID, func(*agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		return cloneAgentRuntimeSnapshotCacheEntry(entry)
	})
	require.NoError(t, err)

	_, err = storeB.Get(ctx, entry.AgentID)
	require.NoError(t, err)
	require.NoError(t, storeA.MarkDirty(ctx, []string{entry.AgentID}))

	require.Eventually(t, func() bool {
		loaded, loadErr := storeB.Get(context.Background(), entry.AgentID)
		return loadErr == nil && loaded != nil && loaded.Dirty
	}, 2*time.Second, 50*time.Millisecond)
}

func TestRedisRuntimeSnapshotStore_RefreshLeaseSingleOwner(t *testing.T) {
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:runtime-snapshot:%s", sanitizeRuleStoreKeyPart(t.Name()))
	ctx := context.Background()

	storeA := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-a", 50*time.Millisecond)
	storeB := newTestRedisRuntimeSnapshotStore(t, client, prefix, "instance-b", 50*time.Millisecond)

	acquiredA, err := storeA.TryAcquireRefreshLease(ctx, "agent-1", "instance-a", 150*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, acquiredA)

	acquiredB, err := storeB.TryAcquireRefreshLease(ctx, "agent-1", "instance-b", 150*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, acquiredB)

	require.Eventually(t, func() bool {
		ok, acquireErr := storeB.TryAcquireRefreshLease(context.Background(), "agent-1", "instance-b", 150*time.Millisecond)
		return acquireErr == nil && ok
	}, time.Second, 50*time.Millisecond)
}

type runtimeSnapshotTaskRecord struct {
	task        *model.Task
	result      *model.TaskResult
	availableAt time.Time
}

type runtimeSnapshotTaskManagerStub struct {
	mu          sync.Mutex
	payload     json.RawMessage
	resultDelay time.Duration
	tasks       map[string]*runtimeSnapshotTaskRecord
	submitCount int
}

func newRuntimeSnapshotTaskManagerStub(payload json.RawMessage, resultDelay time.Duration) *runtimeSnapshotTaskManagerStub {
	return &runtimeSnapshotTaskManagerStub{
		payload:     payload,
		resultDelay: resultDelay,
		tasks:       make(map[string]*runtimeSnapshotTaskRecord),
	}
}

func (m *runtimeSnapshotTaskManagerStub) SubmitTask(context.Context, *model.Task) error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) SubmitTaskForAgent(_ context.Context, _ *taskmanager.AgentMeta, task *model.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submitCount++
	completedAt := time.Now().Add(m.resultDelay)
	m.tasks[task.ID] = &runtimeSnapshotTaskRecord{
		task: task,
		result: &model.TaskResult{
			TaskID:            task.ID,
			AgentID:           task.TargetAgentID,
			Status:            model.TaskStatusSuccess,
			ResultJSON:        append(json.RawMessage(nil), m.payload...),
			StartedAtMillis:   task.CreatedAtMillis,
			CompletedAtMillis: completedAt.UnixMilli(),
		},
		availableAt: completedAt,
	}
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) FetchTask(context.Context, string, time.Duration) (*model.Task, error) {
	return nil, nil
}

func (m *runtimeSnapshotTaskManagerStub) GetPendingTasks(context.Context, string) ([]*model.Task, error) {
	return nil, nil
}

func (m *runtimeSnapshotTaskManagerStub) GetGlobalPendingTasks(context.Context) ([]*model.Task, error) {
	return nil, nil
}

func (m *runtimeSnapshotTaskManagerStub) GetAllTasks(context.Context) ([]*taskmanager.TaskInfo, error) {
	return nil, nil
}

func (m *runtimeSnapshotTaskManagerStub) ListTasks(context.Context, taskmanager.ListTasksQuery) (taskmanager.ListTasksPage, error) {
	return taskmanager.ListTasksPage{}, nil
}

func (m *runtimeSnapshotTaskManagerStub) CancelTask(context.Context, string) error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) IsTaskCancelled(context.Context, string) (bool, error) {
	return false, nil
}

func (m *runtimeSnapshotTaskManagerStub) ReportTaskResult(context.Context, *model.TaskResult) error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) GetTaskResult(_ context.Context, taskID string) (*model.TaskResult, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.tasks[taskID]
	if !ok {
		return nil, false, nil
	}
	if time.Now().Before(record.availableAt) {
		return nil, false, nil
	}
	return cloneTaskResult(record.result), true, nil
}

func (m *runtimeSnapshotTaskManagerStub) GetTaskStatus(_ context.Context, taskID string) (*taskmanager.TaskInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.tasks[taskID]
	if !ok {
		return nil, nil
	}
	status := model.TaskStatusRunning
	var result *model.TaskResult
	if !time.Now().Before(record.availableAt) {
		status = model.TaskStatusSuccess
		result = cloneTaskResult(record.result)
	}
	return &taskmanager.TaskInfo{
		Task:            record.task,
		Status:          status,
		AgentID:         record.task.TargetAgentID,
		CreatedAtMillis: record.task.CreatedAtMillis,
		Result:          result,
	}, nil
}

func (m *runtimeSnapshotTaskManagerStub) SetTaskRunning(context.Context, string, string) error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) Start(context.Context) error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) Close() error {
	return nil
}

func (m *runtimeSnapshotTaskManagerStub) SubmitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.submitCount
}

func cloneTaskResult(result *model.TaskResult) *model.TaskResult {
	if result == nil {
		return nil
	}
	copied := *result
	copied.ResultJSON = append(json.RawMessage(nil), result.ResultJSON...)
	copied.ResultData = append([]byte(nil), result.ResultData...)
	return &copied
}

func TestInstrumentationService_GetRuleRuntimeSnapshot_UsesSingleDistributedRefresh(t *testing.T) {
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:runtime-snapshot:%s", sanitizeRuleStoreKeyPart(t.Name()))
	ctx := context.Background()

	payload, err := json.Marshal(dynamicInstrumentListResponse{
		Summary: dynamicInstrumentListSummary{InstrumentationAvailable: true, EnhancementCapability: true, ActiveTransformerCount: 1},
		Items:   []dynamicInstrumentListItem{{RuleID: "rule-distributed", RuntimeStatus: "active", IsApplied: true, IsEffective: true}},
	})
	require.NoError(t, err)
	taskMgr := newRuntimeSnapshotTaskManagerStub(payload, 120*time.Millisecond)

	agentCfg := agentregistry.DefaultConfig()
	agentReg := agentregistry.NewMemoryAgentRegistry(zap.NewNop(), agentCfg)
	require.NoError(t, agentReg.Start(ctx))
	defer func() { _ = agentReg.Close() }()
	require.NoError(t, agentReg.Register(ctx, &agentregistry.AgentInfo{
		AgentID:     "agent-1",
		Token:       "token-a",
		AppID:       "app-a",
		ServiceName: "svc-a",
		Hostname:    "host-1",
		IP:          "127.0.0.1",
		Status:      &agentregistry.AgentStatus{State: agentregistry.AgentStateOnline},
	}))

	sharedRuleStore := NewMemoryRuleStore()
	require.NoError(t, sharedRuleStore.Start(ctx))
	rule := newTestRule("rule-distributed")
	require.NoError(t, sharedRuleStore.SaveRule(ctx, rule, true))
	targets := newTestTargets(rule.ID)
	targets[0].TaskID = ""
	targets[0].TaskStatus = ""
	targets[0].State = TargetStateApplied
	require.NoError(t, sharedRuleStore.SaveTargetStatuses(ctx, rule.ID, targets))

	cfg := DefaultConfig()
	cfg.Type = "redis"
	cfg.RuntimeSnapshotSharedSyncInterval = 50
	cfg.RuntimeSnapshotLeaseTTL = 400
	cfg.RuntimeSnapshotPollInterval = 20

	svcA := newInstrumentationServiceWithRuntimeSnapshotStore(
		zap.NewNop(),
		cfg,
		sharedRuleStore,
		agentReg,
		taskMgr,
		newRedisRuntimeSnapshotStore(zap.NewNop(), client, prefix, "instance-a", 50*time.Millisecond),
		"instance-a",
	)
	svcB := newInstrumentationServiceWithRuntimeSnapshotStore(
		zap.NewNop(),
		cfg,
		sharedRuleStore,
		agentReg,
		taskMgr,
		newRedisRuntimeSnapshotStore(zap.NewNop(), client, prefix, "instance-b", 50*time.Millisecond),
		"instance-b",
	)
	require.NoError(t, svcA.Start(ctx))
	defer func() { _ = svcA.Close() }()
	require.NoError(t, svcB.Start(ctx))
	defer func() { _ = svcB.Close() }()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		errs     []error
		snapshots = make([]*RuleRuntimeSnapshot, 0, 2)
		snapMu   sync.Mutex
	)
	call := func(svc *InstrumentationService) {
		defer wg.Done()
		snapshot, snapshotErr := svc.GetRuleRuntimeSnapshot(ctx, rule.ID)
		errMu.Lock()
		if snapshotErr != nil {
			errs = append(errs, snapshotErr)
		}
		errMu.Unlock()
		if snapshot != nil {
			snapMu.Lock()
			snapshots = append(snapshots, snapshot)
			snapMu.Unlock()
		}
	}

	wg.Add(2)
	go call(svcA)
	go call(svcB)
	wg.Wait()

	require.Empty(t, errs)
	require.Len(t, snapshots, 2)
	for _, snapshot := range snapshots {
		require.NotNil(t, snapshot)
		require.Len(t, snapshot.Targets, 1)
		assert.True(t, snapshot.Targets[0].RuntimeFound)
		assert.True(t, snapshot.Targets[0].IsEffective)
		assert.Equal(t, RuntimeRefreshStatusSuccess, snapshot.Targets[0].LastRefreshStatus)
	}
	assert.Equal(t, 1, taskMgr.SubmitCount())
}
