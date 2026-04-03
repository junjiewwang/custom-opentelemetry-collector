// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
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
		Task: &model.Task{
			ID:       "task-1",
			TypeName: "test",
		},
		Status:          model.TaskStatusPending,
		CreatedAtMillis: time.Now().UnixMilli(),
	}

	// Save new task
	err := store.SaveTaskInfo(ctx, info, true)
	require.NoError(t, err)

	// Get task
	retrieved, err := store.GetTaskInfo(ctx, "task-1")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "task-1", retrieved.Task.ID)
	assert.Equal(t, model.TaskStatusPending, retrieved.Status)
}

func TestMemoryTaskStore_SaveTaskInfo_Duplicate(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-1",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}

	// First save
	err := store.SaveTaskInfo(ctx, info, true)
	require.NoError(t, err)

	// Duplicate save should fail
	err = store.SaveTaskInfo(ctx, info, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Update (isNew=false) should succeed
	info.Status = model.TaskStatusRunning
	err = store.SaveTaskInfo(ctx, info, false)
	require.NoError(t, err)
}

func TestMemoryTaskStore_UpdateTaskInfo(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-1",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}
	_ = store.SaveTaskInfo(ctx, info, true)

	// Update task
	err := store.UpdateTaskInfo(ctx, "task-1", func(i *TaskInfo) error {
		i.Status = model.TaskStatusRunning
		i.AgentID = "agent-1"
		return nil
	})
	require.NoError(t, err)

	// Verify update
	retrieved, _ := store.GetTaskInfo(ctx, "task-1")
	assert.Equal(t, model.TaskStatusRunning, retrieved.Status)
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
		Task: &model.Task{
			ID:       "task-1",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
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
			Task: &model.Task{
				ID:       "task-" + string(rune('a'+i)),
				TypeName: "test",
			},
			Status: model.TaskStatusPending,
		}
		_ = store.SaveTaskInfo(ctx, info, true)
	}

	// List all
	tasks, err := store.ListTaskInfos(ctx)
	require.NoError(t, err)
	assert.Len(t, tasks, 3)
}

func TestMemoryTaskStore_ListTaskInfosPage_FilterAndPaginate(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	tasks := []*TaskInfo{
		{
			Task: &model.Task{ID: "task-a", TypeName: "collect"},
			Status:          model.TaskStatusPending,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 100,
		},
		{
			Task: &model.Task{ID: "task-b", TypeName: "collect"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 300,
		},
		{
			Task: &model.Task{ID: "task-c", TypeName: "deploy"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-2",
			ServiceName:     "svc-2",
			AgentID:         "agent-2",
			CreatedAtMillis: 200,
		},
	}

	for _, info := range tasks {
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	filteredPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Statuses: []model.TaskStatus{model.TaskStatusRunning},
		AppID:    "app-1",
		TaskType: "collect",
		Limit:    1,
	})
	require.NoError(t, err)
	require.Len(t, filteredPage.Items, 1)
	assert.Equal(t, "task-b", filteredPage.Items[0].Task.ID)
	assert.False(t, filteredPage.HasMore)
	assert.Empty(t, filteredPage.NextCursor)

	page, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "task-b", page.Items[0].Task.ID)
	assert.True(t, page.HasMore)
	assert.NotEmpty(t, page.NextCursor) // seek cursor 格式

	nextPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Limit:  1,
		Cursor: page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, nextPage.Items, 1)
	assert.Equal(t, "task-c", nextPage.Items[0].Task.ID)
	assert.True(t, nextPage.HasMore)
	assert.NotEmpty(t, nextPage.NextCursor)
	assert.Greater(t, nextPage.Items[0].CreatedAtMillis, int64(0))

	finalPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Limit:  1,
		Cursor: nextPage.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, finalPage.Items, 1)
	assert.Equal(t, "task-a", finalPage.Items[0].Task.ID)
	assert.False(t, finalPage.HasMore)
	assert.Empty(t, finalPage.NextCursor)
	assert.Equal(t, int64(100), finalPage.Items[0].CreatedAtMillis)
	assert.Equal(t, "app-1", finalPage.Items[0].AppID)
	assert.Equal(t, "svc-1", finalPage.Items[0].ServiceName)
	assert.Equal(t, "agent-1", finalPage.Items[0].AgentID)
	assert.Equal(t, "collect", finalPage.Items[0].Task.TypeName)
	assert.Equal(t, model.TaskStatusPending, finalPage.Items[0].Status)
	assert.NotSame(t, tasks[0], finalPage.Items[0])
	assert.NotSame(t, tasks[0].Task, finalPage.Items[0].Task)
	assert.Equal(t, tasks[0].Task.ID, finalPage.Items[0].Task.ID)

	page.Items[0].Status = model.TaskStatusFailed
	retrieved, err := store.GetTaskInfo(ctx, "task-b")
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusRunning, retrieved.Status)

}

