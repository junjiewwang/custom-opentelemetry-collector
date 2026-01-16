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

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane_legacy/v1"
)

func newTestTaskService(t *testing.T) *TaskService {
	logger := zap.NewNop()
	config := DefaultConfig()
	config.ResultTTL = time.Hour

	tm, err := NewTaskManager(logger, config, nil)
	require.NoError(t, err)
	service, ok := tm.(*TaskService)
	require.True(t, ok)

	err = service.Start(context.Background())
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = service.Close()
	})

	return service
}

func TestTaskService_SubmitAndFetch(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "test-task-1",
		TaskType: "test",
		Priority: 1,
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Fetch the task
	fetched, err := service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "test-task-1", fetched.TaskID)
}

func TestTaskService_SubmitForAgent(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task for specific agent
	task := &controlplanev1.Task{
		TaskID:   "agent-task-1",
		TaskType: "test",
	}
	agentMeta := &AgentMeta{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
	}
	err := service.SubmitTaskForAgent(ctx, agentMeta, task)
	require.NoError(t, err)

	// Different agent should not get the task
	fetched, err := service.FetchTask(ctx, "agent-2", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, fetched)

	// Correct agent should get the task
	fetched, err = service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "agent-task-1", fetched.TaskID)
}

func TestTaskService_DuplicateTask(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	task := &controlplanev1.Task{
		TaskID:   "dup-task",
		TaskType: "test",
	}

	// First submission should succeed
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Second submission should fail
	err = service.SubmitTask(ctx, task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestTaskService_CancelTask(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "cancel-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Cancel the task
	err = service.CancelTask(ctx, "cancel-task")
	require.NoError(t, err)

	// Check cancelled status
	cancelled, err := service.IsTaskCancelled(ctx, "cancel-task")
	require.NoError(t, err)
	assert.True(t, cancelled)

	// Fetch should not return cancelled task
	fetched, err := service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestTaskService_ReportResult(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "result-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report running status
	result := &controlplanev1.TaskResult{
		TaskID:  "result-task",
		Status:  controlplanev1.TaskStatusRunning,
		AgentID: "agent-1",
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Check status
	info, err := service.GetTaskStatus(ctx, "result-task")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)

	// Report success
	result = &controlplanev1.TaskResult{
		TaskID:            "result-task",
		Status:            controlplanev1.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Check final status
	info, err = service.GetTaskStatus(ctx, "result-task")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_RejectRollback(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "sm-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &controlplanev1.TaskResult{
		TaskID:            "sm-task",
		Status:            controlplanev1.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Try to report running after success
	// Now treated as idempotent/no-op (once terminal, further updates are ignored)
	result = &controlplanev1.TaskResult{
		TaskID: "sm-task",
		Status: controlplanev1.TaskStatusRunning,
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Verify the original status is preserved
	info, err := service.GetTaskStatus(ctx, "sm-task")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_TerminalConflict(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "conflict-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &controlplanev1.TaskResult{
		TaskID:            "conflict-task",
		Status:            controlplanev1.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Try to report failed after success (terminal conflict)
	// Now treated as idempotent - first terminal state wins, no error returned
	result = &controlplanev1.TaskResult{
		TaskID:            "conflict-task",
		Status:            controlplanev1.TaskStatusFailed,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err) // No error - treated as already completed

	// Verify the original status is preserved
	info, err := service.GetTaskStatus(ctx, "conflict-task")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_Idempotent(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "idempotent-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &controlplanev1.TaskResult{
		TaskID:            "idempotent-task",
		Status:            controlplanev1.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Report success again (should be idempotent, no error)
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)
}

func TestTaskService_GetAllTasks(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit multiple tasks
	for i := 0; i < 3; i++ {
		task := &controlplanev1.Task{
			TaskID:   "all-task-" + string(rune('a'+i)),
			TaskType: "test",
		}
		err := service.SubmitTask(ctx, task)
		require.NoError(t, err)
	}

	// Get all tasks
	tasks, err := service.GetAllTasks(ctx)
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
}

func TestTaskService_SetTaskRunning(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &controlplanev1.Task{
		TaskID:   "running-task",
		TaskType: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Set running
	err = service.SetTaskRunning(ctx, "running-task", "agent-1")
	require.NoError(t, err)

	// Check status
	info, err := service.GetTaskStatus(ctx, "running-task")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.Greater(t, info.StartedAtMillis, int64(0))
}

func TestTaskService_PriorityQueue(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit tasks with different priorities
	tasks := []*controlplanev1.Task{
		{TaskID: "low-priority", TaskType: "test", Priority: 1},
		{TaskID: "high-priority", TaskType: "test", Priority: 10},
		{TaskID: "medium-priority", TaskType: "test", Priority: 5},
	}

	for _, task := range tasks {
		err := service.SubmitTask(ctx, task)
		require.NoError(t, err)
	}

	// Fetch tasks - should come in priority order
	fetched, err := service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "high-priority", fetched.TaskID)

	fetched, err = service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "medium-priority", fetched.TaskID)

	fetched, err = service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "low-priority", fetched.TaskID)
}
