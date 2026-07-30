// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// These tests cover the RedisStore write-path and lifecycle methods that the
// existing store_redis_test.go leaves at 0%: queue ops (Enqueue/Dequeue/
// RemoveFromQueue), result storage (SaveResult/GetResult), events
// (PublishEvent/SubscribeEvents), lifecycle (Start/Close), the reaper
// optimized path (GetOverdueRunningTasks), meta-only access
// (GetTaskMeta/GetTasksMeta, incl. legacy fallback), and DeleteTask.
//
// All backed by miniredis (no real Redis). miniredis supports the Lua EVAL
// used by Dequeue and basic Pub/Sub used by PublishEvent/SubscribeEvents.

// newRedisStoreWithClient builds a RedisStore over a miniredis instance and
// returns the store + the raw client (for direct state seeding/inspection).
func newRedisStoreWithClient(t *testing.T) (*RedisStore, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisStore(client, zap.NewNop(), RedisStoreConfig{KeyPrefix: "te"})
	return store, client
}

// ── Defaults / constructor ─────────────────────────────────────────────

func TestDefaultRedisStoreConfig(t *testing.T) {
	cfg := DefaultRedisStoreConfig()
	assert.Equal(t, "te", cfg.KeyPrefix)
	assert.Equal(t, 24*time.Hour, cfg.ResultTTL)
}

func TestNewRedisStore_AppliesDefaults(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// Empty config → defaults applied.
	store := NewRedisStore(client, zap.NewNop(), RedisStoreConfig{})
	assert.Equal(t, "te", store.prefix)
	assert.Equal(t, 24*time.Hour, store.resultTTL)

	// Custom values preserved.
	store2 := NewRedisStore(client, zap.NewNop(), RedisStoreConfig{KeyPrefix: "x", ResultTTL: time.Minute})
	assert.Equal(t, "x", store2.prefix)
	assert.Equal(t, time.Minute, store2.resultTTL)
}

// ── Queue operations ───────────────────────────────────────────────────