func TestMemoryTaskStore_ListTaskInfosPage_InvalidCursor(t *testing.T) {
	store := newTestMemoryStore(t)
	_, err := store.ListTaskInfosPage(context.Background(), TaskListQuery{Cursor: "bad-cursor"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cursor")
}

// TestMemoryTaskStore_ListTaskInfosPage_SeekCursor 验证 seek cursor 翻页链路。
func TestMemoryTaskStore_ListTaskInfosPage_SeekCursor(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// 创建 5 个 task，created_at 分别为 100, 200, 300, 400, 500
	for i := 1; i <= 5; i++ {
		info := &TaskInfo{
			Task:            &model.Task{ID: fmt.Sprintf("task-%d", i), TypeName: "test"},
			Status:          model.TaskStatusPending,
			CreatedAtMillis: int64(i * 100),
		}
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	// 第一页：limit=2，无 cursor
	page1, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1.Items, 2)
	assert.Equal(t, "task-5", page1.Items[0].Task.ID) // 最新的在前
	assert.Equal(t, "task-4", page1.Items[1].Task.ID)
	assert.True(t, page1.HasMore)
	assert.NotEmpty(t, page1.NextCursor)

	// 第二页：使用 seek cursor
	page2, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2, Cursor: page1.NextCursor})
	require.NoError(t, err)
	require.Len(t, page2.Items, 2)
	assert.Equal(t, "task-3", page2.Items[0].Task.ID)
	assert.Equal(t, "task-2", page2.Items[1].Task.ID)
	assert.True(t, page2.HasMore)
	assert.NotEmpty(t, page2.NextCursor)

	// 第三页：最后一条
	page3, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2, Cursor: page2.NextCursor})
	require.NoError(t, err)
	require.Len(t, page3.Items, 1)
	assert.Equal(t, "task-1", page3.Items[0].Task.ID)
	assert.False(t, page3.HasMore)
	assert.Empty(t, page3.NextCursor)
}

// TestMemoryTaskStore_ListTaskInfosPage_SeekCursor_SameScore 验证同分值下 seek cursor 的去重。
func TestMemoryTaskStore_ListTaskInfosPage_SeekCursor_SameScore(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	// 创建 3 个 task，created_at 都是 100（同分值）
	for _, id := range []string{"task-c", "task-b", "task-a"} {
		info := &TaskInfo{
			Task:            &model.Task{ID: id, TypeName: "test"},
			Status:          model.TaskStatusPending,
			CreatedAtMillis: 100,
		}
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	// 第一页
	page1, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page1.Items, 1)
	assert.Equal(t, "task-c", page1.Items[0].Task.ID) // 同分值按 ID desc
	assert.True(t, page1.HasMore)

	// 第二页
	page2, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 1, Cursor: page1.NextCursor})
	require.NoError(t, err)
	require.Len(t, page2.Items, 1)
	assert.Equal(t, "task-b", page2.Items[0].Task.ID)
	assert.True(t, page2.HasMore)

	// 第三页
	page3, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 1, Cursor: page2.NextCursor})
	require.NoError(t, err)
	require.Len(t, page3.Items, 1)
	assert.Equal(t, "task-a", page3.Items[0].Task.ID)
	assert.False(t, page3.HasMore)
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

	result := &model.TaskResult{
		TaskID:            "task-1",
		Status:            model.TaskStatusSuccess,
		CompletedAtMillis: time.Now().UnixMilli(),
	}

	// Save result
	err := store.SaveResult(ctx, result)
	require.NoError(t, err)

	// Get result
	retrieved, found, err := store.GetResult(ctx, "task-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, model.TaskStatusSuccess, retrieved.Status)
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

func TestMemoryTaskStore_ListRunningTaskInfos(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	runningInfo := &TaskInfo{
		Task: &model.Task{
			ID:       "task-running",
			TypeName: "test",
		},
		Status: model.TaskStatusRunning,
	}
	pendingInfo := &TaskInfo{
		Task: &model.Task{
			ID:       "task-pending",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}

	require.NoError(t, store.SaveTaskInfo(ctx, runningInfo, true))
	require.NoError(t, store.SaveTaskInfo(ctx, pendingInfo, true))
	require.NoError(t, store.SetRunning(ctx, "task-running", "agent-1"))
	require.NoError(t, store.SetRunning(ctx, "task-pending", "agent-2"))
	require.NoError(t, store.SetRunning(ctx, "task-missing", "agent-3"))

	infos, err := store.ListRunningTaskInfos(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "task-running", infos[0].Task.ID)
	assert.Equal(t, "agent-1", infos[0].AgentID)

	agentID, err := store.GetRunning(ctx, "task-pending")
	require.NoError(t, err)
	assert.Empty(t, agentID)

	agentID, err = store.GetRunning(ctx, "task-missing")
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
				Task: &model.Task{
					ID:       taskID,
					TypeName: "test",
				},
				Status: model.TaskStatusPending,
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

func TestMemoryTaskStore_ApplyTaskResult_UsesSharedStateMachine(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-apply-result",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplyTaskResult(ctx, info.Task.ID, &model.TaskResult{
		TaskID:  info.Task.ID,
		Status:  model.TaskStatusRunning,
		AgentID: "agent-1",
	}, 123)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskUpdated, res.Code)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, model.TaskStatusRunning, retrieved.Status)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, int64(123), retrieved.StartedAtMillis)
}

func TestMemoryTaskStore_ApplyCancel_RejectsTerminal(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-cancel-terminal",
			TypeName: "test",
		},
		Status: model.TaskStatusSuccess,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplyCancel(ctx, info.Task.ID, 456)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskRejected, res.Code)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, model.TaskStatusSuccess, retrieved.Status)
}

func TestMemoryTaskStore_ApplySetRunning_Idempotent(t *testing.T) {
	store := newTestMemoryStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-running-idempotent",
			TypeName: "test",
		},
		Status:          model.TaskStatusRunning,
		AgentID:         "agent-1",
		StartedAtMillis: 100,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplySetRunning(ctx, info.Task.ID, "agent-2", 789)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskNoop, res.Code)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, int64(100), retrieved.StartedAtMillis)
}