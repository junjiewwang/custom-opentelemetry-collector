// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newTestRedisStore creates a RedisStore backed by miniredis for contract testing.
func newTestRedisStore(t *testing.T) *RedisStore {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisStore(client, zap.NewNop(), RedisStoreConfig{KeyPrefix: "te"})
}

// makeTask is a helper that creates a task with a unique ID based on the test name + index.
func makeTask(id string, status TaskStatus, ts time.Time) *Task {
	return &Task{
		ID:        id,
		Type:      TaskTypePurgeIndex,
		Status:    status,
		CreatedAt: ts.UnixMilli(),
		Routing:   TaskRouting{Strategy: RoutingBroadcast},
	}
}

// saveTaskViaClient directly inserts into Redis (bypassing Go Store API) for test setup.
func saveTaskViaClient(t *testing.T, client redis.UniversalClient, task *Task) {
	t.Helper()
	data, err := json.Marshal(task)
	require.NoError(t, err)
	key := fmt.Sprintf("te:task:%s", task.ID)
	err = client.Set(context.Background(), key, data, 0).Err()
	require.NoError(t, err)
}

func TestRedisStore_FastPath_ReturnsRunningTasks(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Save tasks in various states
	saveTaskViaClient(t, store.client, makeTask("fp-1", StatusRunning, now.Add(-1*time.Hour)))
	saveTaskViaClient(t, store.client, makeTask("fp-2", StatusRunning, now.Add(-30*time.Minute)))
	saveTaskViaClient(t, store.client, makeTask("fp-3", StatusRunning, now))
	saveTaskViaClient(t, store.client, makeTask("fp-4", StatusSuccess, now))
	saveTaskViaClient(t, store.client, makeTask("fp-5", StatusFailed, now))

	// Pre-populate ZSET (simulating tasks already transitioned via Lua)
	for _, id := range []string{"fp-1", "fp-2", "fp-3"} {
		err := store.client.ZAdd(ctx, store.runningKey(), redis.Z{
			Score:  float64(now.UnixMilli()),
			Member: fmt.Sprintf("te:task:%s", id),
		}).Err()
		require.NoError(t, err)
	}

	// Fast path should return only running tasks
	page, err := store.ListTasks(ctx, ListQuery{
		Status: StatusRunning,
		Limit:  100,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, page.Total)
	for _, task := range page.Tasks {
		assert.Equal(t, StatusRunning, task.Status)
	}
}

func TestRedisStore_SlowPath_NonRunning(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	saveTaskViaClient(t, store.client, makeTask("sp-1", StatusSuccess, now))
	saveTaskViaClient(t, store.client, makeTask("sp-2", StatusFailed, now))
	saveTaskViaClient(t, store.client, makeTask("sp-3", StatusSuccess, now))

	// Status=Success triggers slow path (NOT running → no fast path)
	page, err := store.ListTasks(ctx, ListQuery{
		Status: StatusSuccess,
		Limit:  100,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, page.Total, "slow path should find SUCCESS tasks")
}

func TestRedisStore_BothPaths_Consistent(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Same data, query without status filter → slow path should work
	for i := 0; i < 5; i++ {
		saveTaskViaClient(t, store.client, makeTask(fmt.Sprintf("both-%d", i), StatusRunning, now))
	}
	// Populate ZSET for fast path
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("both-%d", i)
		require.NoError(t, store.client.ZAdd(ctx, store.runningKey(), redis.Z{
			Score: float64(now.UnixMilli()), Member: fmt.Sprintf("te:task:%s", id),
		}).Err())
	}

	// Fast path
	fast, err := store.ListTasks(ctx, ListQuery{Status: StatusRunning, Limit: 100})
	require.NoError(t, err)
	// Slow path (no status filter → goes through SCAN)
	slow, err := store.ListTasks(ctx, ListQuery{Limit: 100})
	require.NoError(t, err)

	// All 5 running tasks should be in slow path results (among others)
	assert.GreaterOrEqual(t, slow.Total, fast.Total)
}

func TestRedisStore_FastPath_FallsBackOnEmptyZSet(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Save running tasks WITHOUT populating ZSET (cold-start scenario)
	for i := 1; i <= 3; i++ {
		saveTaskViaClient(t, store.client,
			makeTask(fmt.Sprintf("cold-%d", i), StatusRunning, now))
	}

	// Fast path should fall back to SCAN and find all 3 tasks
	page, err := store.ListTasks(ctx, ListQuery{
		Status: StatusRunning,
		Limit:  100,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, page.Total, "fallback SCAN should find all running tasks when ZSET empty")
}

func TestRedisStore_ZSETIndex_Maintenance(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	// Create task via Store API (ensures proper initialization)
	task := makeTask("idx-task", StatusPending, time.Now())
	err := store.SaveTask(ctx, task)
	require.NoError(t, err)

	// Verify ZSET is empty before RUNNING
	count, err := store.client.ZCard(ctx, store.runningKey()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Transition PENDING → RUNNING (via Lua with ZADD)
	err = store.UpdateTaskStatus(ctx, "idx-task", StatusRunning, "node-1")
	require.NoError(t, err)

	// ZSET should now have 1 entry
	count, err = store.client.ZCard(ctx, store.runningKey()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Transition RUNNING → SUCCESS (via Lua with ZREM)
	err = store.UpdateTaskStatus(ctx, "idx-task", StatusSuccess, "")
	require.NoError(t, err)

	// ZSET should be empty again
	count, err = store.client.ZCard(ctx, store.runningKey()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestRedisStore_ZSETIndex_NegativeTransitions(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	// Create + transition to RUNNING
	task := makeTask("neg-task", StatusPending, time.Now())
	require.NoError(t, store.SaveTask(ctx, task))
	require.NoError(t, store.UpdateTaskStatus(ctx, "neg-task", StatusRunning, "node-1"))

	// Direct transition to TIMEOUT (reaper) — should ZREM
	require.NoError(t, store.UpdateTaskStatus(ctx, "neg-task", StatusTimeout, ""))

	count, err := store.client.ZCard(ctx, store.runningKey()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestRedisStore_TerminalTTL(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	task := makeTask("ttl-task", StatusPending, time.Now())
	require.NoError(t, store.SaveTask(ctx, task))
	require.NoError(t, store.UpdateTaskStatus(ctx, "ttl-task", StatusRunning, "node-1"))
	require.NoError(t, store.UpdateTaskStatus(ctx, "ttl-task", StatusSuccess, ""))

	// Terminal tasks should get EXPIRE set (14d = 1209600s)
	ttl, err := store.client.TTL(ctx, store.taskKey("ttl-task")).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl.Seconds(), float64(0), "terminal task should have TTL set")
	assert.LessOrEqual(t, ttl, 14*24*time.Hour+time.Second)
}

func TestRedisStore_ListTasks_NonRunning(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Save tasks in terminal states
	saveTaskViaClient(t, store.client, makeTask("done-1", StatusSuccess, now))
	saveTaskViaClient(t, store.client, makeTask("done-2", StatusFailed, now))
	saveTaskViaClient(t, store.client, makeTask("done-3", StatusSuccess, now))

	// Query for terminal status — should use slow path (not fast path)
	page, err := store.ListTasks(ctx, ListQuery{
		Status: StatusSuccess,
		Limit:  100,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, page.Total, "should find 2 SUCCESS tasks via slow path")
}

func TestRedisStore_GetTasksChunked(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Save more than taskMGetChunkSize tasks
	n := taskMGetChunkSize + 10
	taskIDs := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		taskIDs[i] = id
		saveTaskViaClient(t, store.client, makeTask(id, StatusSuccess, now))
	}

	tasks, err := store.getTasksChunked(ctx, taskIDs)
	require.NoError(t, err)
	assert.Equal(t, n, len(tasks), "chunked fetch should return all tasks")
}

func TestRedisStore_ZSET_Scores(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	baseTime := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("score-%d", i)
		task := makeTask(id, StatusPending, baseTime.Add(time.Duration(i)*time.Hour))
		require.NoError(t, store.SaveTask(ctx, task))
		require.NoError(t, store.UpdateTaskStatus(ctx, id, StatusRunning, "node-1"))
	}

	// ZSET should be ordered by createdAt
	results, err := store.client.ZRangeWithScores(ctx, store.runningKey(), 0, -1).Result()
	require.NoError(t, err)
	assert.Equal(t, 3, len(results))
	// Scores should be monotonically increasing (oldest first)
	for i := 1; i < len(results); i++ {
		assert.True(t, results[i].Score >= results[i-1].Score,
			"scores should be non-decreasing: %f >= %f", results[i].Score, results[i-1].Score)
	}
}

// ─── GetProgress Tests (Plan C: SMEMBERS + Pipeline HGET) ───

func TestRedisStore_GetProgress_BasicCounts(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()
	groupID := "epoch-test-1"

	// Create tasks in a group with mixed statuses
	tasks := []struct {
		id     string
		status TaskStatus
	}{
		{"prog-1", StatusPending},
		{"prog-2", StatusPending},
		{"prog-3", StatusRunning},
		{"prog-4", StatusSuccess},
		{"prog-5", StatusFailed},
		{"prog-6", StatusTimeout},
		{"prog-7", StatusCancelled},
		{"prog-8", StatusSkipped}, // counts as Completed
	}

	for _, tc := range tasks {
		task := &Task{
			ID:        tc.id,
			Type:      TaskTypePurgeIndex,
			Status:    StatusPending,
			CreatedAt: now.UnixMilli(),
			GroupID:   groupID,
			Timeout:   2 * time.Minute,
			Routing:   TaskRouting{Strategy: RoutingBroadcast},
		}
		require.NoError(t, store.SaveTask(ctx, task))

		// Transition to target status
		if tc.status == StatusRunning {
			require.NoError(t, store.UpdateTaskStatus(ctx, tc.id, StatusRunning, "node-1"))
		} else if tc.status != StatusPending {
			require.NoError(t, store.UpdateTaskStatus(ctx, tc.id, StatusRunning, "node-1"))
			require.NoError(t, store.UpdateTaskStatus(ctx, tc.id, tc.status, ""))
		}
	}

	// Query progress
	progress, err := store.GetProgress(ctx, "", groupID)
	require.NoError(t, err)

	assert.Equal(t, 8, progress.Total)
	assert.Equal(t, 2, progress.Pending)
	assert.Equal(t, 1, progress.Running)
	assert.Equal(t, 2, progress.Completed) // success + skipped
	assert.Equal(t, 1, progress.Failed)
	assert.Equal(t, 1, progress.Timeout)
	assert.Equal(t, 1, progress.Cancelled)
}

func TestRedisStore_GetProgress_WithTypeFilter(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()
	groupID := "epoch-type-filter"

	// Create tasks with different types in the same group
	purgeTask := &Task{
		ID:        "type-purge-1",
		Type:      TaskTypePurgeIndex,
		Status:    StatusPending,
		CreatedAt: now.UnixMilli(),
		GroupID:   groupID,
		Timeout:   2 * time.Minute,
		Routing:   TaskRouting{Strategy: RoutingBroadcast},
	}
	otherTask := &Task{
		ID:        "type-other-1",
		Type:      TaskType("other_type"),
		Status:    StatusPending,
		CreatedAt: now.UnixMilli(),
		GroupID:   groupID,
		Timeout:   2 * time.Minute,
		Routing:   TaskRouting{Strategy: RoutingBroadcast},
	}

	require.NoError(t, store.SaveTask(ctx, purgeTask))
	require.NoError(t, store.SaveTask(ctx, otherTask))

	// Filter by TaskTypePurgeIndex — should only count 1
	progress, err := store.GetProgress(ctx, TaskTypePurgeIndex, groupID)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.Total)
	assert.Equal(t, 1, progress.Pending)

	// No filter — should count 2
	progressAll, err := store.GetProgress(ctx, "", groupID)
	require.NoError(t, err)
	assert.Equal(t, 2, progressAll.Total)
}

func TestRedisStore_GetProgress_EmptyGroup(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	// Query non-existent group
	progress, err := store.GetProgress(ctx, "", "non-existent-group")
	require.NoError(t, err)
	assert.Equal(t, 0, progress.Total)
	assert.Equal(t, 0, progress.Pending)
	assert.Equal(t, 0, progress.Running)
}

func TestRedisStore_GetProgress_FallbackLegacy_NoGroupID(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()
	now := time.Now()

	// Save tasks without group (legacy path)
	task := &Task{
		ID:        "legacy-prog-1",
		Type:      TaskTypePurgeIndex,
		Status:    StatusPending,
		CreatedAt: now.UnixMilli(),
		Timeout:   2 * time.Minute,
		Routing:   TaskRouting{Strategy: RoutingBroadcast},
	}
	require.NoError(t, store.SaveTask(ctx, task))

	// GetProgress without groupID should fall back to legacy ListTasks path
	progress, err := store.GetProgress(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, 1, progress.Total)
	assert.Equal(t, 1, progress.Pending)
}