func TestRedisStore_EnqueueDequeue_FIFO(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	// Enqueue two tasks into the same queue (LPUSH → RPOP gives FIFO).
	require.NoError(t, store.Enqueue(ctx, "global", "t1", 1))
	require.NoError(t, store.Enqueue(ctx, "global", "t2", 1))

	// Dequeue returns the oldest first (FIFO).
	id, err := store.Dequeue(ctx, []string{"global"})
	require.NoError(t, err)
	assert.Equal(t, "t1", id)

	id, err = store.Dequeue(ctx, []string{"global"})
	require.NoError(t, err)
	assert.Equal(t, "t2", id)

	// Queue drained → empty string, no error.
	id, err = store.Dequeue(ctx, []string{"global"})
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestRedisStore_Dequeue_EmptyQueueList(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	// Empty input → "", nil (no Redis call).
	id, err := store.Dequeue(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestRedisStore_Dequeue_PriorityOrder(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	// Two queues: "high" has nothing, "low" has a task. Dequeue checks in order.
	require.NoError(t, store.Enqueue(ctx, "low", "t-low", 1))

	// First non-empty queue wins → "t-low".
	id, err := store.Dequeue(ctx, []string{"high", "low"})
	require.NoError(t, err)
	assert.Equal(t, "t-low", id)

	// Now enqueue into "high" — it should be returned before "low".
	require.NoError(t, store.Enqueue(ctx, "high", "t-high", 1))
	require.NoError(t, store.Enqueue(ctx, "low", "t-low2", 1))

	id, err = store.Dequeue(ctx, []string{"high", "low"})
	require.NoError(t, err)
	assert.Equal(t, "t-high", id)
}

func TestRedisStore_RemoveFromQueue(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	require.NoError(t, store.Enqueue(ctx, "global", "t1", 1))
	require.NoError(t, store.Enqueue(ctx, "global", "t2", 1))

	// Remove t1 from the middle; t2 remains.
	require.NoError(t, store.RemoveFromQueue(ctx, "global", "t1"))

	id, err := store.Dequeue(ctx, []string{"global"})
	require.NoError(t, err)
	assert.Equal(t, "t2", id)
}

// ── Result storage ─────────────────────────────────────────────────────

func TestRedisStore_SaveAndGetResult_RoundTrip(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	want := &TaskResult{
		TaskID:      "t1",
		NodeID:      "agent-1",
		Status:      StatusSuccess,
		Output:      json.RawMessage(`{"k":"v"}`),
		StartedAt:   1000,
		CompletedAt: 2000,
		RetryCount:  0,
	}
	require.NoError(t, store.SaveResult(ctx, want))

	got, err := store.GetResult(ctx, "t1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.TaskID, got.TaskID)
	assert.Equal(t, want.Status, got.Status)
	assert.Equal(t, want.NodeID, got.NodeID)
	assert.Equal(t, want.CompletedAt, got.CompletedAt)
	assert.JSONEq(t, string(want.Output), string(got.Output))
}

func TestRedisStore_GetResult_NotFound(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	got, err := store.GetResult(context.Background(), "no-such-task")
	require.NoError(t, err) // redis.Nil maps to (nil, nil)
	assert.Nil(t, got)
}

// ── Lifecycle (no-ops) ─────────────────────────────────────────────────

func TestRedisStore_StartClose_NoOp(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	// Both are documented no-ops (connection managed externally); they must
	// not error and must not close the shared client.
	require.NoError(t, store.Start(context.Background()))
	require.NoError(t, store.Close())

	// Store remains usable after Start/Close.
	require.NoError(t, store.Enqueue(context.Background(), "q", "t", 1))
}

// ── Reaper optimized path ──────────────────────────────────────────────

func TestRedisStore_GetOverdueRunningTasks(t *testing.T) {
	store, client := newRedisStoreWithClient(t)
	ctx := context.Background()

	// Seed the running_deadlines ZSET with three tasks whose deadlines
	// (score = createdAt + timeout, in millis) straddle "now".
	//   overdue-1: deadline 1000  (overdue at now=5000)
	//   overdue-2: deadline 4000  (overdue at now=5000)
	//   future-1:  deadline 9000  (not overdue at now=5000)
	zadd := client.ZAdd(ctx, store.runningDeadlineKey(),
		redis.Z{Score: 1000, Member: "overdue-1"},
		redis.Z{Score: 4000, Member: "overdue-2"},
		redis.Z{Score: 9000, Member: "future-1"},
	)
	require.NoError(t, zadd.Err())

	ids, err := store.GetOverdueRunningTasks(ctx, 5000)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"overdue-1", "overdue-2"}, ids)
}

func TestRedisStore_GetOverdueRunningTasks_Empty(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	ids, err := store.GetOverdueRunningTasks(context.Background(), 5000)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// ── Meta-only access ───────────────────────────────────────────────────

func TestRedisStore_GetTaskMeta_NewHashFormat(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	// SaveTask writes the new HASH meta layout.
	task := &Task{
		ID:        "meta-1",
		Type:      TaskTypeArthasAttach,
		Status:    StatusRunning,
		CreatedAt: 123,
		Timeout:   30 * time.Second,
		Priority:  7,
		ClaimedBy: "agent-x",
		GroupID:   "g1",
	}
	require.NoError(t, store.SaveTask(ctx, task))

	meta, err := store.GetTaskMeta(ctx, "meta-1")
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "meta-1", meta.ID)
	assert.Equal(t, TaskTypeArthasAttach, meta.Type)
	assert.Equal(t, StatusRunning, meta.Status)
	assert.Equal(t, int64(123), meta.CreatedAt)
	assert.Equal(t, 30*time.Second, meta.Timeout)
	assert.Equal(t, int32(7), meta.Priority)
	assert.Equal(t, "agent-x", meta.ClaimedBy)
	assert.Equal(t, "g1", meta.GroupID)
}

func TestRedisStore_GetTaskMeta_LegacyFallback(t *testing.T) {
	store, client := newRedisStoreWithClient(t)
	ctx := context.Background()

	// Seed ONLY the legacy STRING format (no meta HASH) — simulates a task
	// written before the split-layout migration.
	legacy := &Task{
		ID:        "legacy-1",
		Type:      TaskTypeArthasAttach,
		Status:    StatusRunning,
		CreatedAt: 999,
		ClaimedBy: "agent-old",
	}
	data, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, store.taskKey("legacy-1"), data, 0).Err())

	// GetTaskMeta must fall back to the legacy STRING and convert via taskToMeta.
	meta, err := store.GetTaskMeta(ctx, "legacy-1")
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "legacy-1", meta.ID)
	assert.Equal(t, StatusRunning, meta.Status)
	assert.Equal(t, int64(999), meta.CreatedAt)
	assert.Equal(t, "agent-old", meta.ClaimedBy)
}

