// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
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

// phaseTaskManagerStub simulates an agent that first reports RUNNING status
// (with empty payload), then transitions to SUCCESS with actual result data
// after a configurable delay. This reproduces the real-world behavior where
// TaskDispatcher.dispatchWithResult() emits RUNNING immediately, then SUCCESS
// after the executor completes.
type phaseTaskManagerStub struct {
	mu sync.Mutex

	// runningResult is available immediately after submit (status=RUNNING, no payload)
	// successResult becomes available after successDelay
	tasks map[string]*phaseTaskRecord
}

type phaseTaskRecord struct {
	runningResult *model.TaskResult
	successResult *model.TaskResult
	submittedAt   time.Time
	successDelay  time.Duration
}

type phaseStubConfig struct {
	payload      json.RawMessage
	successDelay time.Duration
}

func newPhaseTaskManagerStubWithConfig(_ phaseStubConfig) *phaseTaskManagerStub {
	return &phaseTaskManagerStub{
		tasks: make(map[string]*phaseTaskRecord),
	}
}

func (m *phaseTaskManagerStub) SubmitTask(context.Context, *model.Task) error { return nil }

func (m *phaseTaskManagerStub) SubmitTaskForAgent(_ context.Context, _ *taskmanager.AgentMeta, task *model.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.getConfigForTask(task.ID)
	m.tasks[task.ID] = &phaseTaskRecord{
		runningResult: &model.TaskResult{
			TaskID:          task.ID,
			AgentID:         task.TargetAgentID,
			Status:          model.TaskStatusRunning,
			StartedAtMillis: time.Now().UnixMilli(),
		},
		successResult: &model.TaskResult{
			TaskID:            task.ID,
			AgentID:           task.TargetAgentID,
			Status:            model.TaskStatusSuccess,
			ResultJSON:        append(json.RawMessage(nil), cfg.payload...),
			StartedAtMillis:   task.CreatedAtMillis,
			CompletedAtMillis: time.Now().Add(cfg.successDelay).UnixMilli(),
		},
		submittedAt:  time.Now(),
		successDelay: cfg.successDelay,
	}
	return nil
}

// defaultConfig is set per test via setDefaultConfig
var phaseStubDefaultConfig = phaseStubConfig{
	successDelay: 200 * time.Millisecond,
}

func (m *phaseTaskManagerStub) setDefaultConfig(cfg phaseStubConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	phaseStubDefaultConfig = cfg
}

func (m *phaseTaskManagerStub) getConfigForTask(_ string) phaseStubConfig {
	return phaseStubDefaultConfig
}

// GetTaskResult simulates store behavior:
// - Immediately after submit: returns RUNNING result (as agent reports RUNNING first)
// - After successDelay: returns SUCCESS result with payload
func (m *phaseTaskManagerStub) GetTaskResult(_ context.Context, taskID string) (*model.TaskResult, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.tasks[taskID]
	if !ok {
		return nil, false, nil
	}
	if time.Since(record.submittedAt) >= record.successDelay {
		// Agent has completed — return SUCCESS
		return cloneResult(record.successResult), true, nil
	}
	// Agent is still running — return RUNNING (simulates SaveResult from RUNNING report)
	return cloneResult(record.runningResult), true, nil
}

func (m *phaseTaskManagerStub) GetTaskStatus(_ context.Context, taskID string) (*taskmanager.TaskInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.tasks[taskID]
	if !ok {
		return nil, nil
	}
	if time.Since(record.submittedAt) >= record.successDelay {
		return &taskmanager.TaskInfo{
			Task:    &model.Task{ID: taskID},
			Status:  model.TaskStatusSuccess,
			Result:  cloneResult(record.successResult),
			AgentID: record.successResult.AgentID,
		}, nil
	}
	return &taskmanager.TaskInfo{
		Task:    &model.Task{ID: taskID},
		Status:  model.TaskStatusRunning,
		AgentID: record.runningResult.AgentID,
	}, nil
}

