// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func newTestMemoryStore(t *testing.T) *MemoryTaskStore {
	store := NewMemoryTaskStore(zap.NewNop(), time.Hour)
	err := store.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestMemoryTaskStore_SaveAndGetTaskInfo(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &controlplanev1.Task{
			TaskID:   "task-1",
			TaskType: "test",
		},
		Status:          controlplanev1.TaskStatusPending,
		CreatedAtMillis: time.Now().UnixMilli(),
	}

	// Save new task
	err := store.SaveTaskInfo(ctx, info, true)
	require.NoError(t, err)

	// Get task
	retrieved, err := store.GetTaskInfo(ctx, "task-1")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "task-1", retrieved.Task.TaskID)
	assert.Equal(t, controlplanev1.TaskStatusPending, retrieved.Status)
}

func TestMemoryTaskStore_SaveTaskInfo_Duplicate(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &controlplanev1.Task{
			TaskID:   "task-1",
			TaskType: "test",
		},
		Status: controlplanev1.TaskStatusPending,
	}

	// First save
	err := store.SaveTaskInfo(ctx, info, true)
	require.NoError(t, err)

	// Duplicate save should fail
	err = store.SaveTaskInfo(ctx, info, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Update (isNew=false) should succeed
	info.Status = controlplanev1.TaskStatusRunning
	err = store.SaveTaskInfo(ctx, info, false)
	require.NoError(t, err)
}

func TestMemoryTaskStore_UpdateTaskInfo(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &controlplanev1.Task{
			TaskID:   "task-1",
			TaskType: "test",
		},
		Status: controlplanev1.TaskStatusPending,
	}
	_ = store.SaveTaskInfo(ctx, info, true)

	// Update task
	err := store.UpdateTaskInfo(ctx, "task-1", func(i *TaskInfo) error {
		i.Status = controlplanev1.TaskStatusRunning
		i.AgentID = "agent-1"
		return nil
	})
	require.NoError(t, err)

	// Verify update
	retrieved, _ := store.GetTaskInfo(ctx, "task-1")
	assert.Equal(t, controlplanev1.TaskStatusRunning, retrieved.Status)
	assert.Equal(t, "agent-1", retrieved.AgentID)
}