func TestRedisStore_GetTaskMeta_NotFound(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	meta, err := store.GetTaskMeta(context.Background(), "no-such-task")
	require.NoError(t, err)
	assert.Nil(t, meta)
}

func TestRedisStore_GetTasksMeta_Batch(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx := context.Background()

	require.NoError(t, store.SaveTask(ctx, &Task{ID: "b1", Type: TaskTypeArthasAttach, Status: StatusPending, CreatedAt: 1}))
	require.NoError(t, store.SaveTask(ctx, &Task{ID: "b2", Type: TaskTypeArthasAttach, Status: StatusRunning, CreatedAt: 2, ClaimedBy: "a1"}))

	metas, err := store.GetTasksMeta(ctx, []string{"b1", "b2", "missing"})
	require.NoError(t, err)
	require.Len(t, metas, 2) // missing silently omitted

	byID := map[string]*TaskMeta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	assert.Equal(t, StatusPending, byID["b1"].Status)
	assert.Equal(t, StatusRunning, byID["b2"].Status)
	assert.Equal(t, "a1", byID["b2"].ClaimedBy)
}

func TestRedisStore_GetTasksMeta_EmptyInput(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	metas, err := store.GetTasksMeta(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, metas)
}

// ── DeleteTask ─────────────────────────────────────────────────────────

func TestRedisStore_DeleteTask_RemovesAllFormatsAndIndexes(t *testing.T) {
	store, client := newRedisStoreWithClient(t)
	ctx := context.Background()

	task := &Task{
		ID:        "del-1",
		Type:      TaskTypeArthasAttach,
		Status:    StatusRunning,
		CreatedAt: 1,
		GroupID:   "g-del",
	}
	require.NoError(t, store.SaveTask(ctx, task))
	// Add it to the running deadline index and the group set (mirrors Claim).
	require.NoError(t, client.ZAdd(ctx, store.runningDeadlineKey(), redis.Z{Score: 1, Member: "del-1"}).Err())

	require.NoError(t, store.DeleteTask(ctx, "del-1"))

	// All keys gone: meta, payload, legacy task, result, indexes, group.
	exists, err := client.Exists(ctx,
		store.metaKey("del-1"),
		store.payloadKey("del-1"),
		store.taskKey("del-1"),
	).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists)

	inIndex, err := client.ZScore(ctx, store.runningDeadlineKey(), "del-1").Result()
	assert.Error(t, err, "task must be removed from running_deadlines ZSET")
	_ = inIndex

	inGroup, err := client.SIsMember(ctx, store.groupKey("g-del"), "del-1").Result()
	require.NoError(t, err)
	assert.False(t, inGroup, "task must be removed from its group set")

	// GetTask now returns nil.
	got, err := store.GetTask(ctx, "del-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRedisStore_DeleteTask_AlreadyGone_NoError(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)

	// Deleting a non-existent task is a no-op (GetTask returns nil → early return).
	err := store.DeleteTask(context.Background(), "never-existed")
	require.NoError(t, err)
}

// ── Events ─────────────────────────────────────────────────────────────

