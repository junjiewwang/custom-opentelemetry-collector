// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// newTestTaskManager creates a TaskManager using the new architecture for testing.
func newTestTaskManager(_ *testing.T) TaskManager {
	logger := zap.NewNop()
	config := Config{
		ResultTTL:      1 * time.Hour,
		Workers:        4,
		QueueSize:      100,
		DefaultTimeout: 30 * time.Second,
	}
	tm, _ := NewTaskManager(logger, config, nil)
	return tm
}

func TestTaskManager_SubmitTask(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &model.Task{
		ID:          "task-1",
		TypeName:    "test",
		PriorityNum: 1,
	}

	err = tm.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Verify task status
	info, err := tm.GetTaskStatus(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusPending, info.Status)
}

func TestTaskManager_SubmitTask_Validation(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Nil task
	err = tm.SubmitTask(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")

	// Empty task ID
	err = tm.SubmitTask(ctx, &model.Task{TypeName: "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task_id is required")

	// Empty task type
	err = tm.SubmitTask(ctx, &model.Task{ID: "task-1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task_type")

	// Duplicate task
	task := &model.Task{ID: "task-1", TypeName: "test"}
	_ = tm.SubmitTask(ctx, task)
	err = tm.SubmitTask(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestTaskManager_SubmitTask_Expired(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &model.Task{
		ID:              "task-1",
		TypeName:        "test",
		ExpiresAtMillis: time.Now().Add(-1 * time.Hour).UnixMilli(), // Already expired
	}

	err = tm.SubmitTask(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestTaskManager_SubmitTaskForAgent(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &model.Task{
		ID:       "task-1",
		TypeName: "test",
	}

	agentMeta := &AgentMeta{AgentID: "agent-1", AppID: "app-1", ServiceName: "svc-1"}
	err = tm.SubmitTaskForAgent(ctx, agentMeta, task)
	require.NoError(t, err)

	// Verify task is in agent queue
	info, err := tm.GetTaskStatus(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.Equal(t, "app-1", info.AppID)
	assert.Equal(t, "svc-1", info.ServiceName)
}

func TestTaskManager_FetchTask(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &model.Task{
		ID:       "task-1",
		TypeName: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Fetch task
	fetched, err := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "task-1", fetched.ID)
}

func TestTaskManager_FetchTask_Priority(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit tasks with different priorities
	lowPriority := &model.Task{
		ID:          "low",
		TypeName:    "test",
		PriorityNum: 1,
	}
	highPriority := &model.Task{
		ID:          "high",
		TypeName:    "test",
		PriorityNum: 10,
	}

	_ = tm.SubmitTask(ctx, lowPriority)
	_ = tm.SubmitTask(ctx, highPriority)

	// High priority should be fetched first
	fetched, _ := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "high", fetched.ID)

	fetched, _ = tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "low", fetched.ID)
}

func TestTaskManager_FetchTask_AgentSpecific(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit agent-specific task
	agentTask := &model.Task{
		ID:       "agent-task",
		TypeName: "test",
	}
	_ = tm.SubmitTaskForAgent(ctx, &AgentMeta{AgentID: "agent-1"}, agentTask)

	// Submit global task
	globalTask := &model.Task{
		ID:       "global-task",
		TypeName: "test",
	}
	_ = tm.SubmitTask(ctx, globalTask)

	// Agent-specific task should be fetched first
	fetched, _ := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "agent-task", fetched.ID)
}

func TestTaskManager_FetchTask_Timeout(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// No tasks, should timeout
	start := time.Now()
	fetched, err := tm.FetchTask(ctx, "agent-1", 200*time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Nil(t, fetched)
	assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
}

func TestTaskManager_CancelTask(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit and cancel
	task := &model.Task{
		ID:       "task-1",
		TypeName: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	err = tm.CancelTask(ctx, "task-1")
	require.NoError(t, err)

	// Verify cancelled
	cancelled, err := tm.IsTaskCancelled(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, cancelled)

	// Status should be cancelled
	info, _ := tm.GetTaskStatus(ctx, "task-1")
	assert.Equal(t, model.TaskStatusCancelled, info.Status)
}

func TestTaskManager_ReportTaskResult(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &model.Task{
		ID:       "task-1",
		TypeName: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Report result
	result := &model.TaskResult{
		TaskID:       "task-1",
		Status:       model.TaskStatusSuccess,
		ErrorMessage: "",
	}
	err = tm.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Get result
	retrieved, found, err := tm.GetTaskResult(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, model.TaskStatusSuccess, retrieved.Status)
}

func TestTaskManager_ReportTaskResult_Running_RemovesFromQueue(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task for specific agent
	task := &model.Task{ID: "task-1", TypeName: "test"}
	err = tm.SubmitTaskForAgent(ctx, &AgentMeta{AgentID: "agent-1"}, task)
	require.NoError(t, err)

	// Verify it is pending
	pending, err := tm.GetPendingTasks(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "task-1", pending[0].ID)

	// Report RUNNING (agent accepted and started)
	running := &model.TaskResult{TaskID: "task-1", Status: model.TaskStatusRunning, AgentID: "agent-1"}
	err = tm.ReportTaskResult(ctx, running)
	require.NoError(t, err)

	// Should be removed from pending queues
	pending, err = tm.GetPendingTasks(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, pending, 0)

	// Status should be RUNNING with started_at set
	info, err := tm.GetTaskStatus(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.NotZero(t, info.StartedAtMillis)
}

func TestTaskManager_ReportTaskResult_Validation(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	err = tm.ReportTaskResult(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

func TestTaskManager_GetTaskStatus_NotFound(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	_, err = tm.GetTaskStatus(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTaskManager_SetTaskRunning(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &model.Task{
		ID:       "task-1",
		TypeName: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Set running
	err = tm.SetTaskRunning(ctx, "task-1", "agent-1")
	require.NoError(t, err)

	// Verify
	info, _ := tm.GetTaskStatus(ctx, "task-1")
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.NotZero(t, info.StartedAtMillis)
}

func TestTaskManager_GetPendingTasks(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit tasks
	for i := 1; i <= 3; i++ {
		task := &model.Task{
			ID:       "task-" + string(rune('0'+i)),
			TypeName: "test",
		}
		_ = tm.SubmitTask(ctx, task)
	}

	// Get pending
	tasks, err := tm.GetPendingTasks(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
}

func TestTaskManager_GetGlobalPendingTasks(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit global tasks
	for i := 1; i <= 2; i++ {
		task := &model.Task{
			ID:       "global-" + string(rune('0'+i)),
			TypeName: "test",
		}
		_ = tm.SubmitTask(ctx, task)
	}

	// Submit agent-specific task
	agentTask := &model.Task{
		ID:       "agent-task",
		TypeName: "test",
	}
	_ = tm.SubmitTaskForAgent(ctx, &AgentMeta{AgentID: "agent-1"}, agentTask)

	// Get global pending
	tasks, err := tm.GetGlobalPendingTasks(ctx)
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestTaskManager_StartClose(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	// Start
	err := tm.Start(ctx)
	require.NoError(t, err)

	// Double start
	err = tm.Start(ctx)
	require.NoError(t, err)

	// Close
	err = tm.Close()
	require.NoError(t, err)

	// Double close
	err = tm.Close()
	require.NoError(t, err)
}

func TestTaskManager_ConcurrentAccess(t *testing.T) {
	tm := newTestTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			task := &model.Task{
				ID:       "task-" + string(rune('0'+id)),
				TypeName: "test",
			}
			_ = tm.SubmitTask(ctx, task)
			_, _, _ = tm.GetTaskResult(ctx, task.ID)
			_, _ = tm.GetTaskStatus(ctx, task.ID)
			_, _ = tm.GetPendingTasks(ctx, "agent")
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