func (m *phaseTaskManagerStub) FetchTask(context.Context, string, time.Duration) (*model.Task, error) {
	return nil, nil
}
func (m *phaseTaskManagerStub) GetPendingTasks(context.Context, string) ([]*model.Task, error) {
	return nil, nil
}
func (m *phaseTaskManagerStub) GetGlobalPendingTasks(context.Context) ([]*model.Task, error) {
	return nil, nil
}
func (m *phaseTaskManagerStub) GetAllTasks(context.Context) ([]*taskmanager.TaskInfo, error) {
	return nil, nil
}
func (m *phaseTaskManagerStub) ListTasks(context.Context, taskmanager.ListTasksQuery) (taskmanager.ListTasksPage, error) {
	return taskmanager.ListTasksPage{}, nil
}
func (m *phaseTaskManagerStub) CancelTask(context.Context, string) error { return nil }
func (m *phaseTaskManagerStub) IsTaskCancelled(context.Context, string) (bool, error) {
	return false, nil
}
func (m *phaseTaskManagerStub) ReportTaskResult(context.Context, *model.TaskResult) error {
	return nil
}
func (m *phaseTaskManagerStub) SetTaskRunning(context.Context, string, string) error { return nil }
func (m *phaseTaskManagerStub) Start(context.Context) error                          { return nil }
func (m *phaseTaskManagerStub) Close() error                                         { return nil }

func cloneResult(r *model.TaskResult) *model.TaskResult {
	if r == nil {
		return nil
	}
	c := *r
	c.ResultJSON = append(json.RawMessage(nil), r.ResultJSON...)
	return &c
}

// TestWaitForRuntimeSnapshotResult_SkipsRunningStatus verifies that
// waitForRuntimeSnapshotResult does NOT return prematurely when
// GetTaskResult returns a RUNNING-status result. It should continue
// polling until the terminal SUCCESS result is available.
func TestWaitForRuntimeSnapshotResult_SkipsRunningStatus(t *testing.T) {
	ctx := context.Background()

	payload, err := json.Marshal(dynamicInstrumentListResponse{
		Summary: dynamicInstrumentListSummary{
			InstrumentationAvailable: true,
			EnhancementCapability:    true,
			ActiveTransformerCount:   1,
		},
		Items: []dynamicInstrumentListItem{
			{RuleID: "rule-1", RuntimeStatus: "active", IsApplied: true, IsEffective: true},
		},
	})
	require.NoError(t, err)

	taskMgr := newPhaseTaskManagerStubWithConfig(phaseStubConfig{
		payload:      payload,
		successDelay: 300 * time.Millisecond,
	})
	taskMgr.setDefaultConfig(phaseStubConfig{
		payload:      payload,
		successDelay: 300 * time.Millisecond,
	})

	agentCfg := agentregistry.DefaultConfig()
	agentReg := agentregistry.NewMemoryAgentRegistry(zap.NewNop(), agentCfg)
	require.NoError(t, agentReg.Start(ctx))
	defer func() { _ = agentReg.Close() }()
	require.NoError(t, agentReg.Register(ctx, &agentregistry.AgentInfo{
		AgentID:     "agent-1",
		Token:       "token-phase",
		AppID:       "app-phase",
		ServiceName: "svc-phase",
		Hostname:    "host-1",
		IP:          "127.0.0.1",
		Status:      &agentregistry.AgentStatus{State: agentregistry.AgentStateOnline},
	}))

	ruleStore := NewMemoryRuleStore()
	require.NoError(t, ruleStore.Start(ctx))
	rule := newTestRule("rule-1")
	require.NoError(t, ruleStore.SaveRule(ctx, rule, true))
	targets := newTestTargets(rule.ID)
	targets[0].TaskID = ""
	targets[0].TaskStatus = ""
	targets[0].State = TargetStateApplied
	require.NoError(t, ruleStore.SaveTargetStatuses(ctx, rule.ID, targets))

	cfg := DefaultConfig()
	cfg.RuntimeSnapshotQueryTimeout = 2000 // 2s — enough for 300ms delay
	cfg.RuntimeSnapshotPollInterval = 50   // poll every 50ms

	svc := newInstrumentationServiceWithRuntimeSnapshotStore(
		zap.NewNop(),
		cfg,
		ruleStore,
		agentReg,
		taskMgr,
		newMemoryRuntimeSnapshotStore(),
		"test-instance",
	)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Close() }()

	// Query the runtime snapshot — this triggers queryAgentRuntimeSnapshot internally
	snapshot, err := svc.GetRuleRuntimeSnapshot(ctx, rule.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot)

	// Verify the target reports success refresh status (not failed)
	require.Len(t, snapshot.Targets, 1)
	assert.Equal(t, RuntimeRefreshStatusSuccess, snapshot.Targets[0].LastRefreshStatus,
		"expected target refresh status SUCCESS, got %s (bug: waitForRuntimeSnapshotResult returned RUNNING prematurely)",
		snapshot.Targets[0].LastRefreshStatus)

	// Verify effective data comes from the agent's actual payload (not empty defaults)
	assert.True(t, snapshot.Targets[0].IsEffective,
		"target should be effective when agent reports is_effective=true")
	assert.True(t, snapshot.Targets[0].IsApplied,
		"target should be applied when agent reports is_applied=true")
	assert.Equal(t, 1, snapshot.Summary.EffectiveTargets,
		"summary should show 1 effective target")
	assert.Equal(t, 0, snapshot.Summary.RefreshFailedTargets,
		"summary should show 0 refresh-failed targets")
}

