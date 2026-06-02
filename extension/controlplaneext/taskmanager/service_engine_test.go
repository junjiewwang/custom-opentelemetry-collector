// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/taskengine"
	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// ═══════════════════════════════════════════════════
// Mock Engine for testing
// ═══════════════════════════════════════════════════

type mockEngine struct {
	tasks   map[string]*taskengine.Task
	results map[string]*taskengine.TaskResult
	queue   []*taskengine.Task // simple FIFO for Claim

	submitCalled int
	cancelCalled int
	claimCalled  int
	reportCalled int
	started      bool
	stopped      bool
}

func newMockEngine() *mockEngine {
	return &mockEngine{
		tasks:   make(map[string]*taskengine.Task),
		results: make(map[string]*taskengine.TaskResult),
	}
}

func (m *mockEngine) Submit(_ context.Context, task *taskengine.Task) error {
	m.submitCalled++
	task.Status = taskengine.StatusPending
	task.CreatedAt = time.Now().UnixMilli()
	m.tasks[task.ID] = task
	m.queue = append(m.queue, task)
	return nil
}

func (m *mockEngine) SubmitBatch(ctx context.Context, tasks []*taskengine.Task) error {
	for _, t := range tasks {
		if err := m.Submit(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockEngine) Cancel(_ context.Context, taskID string) error {
	m.cancelCalled++
	if task, ok := m.tasks[taskID]; ok {
		task.Status = taskengine.StatusCancelled
	}
	return nil
}

func (m *mockEngine) Claim(_ context.Context, consumer *taskengine.ConsumerDescriptor) (*taskengine.Task, error) {
	m.claimCalled++
	if len(m.queue) == 0 {
		return nil, nil
	}

	// Find a task matching the consumer's direct queue or global
	for i, task := range m.queue {
		if task.Routing.Strategy == taskengine.RoutingDirect {
			if task.Routing.TargetNodeID == consumer.ID {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				task.Status = taskengine.StatusRunning
				task.ClaimedBy = consumer.ID
				return task, nil
			}
		} else {
			// Broadcast — anyone can claim
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			task.Status = taskengine.StatusRunning
			task.ClaimedBy = consumer.ID
			return task, nil
		}
	}
	return nil, nil
}

func (m *mockEngine) Report(_ context.Context, result *taskengine.TaskResult) error {
	m.reportCalled++
	m.results[result.TaskID] = result
	if task, ok := m.tasks[result.TaskID]; ok {
		task.Status = result.Status
	}
	return nil
}

func (m *mockEngine) GetTask(_ context.Context, taskID string) (*taskengine.Task, error) {
	return m.tasks[taskID], nil
}

func (m *mockEngine) GetResult(_ context.Context, taskID string) (*taskengine.TaskResult, error) {
	return m.results[taskID], nil
}

func (m *mockEngine) GetProgress(_ context.Context, _ taskengine.TaskType, _ string) (*taskengine.Progress, error) {
	return &taskengine.Progress{}, nil
}

func (m *mockEngine) ListTasks(_ context.Context, query taskengine.ListQuery) (*taskengine.ListPage, error) {
	var matched []*taskengine.Task
	for _, t := range m.tasks {
		if query.Status != "" && t.Status != query.Status {
			continue
		}
		if query.TaskType != "" && t.Type != query.TaskType {
			continue
		}
		matched = append(matched, t)
	}

	limit := query.Limit
	if limit == 0 {
		limit = 100
	}
	offset := query.Offset
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}

	return &taskengine.ListPage{
		Tasks:  matched[offset:end],
		Total:  len(matched),
		Offset: offset,
		Limit:  limit,
	}, nil
}

func (m *mockEngine) Start(_ context.Context) error {
	m.started = true
	return nil
}

func (m *mockEngine) Stop(_ context.Context) error {
	m.stopped = true
	return nil
}

// ═══════════════════════════════════════════════════
// Tests
// ═══════════════════════════════════════════════════

func newTestFacade(engine *mockEngine) *TaskServiceEngine {
	logger := zaptest.NewLogger(&testing.T{})
	return NewTaskServiceEngine(engine, logger, DefaultConfig())
}

func TestTaskServiceEngine_SubmitTask(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	task := &model.Task{
		ID:             "task-001",
		TypeName:       "arthas_attach",
		ParametersJSON: json.RawMessage(`{"pid": 1234}`),
		PriorityNum:    5,
		TimeoutMillis:  30000,
	}

	err := svc.SubmitTask(context.Background(), task)
	require.NoError(t, err)
	assert.Equal(t, 1, engine.submitCalled)

	// Verify engine received correct task
	engineTask := engine.tasks["task-001"]
	require.NotNil(t, engineTask)
	assert.Equal(t, taskengine.TaskTypeArthasAttach, engineTask.Type)
	assert.Equal(t, int32(5), engineTask.Priority)
	assert.Equal(t, taskengine.RoutingBroadcast, engineTask.Routing.Strategy)
	assert.Equal(t, 30*time.Second, engineTask.Timeout)
}

func TestTaskServiceEngine_SubmitTaskForAgent(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	agentMeta := &AgentMeta{
		AgentID:     "agent-abc",
		AppID:       "app-123",
		ServiceName: "my-service",
	}
	task := &model.Task{
		ID:       "task-002",
		TypeName: "arthas_detach",
	}

	err := svc.SubmitTaskForAgent(context.Background(), agentMeta, task)
	require.NoError(t, err)

	engineTask := engine.tasks["task-002"]
	require.NotNil(t, engineTask)
	assert.Equal(t, taskengine.RoutingDirect, engineTask.Routing.Strategy)
	assert.Equal(t, "agent-abc", engineTask.Routing.TargetNodeID)
	assert.Equal(t, "app-123", engineTask.Metadata["app_id"])
	assert.Equal(t, "my-service", engineTask.Metadata["service_name"])
}

func TestTaskServiceEngine_FetchTask(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit a task first
	task := &model.Task{
		ID:       "task-003",
		TypeName: "arthas_exec_sync",
	}
	require.NoError(t, svc.SubmitTask(context.Background(), task))

	// Fetch it
	fetched, err := svc.FetchTask(context.Background(), "agent-xyz", 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "task-003", fetched.ID)
	assert.Equal(t, "arthas_exec_sync", fetched.TypeName)
}

func TestTaskServiceEngine_FetchTask_Timeout(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// No tasks available — should timeout
	fetched, err := svc.FetchTask(context.Background(), "agent-xyz", 200*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestTaskServiceEngine_FetchTask_DirectRouting(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit a task targeted at specific agent
	agentMeta := &AgentMeta{AgentID: "agent-target"}
	task := &model.Task{
		ID:       "task-004",
		TypeName: "arthas_attach",
	}
	require.NoError(t, svc.SubmitTaskForAgent(context.Background(), agentMeta, task))

	// Different agent should not get it
	fetched, err := svc.FetchTask(context.Background(), "other-agent", 200*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, fetched)

	// Target agent should get it
	fetched, err = svc.FetchTask(context.Background(), "agent-target", 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "task-004", fetched.ID)
}

func TestTaskServiceEngine_CancelTask(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit and cancel
	task := &model.Task{ID: "task-005", TypeName: "arthas_attach"}
	require.NoError(t, svc.SubmitTask(context.Background(), task))
	require.NoError(t, svc.CancelTask(context.Background(), "task-005"))

	assert.Equal(t, 1, engine.cancelCalled)

	// Verify cancelled
	isCancelled, err := svc.IsTaskCancelled(context.Background(), "task-005")
	require.NoError(t, err)
	assert.True(t, isCancelled)
}

func TestTaskServiceEngine_ReportTaskResult(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit and claim a task first
	task := &model.Task{ID: "task-006", TypeName: "arthas_exec_sync"}
	require.NoError(t, svc.SubmitTask(context.Background(), task))
	_, _ = svc.FetchTask(context.Background(), "agent-a", 5*time.Second)

	// Report result
	result := &model.TaskResult{
		TaskID:            "task-006",
		AgentID:           "agent-a",
		Status:            model.TaskStatusSuccess,
		ResultJSON:        json.RawMessage(`{"output": "ok"}`),
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err := svc.ReportTaskResult(context.Background(), result)
	require.NoError(t, err)
	assert.Equal(t, 1, engine.reportCalled)

	// Verify result is retrievable
	got, found, err := svc.GetTaskResult(context.Background(), "task-006")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, model.TaskStatusSuccess, got.Status)
	assert.Equal(t, "agent-a", got.AgentID)
}

func TestTaskServiceEngine_ReportTaskResult_Running(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit a task (stays pending)
	task := &model.Task{ID: "task-007", TypeName: "arthas_attach"}
	require.NoError(t, svc.SubmitTask(context.Background(), task))

	// Report RUNNING — should trigger claim
	result := &model.TaskResult{
		TaskID:  "task-007",
		AgentID: "agent-b",
		Status:  model.TaskStatusRunning,
	}
	err := svc.ReportTaskResult(context.Background(), result)
	require.NoError(t, err)
	assert.Greater(t, engine.claimCalled, 0)
}

func TestTaskServiceEngine_GetTaskStatus(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	task := &model.Task{ID: "task-008", TypeName: "arthas_session_open"}
	require.NoError(t, svc.SubmitTask(context.Background(), task))

	info, err := svc.GetTaskStatus(context.Background(), "task-008")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, model.TaskStatusPending, info.Status)
	assert.Equal(t, "task-008", info.Task.ID)
}

func TestTaskServiceEngine_GetTaskStatus_NotFound(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	_, err := svc.GetTaskStatus(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTaskServiceEngine_GetAllTasks(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit multiple tasks
	for i := 0; i < 3; i++ {
		task := &model.Task{
			ID:       "task-batch-" + string(rune('a'+i)),
			TypeName: "arthas_attach",
		}
		require.NoError(t, svc.SubmitTask(context.Background(), task))
	}

	all, err := svc.GetAllTasks(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestTaskServiceEngine_GetPendingTasks(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit 2 broadcast tasks + 1 directed task
	require.NoError(t, svc.SubmitTask(context.Background(), &model.Task{ID: "t1", TypeName: "arthas_attach"}))
	require.NoError(t, svc.SubmitTask(context.Background(), &model.Task{ID: "t2", TypeName: "arthas_detach"}))
	require.NoError(t, svc.SubmitTaskForAgent(context.Background(), &AgentMeta{AgentID: "agent-x"}, &model.Task{ID: "t3", TypeName: "arthas_attach"}))

	// Agent-x should see its directed task + broadcast tasks
	pending, err := svc.GetPendingTasks(context.Background(), "agent-x")
	require.NoError(t, err)
	assert.Len(t, pending, 3)

	// Other agent should only see broadcast tasks
	pending, err = svc.GetPendingTasks(context.Background(), "other-agent")
	require.NoError(t, err)
	assert.Len(t, pending, 2)
}

func TestTaskServiceEngine_ListTasks(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Submit some tasks
	require.NoError(t, svc.SubmitTask(context.Background(), &model.Task{ID: "l1", TypeName: "arthas_attach"}))
	require.NoError(t, svc.SubmitTask(context.Background(), &model.Task{ID: "l2", TypeName: "arthas_detach"}))

	page, err := svc.ListTasks(context.Background(), ListTasksQuery{
		Statuses: []model.TaskStatus{model.TaskStatusPending},
		Limit:    10,
	})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.False(t, page.HasMore)
}

func TestTaskServiceEngine_Lifecycle(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	require.NoError(t, svc.Start(context.Background()))
	assert.True(t, engine.started)

	require.NoError(t, svc.Close())
	assert.True(t, engine.stopped)
}

func TestTaskServiceEngine_Validation(t *testing.T) {
	engine := newMockEngine()
	svc := newTestFacade(engine)

	// Nil task
	err := svc.SubmitTask(context.Background(), nil)
	require.Error(t, err)

	// Empty ID
	err = svc.SubmitTask(context.Background(), &model.Task{TypeName: "test"})
	require.Error(t, err)

	// Empty type name
	err = svc.SubmitTask(context.Background(), &model.Task{ID: "test"})
	require.Error(t, err)

	// Expired task
	err = svc.SubmitTask(context.Background(), &model.Task{
		ID:              "test",
		TypeName:        "test",
		ExpiresAtMillis: 1, // expired long ago
	})
	require.Error(t, err)
}

// ═══════════════════════════════════════════════════
// Model Conversion Tests
// ═══════════════════════════════════════════════════

func TestModelConversion_TaskRoundTrip(t *testing.T) {
	original := &model.Task{
		ID:              "roundtrip-1",
		TypeName:        "arthas_attach",
		ParametersJSON:  json.RawMessage(`{"target": "jvm"}`),
		PriorityNum:     10,
		TimeoutMillis:   60000,
		CreatedAtMillis: 1700000000000,
		ExpiresAtMillis: 1700003600000,
		TargetAgentID:   "agent-42",
	}

	// Convert to engine and back
	engineTask := controlplaneTaskToEngine(original)
	converted := engineTaskToControlplane(engineTask)

	assert.Equal(t, original.ID, converted.ID)
	assert.Equal(t, original.TypeName, converted.TypeName)
	assert.Equal(t, original.ParametersJSON, converted.ParametersJSON)
	assert.Equal(t, original.PriorityNum, converted.PriorityNum)
	assert.Equal(t, original.TimeoutMillis, converted.TimeoutMillis)
	assert.Equal(t, original.CreatedAtMillis, converted.CreatedAtMillis)
	assert.Equal(t, original.ExpiresAtMillis, converted.ExpiresAtMillis)
	assert.Equal(t, original.TargetAgentID, converted.TargetAgentID)
}

func TestModelConversion_StatusBidirectional(t *testing.T) {
	tests := []struct {
		cpStatus     model.TaskStatus
		engineStatus taskengine.TaskStatus
	}{
		{model.TaskStatusPending, taskengine.StatusPending},
		{model.TaskStatusRunning, taskengine.StatusRunning},
		{model.TaskStatusSuccess, taskengine.StatusSuccess},
		{model.TaskStatusFailed, taskengine.StatusFailed},
		{model.TaskStatusTimeout, taskengine.StatusTimeout},
		{model.TaskStatusCancelled, taskengine.StatusCancelled},
	}

	for _, tt := range tests {
		t.Run(string(tt.engineStatus), func(t *testing.T) {
			// Forward
			got := controlplaneStatusToEngine(tt.cpStatus)
			assert.Equal(t, tt.engineStatus, got)
			// Reverse
			back := engineStatusToControlplane(tt.engineStatus)
			assert.Equal(t, tt.cpStatus, back)
		})
	}
}

func TestModelConversion_TaskTypeMapping(t *testing.T) {
	tests := []struct {
		cpType     string
		engineType taskengine.TaskType
	}{
		{"arthas_attach", taskengine.TaskTypeArthasAttach},
		{"arthas_detach", taskengine.TaskTypeArthasDetach},
		{"arthas_exec_sync", taskengine.TaskTypeArthasExecSync},
		{"arthas_session_open", taskengine.TaskTypeArthasSessionOpen},
		{"arthas_session_exec", taskengine.TaskTypeArthasSessionExec},
		{"arthas_session_pull", taskengine.TaskTypeArthasSessionPull},
		{"arthas_session_close", taskengine.TaskTypeArthasSessionClose},
	}

	for _, tt := range tests {
		t.Run(tt.cpType, func(t *testing.T) {
			got := controlplaneTypeToEngine(tt.cpType)
			assert.Equal(t, tt.engineType, got)
			back := engineTypeToControlplane(tt.engineType)
			assert.Equal(t, tt.cpType, back)
		})
	}
}

func TestModelConversion_UnknownType(t *testing.T) {
	// Unknown type should pass through as-is
	got := controlplaneTypeToEngine("custom_action")
	assert.Equal(t, taskengine.TaskType("custom_action"), got)

	back := engineTypeToControlplane(taskengine.TaskType("custom_action"))
	assert.Equal(t, "custom_action", back)
}

func TestModelConversion_Routing(t *testing.T) {
	// With agent ID → Direct
	routing := controlplaneRoutingToEngine("agent-123")
	assert.Equal(t, taskengine.RoutingDirect, routing.Strategy)
	assert.Equal(t, "agent-123", routing.TargetNodeID)

	// Empty agent ID → Broadcast
	routing = controlplaneRoutingToEngine("")
	assert.Equal(t, taskengine.RoutingBroadcast, routing.Strategy)
	assert.Empty(t, routing.TargetNodeID)
}

func TestModelConversion_ConsumerDescriptor(t *testing.T) {
	consumer := agentToConsumerDescriptor("agent-xyz")
	assert.Equal(t, "agent-xyz", consumer.ID)
	assert.Contains(t, consumer.Roles, node.RoleAgent)
	assert.True(t, consumer.Capabilities.Has(node.CapArthasExec))
}

func TestModelConversion_ResultRoundTrip(t *testing.T) {
	original := &model.TaskResult{
		TaskID:            "result-1",
		AgentID:           "agent-a",
		Status:            model.TaskStatusSuccess,
		ResultJSON:        json.RawMessage(`{"data": "value"}`),
		StartedAtMillis:   1700000000000,
		CompletedAtMillis: 1700000010000,
		RetryCount:        2,
	}

	engineResult := controlplaneResultToEngine(original)
	converted := engineResultToControlplane(engineResult)

	assert.Equal(t, original.TaskID, converted.TaskID)
	assert.Equal(t, original.AgentID, converted.AgentID)
	assert.Equal(t, original.Status, converted.Status)
	assert.Equal(t, original.ResultJSON, converted.ResultJSON)
	assert.Equal(t, original.StartedAtMillis, converted.StartedAtMillis)
	assert.Equal(t, original.CompletedAtMillis, converted.CompletedAtMillis)
	assert.Equal(t, original.RetryCount, converted.RetryCount)
}

func TestNewTaskManagerWithEngine(t *testing.T) {
	engine := newMockEngine()
	logger := zaptest.NewLogger(t)

	mgr, err := NewTaskManagerWithEngine(logger, DefaultConfig(), engine)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	// Verify it implements the interface
	_, ok := mgr.(TaskManager)
	assert.True(t, ok)
}

func TestNewTaskManagerWithEngine_NilEngine(t *testing.T) {
	logger := zaptest.NewLogger(t)

	_, err := NewTaskManagerWithEngine(logger, DefaultConfig(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "engine is required")
}
