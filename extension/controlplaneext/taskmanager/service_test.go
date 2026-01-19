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
	task := &model.Task{
		ID:          "test-task-1",
		TypeName:    "test",
		PriorityNum: 1,
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Fetch the task
	fetched, err := service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "test-task-1", fetched.ID)
}

func TestTaskService_SubmitForAgent(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task for specific agent
	task := &model.Task{
		ID:       "agent-task-1",
		TypeName: "test",
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
	assert.Equal(t, "agent-task-1", fetched.ID)
}

func TestTaskService_DuplicateTask(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	task := &model.Task{
		ID:       "dup-task",
		TypeName: "test",
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
	task := &model.Task{
		ID:       "cancel-task",
		TypeName: "test",
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
	task := &model.Task{
		ID:       "result-task",
		TypeName: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report running status
	result := &model.TaskResult{
		TaskID:  "result-task",
		Status:  model.TaskStatusRunning,
		AgentID: "agent-1",
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Check status
	info, err := service.GetTaskStatus(ctx, "result-task")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)

	// Report success
	result = &model.TaskResult{
		TaskID:            "result-task",
		Status:            model.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Check final status
	info, err = service.GetTaskStatus(ctx, "result-task")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_RejectRollback(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &model.Task{
		ID:       "sm-task",
		TypeName: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &model.TaskResult{
		TaskID:            "sm-task",
		Status:            model.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Try to report running after success
	// Now treated as idempotent/no-op (once terminal, further updates are ignored)
	result = &model.TaskResult{
		TaskID: "sm-task",
		Status: model.TaskStatusRunning,
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Verify the original status is preserved
	info, err := service.GetTaskStatus(ctx, "sm-task")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_TerminalConflict(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &model.Task{
		ID:       "conflict-task",
		TypeName: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &model.TaskResult{
		TaskID:            "conflict-task",
		Status:            model.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Try to report failed after success (terminal conflict)
	// Now treated as idempotent - first terminal state wins, no error returned
	result = &model.TaskResult{
		TaskID:            "conflict-task",
		Status:            model.TaskStatusFailed,
		CompletedAtMillis: time.Now().UnixMilli(),
	}
	err = service.ReportTaskResult(ctx, result)
	require.NoError(t, err) // No error - treated as already completed

	// Verify the original status is preserved
	info, err := service.GetTaskStatus(ctx, "conflict-task")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, info.Status)
}

func TestTaskService_StateMachine_Idempotent(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit a task
	task := &model.Task{
		ID:       "idempotent-task",
		TypeName: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Report success
	result := &model.TaskResult{
		TaskID:            "idempotent-task",
		Status:            model.TaskStatusSuccess,
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
		task := &model.Task{
			ID:       "all-task-" + string(rune('a'+i)),
			TypeName: "test",
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
	task := &model.Task{
		ID:       "running-task",
		TypeName: "test",
	}
	err := service.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Set running
	err = service.SetTaskRunning(ctx, "running-task", "agent-1")
	require.NoError(t, err)

	// Check status
	info, err := service.GetTaskStatus(ctx, "running-task")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.Greater(t, info.StartedAtMillis, int64(0))
}

func TestTaskService_PriorityQueue(t *testing.T) {
	service := newTestTaskService(t)
	ctx := context.Background()

	// Submit tasks with different priorities
	tasks := []*model.Task{
		{ID: "low-priority", TypeName: "test", PriorityNum: 1},
		{ID: "high-priority", TypeName: "test", PriorityNum: 10},
		{ID: "medium-priority", TypeName: "test", PriorityNum: 5},
	}

	for _, task := range tasks {
		err := service.SubmitTask(ctx, task)
		require.NoError(t, err)
	}

	// Fetch tasks - should come in priority order
	fetched, err := service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "high-priority", fetched.ID)

	fetched, err = service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "medium-priority", fetched.ID)

	fetched, err = service.FetchTask(ctx, "agent-1", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "low-priority", fetched.ID)
}