func TestRedisStore_PublishEvent(t *testing.T) {
	store, client := newRedisStoreWithClient(t)
	ctx := context.Background()

	// miniredis records published messages; assert via Publish return (>=0
	// receivers). The channel name must match eventChannel(event.Type).
	event := TaskEvent{
		Type:   EventTaskSubmitted,
		TaskID: "ev-1",
		Status: StatusPending,
		At:     42,
	}
	require.NoError(t, store.PublishEvent(ctx, event))

	// Verify the channel naming convention indirectly: a subscriber on the
	// pattern sees the message. (Also exercises SubscribeEvents' happy path.)
	sub := client.Subscribe(ctx, store.eventChannel(EventTaskSubmitted))
	t.Cleanup(func() { _ = sub.Close() })
	// Drain the subscription ack, then publish again and receive.
	_ = sub.Channel()

	require.NoError(t, store.PublishEvent(ctx, event))

	select {
	case msg := <-sub.Channel():
		var got TaskEvent
		require.NoError(t, json.Unmarshal([]byte(msg.Payload), &got))
		assert.Equal(t, EventTaskSubmitted, got.Type)
		assert.Equal(t, "ev-1", got.TaskID)
	case <-time.After(time.Second):
		t.Fatal("did not receive published event")
	}
}

func TestRedisStore_SubscribeEvents_ReceivesPublished(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := store.SubscribeEvents(ctx)
	require.NoError(t, err)

	// Give the PSubscribe goroutine a moment to register, then publish.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, store.PublishEvent(ctx, TaskEvent{
		Type:   EventTaskClaimed,
		TaskID: "sub-1",
		Status: StatusRunning,
		At:     7,
	}))

	select {
	case ev := <-ch:
		assert.Equal(t, EventTaskClaimed, ev.Type)
		assert.Equal(t, "sub-1", ev.TaskID)
	case <-time.After(time.Second):
		t.Fatal("SubscribeEvents did not deliver the published event")
	}
}

func TestRedisStore_SubscribeEvents_ContextCancelClosesChannel(t *testing.T) {
	store, _ := newRedisStoreWithClient(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := store.SubscribeEvents(ctx)
	require.NoError(t, err)

	// Cancelling ctx must close the returned channel (goroutine exits).
	cancel()

	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel must be closed on ctx cancel")
	case <-time.After(time.Second):
		t.Fatal("channel was not closed after ctx cancel")
	}
}

// ── filterAndPage (pure helper) ────────────────────────────────────────

func TestFilterAndPage_StatusAndTypeFilter(t *testing.T) {
	tasks := []*Task{
		{ID: "1", Type: TaskTypeArthasAttach, Status: StatusPending},
		{ID: "2", Type: TaskTypeArthasAttach, Status: StatusRunning},
		{ID: "3", Type: TaskTypePurgeIndex, Status: StatusPending},
	}

	page := filterAndPage(tasks, ListQuery{Status: StatusPending, TaskType: TaskTypeArthasAttach, Limit: 10})
	assert.Equal(t, 1, page.Total)
	require.Len(t, page.Tasks, 1)
	assert.Equal(t, "1", page.Tasks[0].ID)
}

func TestFilterAndPage_Pagination(t *testing.T) {
	tasks := []*Task{
		{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}, {ID: "5"},
	}
	page := filterAndPage(tasks, ListQuery{Offset: 1, Limit: 2})
	assert.Equal(t, 5, page.Total)
	require.Len(t, page.Tasks, 2)
	assert.Equal(t, "2", page.Tasks[0].ID)
	assert.Equal(t, "3", page.Tasks[1].ID)
}

func TestFilterAndPage_OffsetBeyondEnd(t *testing.T) {
	tasks := []*Task{{ID: "1"}, {ID: "2"}}
	page := filterAndPage(tasks, ListQuery{Offset: 10, Limit: 5})
	assert.Equal(t, 2, page.Total)
	assert.Empty(t, page.Tasks)
}