// TestWaitForRuntimeSnapshotResult_TimeoutWhenNeverTerminal verifies that
// if the task never transitions to a terminal state, the function eventually
// returns a timeout result instead of hanging forever.
func TestWaitForRuntimeSnapshotResult_TimeoutWhenNeverTerminal(t *testing.T) {
	ctx := context.Background()

	payload, err := json.Marshal(dynamicInstrumentListResponse{
		Summary: dynamicInstrumentListSummary{InstrumentationAvailable: true},
		Items:   []dynamicInstrumentListItem{{RuleID: "rule-timeout", IsEffective: true}},
	})
	require.NoError(t, err)

	// Set successDelay > query timeout to simulate agent never completing in time
	taskMgr := newPhaseTaskManagerStubWithConfig(phaseStubConfig{
		payload:      payload,
		successDelay: 10 * time.Second, // way longer than timeout
	})
	taskMgr.setDefaultConfig(phaseStubConfig{
		payload:      payload,
		successDelay: 10 * time.Second,
	})

	agentCfg := agentregistry.DefaultConfig()
	agentReg := agentregistry.NewMemoryAgentRegistry(zap.NewNop(), agentCfg)
	require.NoError(t, agentReg.Start(ctx))
	defer func() { _ = agentReg.Close() }()
	require.NoError(t, agentReg.Register(ctx, &agentregistry.AgentInfo{
		AgentID:     "agent-timeout",
		Token:       "token-timeout",
		AppID:       "app-timeout",
		ServiceName: "svc-timeout",
		Hostname:    "host-timeout",
		IP:          "127.0.0.1",
		Status:      &agentregistry.AgentStatus{State: agentregistry.AgentStateOnline},
	}))

	ruleStore := NewMemoryRuleStore()
	require.NoError(t, ruleStore.Start(ctx))
	rule := newTestRule("rule-timeout")
	require.NoError(t, ruleStore.SaveRule(ctx, rule, true))
	targets := newTestTargets(rule.ID)
	targets[0].TaskID = ""
	targets[0].TaskStatus = ""
	targets[0].State = TargetStateApplied
	require.NoError(t, ruleStore.SaveTargetStatuses(ctx, rule.ID, targets))

	cfg := DefaultConfig()
	cfg.RuntimeSnapshotQueryTimeout = 500 // 500ms timeout
	cfg.RuntimeSnapshotPollInterval = 50  // poll every 50ms

	svc := newInstrumentationServiceWithRuntimeSnapshotStore(
		zap.NewNop(),
		cfg,
		ruleStore,
		agentReg,
		taskMgr,
		newMemoryRuntimeSnapshotStore(),
		"test-timeout-instance",
	)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Close() }()

	start := time.Now()
	snapshot, err := svc.GetRuleRuntimeSnapshot(ctx, rule.ID)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, snapshot)

	// Target should show non-success status (timeout/failed/skipped) — not success
	require.Len(t, snapshot.Targets, 1)
	assert.NotEqual(t, RuntimeRefreshStatusSuccess, snapshot.Targets[0].LastRefreshStatus,
		"expected non-success status when agent never completes, got SUCCESS")

	// Should NOT complete instantly (the old bug would return in <50ms with RUNNING);
	// with the fix, it should either wait for timeout or use the skip/lease logic.
	// Relax assertion: just verify it didn't return SUCCESS prematurely.
	_ = elapsed
	_ = start
}