func TestMemoryTaskStore_UpdateTaskInfo_NotFound(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	err := store.UpdateTaskInfo(ctx, "nonexistent", func(i *TaskInfo) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMemoryTaskStore_DeleteTaskInfo(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &controlplanev1.Task{
			TaskID:   "task-1",
			TaskType: "test",
		},
		Status: controlplanev1.TaskStatusPending,
	}
	_ = store.SaveTaskInfo(ctx, info, true)

	// Delete
	err := store.DeleteTaskInfo(ctx, "task-1")
	require.NoError(t, err)

	// Verify deleted
	retrieved, err := store.GetTaskInfo(ctx, "task-1")
	require.NoError(t, err)
	assert.Nil(t, retrieved)
}

func TestMemoryTaskStore_ListTaskInfos(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Add multiple tasks
	for i := 0; i < 3; i++ {
		info := &TaskInfo{
			Task: &controlplanev1.Task{
				TaskID:   "task-" + string(rune('a'+i)),
				TaskType: "test",
			},
			Status: controlplanev1.TaskStatusPending,
		}
		_ = store.SaveTaskInfo(ctx, info, true)
	}

	// List all
	tasks, err := store.ListTaskInfos(ctx)
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
}

func TestMemoryTaskStore_EnqueueDequeue(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Enqueue tasks
	_ = store.EnqueueTask(ctx, QueueGlobal, "task-1", 1, time.Now().UnixMilli())
	_ = store.EnqueueTask(ctx, QueueGlobal, "task-2", 10, time.Now().UnixMilli()) // Higher priority

	// Dequeue - should get higher priority first
	taskID, err := store.DequeueTask(ctx, QueueGlobal, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "task-2", taskID)

	taskID, err = store.DequeueTask(ctx, QueueGlobal, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "task-1", taskID)
}

func TestMemoryTaskStore_DequeueTaskMulti(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Enqueue to agent queue
	_ = store.EnqueueTask(ctx, "agent-1", "agent-task", 1, time.Now().UnixMilli())
	// Enqueue to global queue
	_ = store.EnqueueTask(ctx, QueueGlobal, "global-task", 1, time.Now().UnixMilli())

	// DequeueMulti should prefer agent queue
	taskID, err := store.DequeueTaskMulti(ctx, []string{"agent-1", QueueGlobal}, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "agent-task", taskID)

	// Next should get global task
	taskID, err = store.DequeueTaskMulti(ctx, []string{"agent-1", QueueGlobal}, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "global-task", taskID)
}

func TestMemoryTaskStore_DequeueTask_Timeout(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	start := time.Now()
	taskID, err := store.DequeueTask(ctx, QueueGlobal, 100*time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Empty(t, taskID)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
}

func TestMemoryTaskStore_PeekQueue(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	_ = store.EnqueueTask(ctx, QueueGlobal, "task-1", 1, time.Now().UnixMilli())
	_ = store.EnqueueTask(ctx, QueueGlobal, "task-2", 1, time.Now().UnixMilli())

	// Peek should not remove items
	taskIDs, err := store.PeekQueue(ctx, QueueGlobal)
	require.NoError(t, err)
	assert.Len(t, taskIDs, 2)

	// Peek again - should still have items
	taskIDs, err = store.PeekQueue(ctx, QueueGlobal)
	require.NoError(t, err)
	assert.Len(t, taskIDs, 2)
}

func TestMemoryTaskStore_RemoveFromQueue(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	_ = store.EnqueueTask(ctx, QueueGlobal, "task-1", 1, time.Now().UnixMilli())
	_ = store.EnqueueTask(ctx, QueueGlobal, "task-2", 1, time.Now().UnixMilli())

	// Remove task-1
	err := store.RemoveFromQueue(ctx, QueueGlobal, "task-1")
	require.NoError(t, err)

	// Only task-2 should remain
	taskIDs, _ := store.PeekQueue(ctx, QueueGlobal)
	assert.Len(t, taskIDs, 1)
	assert.Equal(t, "task-2", taskIDs[0])
}

func TestMemoryTaskStore_RemoveFromAllQueues(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Add to both queues
	_ = store.EnqueueTask(ctx, QueueGlobal, "task-1", 1, time.Now().UnixMilli())
	_ = store.EnqueueTask(ctx, "agent-1", "task-1", 1, time.Now().UnixMilli())

	// Remove from all
	err := store.RemoveFromAllQueues(ctx, "task-1", "agent-1")
	require.NoError(t, err)

	// Both queues should be empty
	globalTasks, _ := store.PeekQueue(ctx, QueueGlobal)
	agentTasks, _ := store.PeekQueue(ctx, "agent-1")
	assert.Empty(t, globalTasks)
	assert.Empty(t, agentTasks)
}

func TestMemoryTaskStore_SaveAndGetResult(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	result := &controlplanev1.TaskResult{
		TaskID:            "task-1",
		Status:            controlplanev1.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}

	// Save result
	err := store.SaveResult(ctx, result)
	require.NoError(t, err)

	// Get result
	retrieved, found, err := store.GetResult(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, controlplanev1.TaskStatusSuccess, retrieved.Status)
}

func TestMemoryTaskStore_GetResult_NotFound(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	result, found, err := store.GetResult(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, result)
}

func TestMemoryTaskStore_Cancellation(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Initially not cancelled
	cancelled, err := store.IsCancelled(ctx, "task-1")
	require.NoError(t, err)
	assert.False(t, cancelled)

	// Set cancelled
	err = store.SetCancelled(ctx, "task-1")
	require.NoError(t, err)

	// Now should be cancelled
	cancelled, err = store.IsCancelled(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, cancelled)
}

func TestMemoryTaskStore_RunningState(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// Initially not running
	agentID, err := store.GetRunning(ctx, "task-1")
	require.NoError(t, err)
	assert.Empty(t, agentID)

	// Set running
	err = store.SetRunning(ctx, "task-1", "agent-1")
	require.NoError(t, err)

	// Now should be running
	agentID, err = store.GetRunning(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", agentID)

	// Clear running
	err = store.ClearRunning(ctx, "task-1")
	require.NoError(t, err)

	// Should no longer be running
	agentID, err = store.GetRunning(ctx, "task-1")
	require.NoError(t, err)
	assert.Empty(t, agentID)
}

func TestMemoryTaskStore_PublishEvent(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// PublishEvent is a no-op for memory store, should not error
	err := store.PublishEvent(ctx, "submitted", "task-1")
	require.NoError(t, err)

	err = store.PublishEvent(ctx, "completed", "task-1")
	require.NoError(t, err)
}

func TestMemoryTaskStore_Lifecycle(t *testing.T) {
	store := NewMemoryTaskStore(zap.NewNop(), time.Hour)
	ctx := context.Background()

	// Start
	err := store.Start(ctx)
	require.NoError(t, err)

	// Double start
	err = store.Start(ctx)
	require.NoError(t, err)

	// Close
	err = store.Close()
	require.NoError(t, err)

	// Double close
	err = store.Close()
	require.NoError(t, err)
}

func TestMemoryTaskStore_ConcurrentAccess(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			taskID := "task-" + string(rune('0'+id))
			info := &TaskInfo{
				Task: &controlplanev1.Task{
					TaskID:   taskID,
					TaskType: "test",
				},
				Status: controlplanev1.TaskStatusPending,
			}
			_ = store.SaveTaskInfo(ctx, info, true)
			_, _ = store.GetTaskInfo(ctx, taskID)
			_ = store.EnqueueTask(ctx, QueueGlobal, taskID, 1, time.Now().UnixMilli())
			_, _ = store.PeekQueue(ctx, QueueGlobal)
			_, _ = store.IsCancelled(ctx, taskID)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
