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

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func newTestMemoryTaskManager(t *testing.T) *MemoryTaskManager {
	logger := zap.NewNop()
	config := Config{
		ResultTTL:      1 * time.Hour,
		Workers:        4,
		QueueSize:      100,
		DefaultTimeout: 30 * time.Second,
	}
	return NewMemoryTaskManager(logger, config)
}

func TestMemoryTaskManager_SubmitTask(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
		Priority: 1,
	}

	err = tm.SubmitTask(ctx, task)
	require.NoError(t, err)

	// Verify task status
	info, err := tm.GetTaskStatus(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, controlplanev1.TaskStatusPending, info.Status)
}

func TestMemoryTaskManager_SubmitTask_Validation(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Nil task
	err = tm.SubmitTask(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")

	// Empty task ID
	err = tm.SubmitTask(ctx, &controlplanev1.Task{TaskType: "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task_id is required")

	// Empty task type
	err = tm.SubmitTask(ctx, &controlplanev1.Task{TaskID: "task-1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task_type is required")

	// Duplicate task
	task := &controlplanev1.Task{TaskID: "task-1", TaskType: "test"}
	_ = tm.SubmitTask(ctx, task)
	err = tm.SubmitTask(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestMemoryTaskManager_SubmitTask_Expired(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &controlplanev1.Task{
		TaskID:            "task-1",
		TaskType:          "test",
		ExpiresAtUnixNano: time.Now().Add(-1 * time.Hour).UnixNano(), // Already expired
	}

	err = tm.SubmitTask(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestMemoryTaskManager_SubmitTaskForAgent(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
	}

	err = tm.SubmitTaskForAgent(ctx, "agent-1", task)
	require.NoError(t, err)

	// Verify task is in agent queue
	info, err := tm.GetTaskStatus(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", info.AgentID)
}

func TestMemoryTaskManager_FetchTask(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Fetch task
	fetched, err := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "task-1", fetched.TaskID)
}

func TestMemoryTaskManager_FetchTask_Priority(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit tasks with different priorities
	lowPriority := &controlplanev1.Task{
		TaskID:   "low",
		TaskType: "test",
		Priority: 1,
	}
	highPriority := &controlplanev1.Task{
		TaskID:   "high",
		TaskType: "test",
		Priority: 10,
	}

	_ = tm.SubmitTask(ctx, lowPriority)
	_ = tm.SubmitTask(ctx, highPriority)

	// High priority should be fetched first
	fetched, _ := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "high", fetched.TaskID)

	fetched, _ = tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "low", fetched.TaskID)
}

func TestMemoryTaskManager_FetchTask_AgentSpecific(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit agent-specific task
	agentTask := &controlplanev1.Task{
		TaskID:   "agent-task",
		TaskType: "test",
	}
	_ = tm.SubmitTaskForAgent(ctx, "agent-1", agentTask)

	// Submit global task
	globalTask := &controlplanev1.Task{
		TaskID:   "global-task",
		TaskType: "test",
	}
	_ = tm.SubmitTask(ctx, globalTask)

	// Agent-specific task should be fetched first
	fetched, _ := tm.FetchTask(ctx, "agent-1", 1*time.Second)
	assert.Equal(t, "agent-task", fetched.TaskID)
}

func TestMemoryTaskManager_FetchTask_Timeout(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
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

func TestMemoryTaskManager_CancelTask(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit and cancel
	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
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
	assert.Equal(t, controlplanev1.TaskStatusCancelled, info.Status)
}

func TestMemoryTaskManager_ReportTaskResult(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Report result
	result := &controlplanev1.TaskResult{
		TaskID:       "task-1",
		Status:       controlplanev1.TaskStatusSuccess,
		ErrorMessage: "",
	}
	err = tm.ReportTaskResult(ctx, result)
	require.NoError(t, err)

	// Get result
	retrieved, found, err := tm.GetTaskResult(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, controlplanev1.TaskStatusSuccess, retrieved.Status)
}

func TestMemoryTaskManager_ReportTaskResult_Validation(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	err = tm.ReportTaskResult(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

func TestMemoryTaskManager_GetTaskStatus_NotFound(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	_, err = tm.GetTaskStatus(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMemoryTaskManager_SetTaskRunning(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit task
	task := &controlplanev1.Task{
		TaskID:   "task-1",
		TaskType: "test",
	}
	_ = tm.SubmitTask(ctx, task)

	// Set running
	err = tm.SetTaskRunning(ctx, "task-1", "agent-1")
	require.NoError(t, err)

	// Verify
	info, _ := tm.GetTaskStatus(ctx, "task-1")
	assert.Equal(t, controlplanev1.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.NotZero(t, info.StartedAt)
}

func TestMemoryTaskManager_GetPendingTasks(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit tasks
	for i := 1; i <= 3; i++ {
		task := &controlplanev1.Task{
			TaskID:   "task-" + string(rune('0'+i)),
			TaskType: "test",
		}
		_ = tm.SubmitTask(ctx, task)
	}

	// Get pending
	tasks, err := tm.GetPendingTasks(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
}

func TestMemoryTaskManager_GetGlobalPendingTasks(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	// Submit global tasks
	for i := 1; i <= 2; i++ {
		task := &controlplanev1.Task{
			TaskID:   "global-" + string(rune('0'+i)),
			TaskType: "test",
		}
		_ = tm.SubmitTask(ctx, task)
	}

	// Submit agent-specific task
	agentTask := &controlplanev1.Task{
		TaskID:   "agent-task",
		TaskType: "test",
	}
	_ = tm.SubmitTaskForAgent(ctx, "agent-1", agentTask)

	// Get global pending
	tasks, err := tm.GetGlobalPendingTasks(ctx)
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestMemoryTaskManager_StartClose(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
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

func TestMemoryTaskManager_ConcurrentAccess(t *testing.T) {
	tm := newTestMemoryTaskManager(t)
	ctx := context.Background()

	err := tm.Start(ctx)
	require.NoError(t, err)
	defer tm.Close()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			task := &controlplanev1.Task{
				TaskID:   "task-" + string(rune('0'+id)),
				TaskType: "test",
			}
			_ = tm.SubmitTask(ctx, task)
			_, _, _ = tm.GetTaskResult(ctx, task.TaskID)
			_, _ = tm.GetTaskStatus(ctx, task.TaskID)
			_, _ = tm.GetPendingTasks(ctx, "agent")
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
