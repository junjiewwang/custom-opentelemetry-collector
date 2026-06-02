// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Redis Integration Tests (using miniredis)
// ═══════════════════════════════════════════════════

func newTestRedisCoordinator(t *testing.T, nodeID string) (*RedisCoordinator, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisCoordinator(client, nodeID, zaptest.NewLogger(t)), mr
}

func TestRedisCoordinator_TryBecomeLeader(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()

	// First attempt should succeed
	ok, err := coord.TryBecomeLeader(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected to become leader")
	}

	// Second attempt by same node (within TTL) should fail
	coord2, _ := newTestRedisCoordinator(t, "node-2")
	// Use the same Redis by pointing coord2 to coord's client
	coord2.client = coord.client
	ok2, err := coord2.TryBecomeLeader(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok2 {
		t.Fatal("expected second node to NOT become leader")
	}
}

func TestRedisCoordinator_ReleaseLeader(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()

	// Become leader
	ok, _ := coord.TryBecomeLeader(ctx)
	if !ok {
		t.Fatal("expected to become leader")
	}

	// Release
	if err := coord.ReleaseLeader(ctx); err != nil {
		t.Fatalf("ReleaseLeader failed: %v", err)
	}

	// Now another node can become leader
	ok2, err := coord.TryBecomeLeader(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok2 {
		t.Fatal("expected to re-acquire leader after release")
	}
}

func TestRedisCoordinator_ReleaseLeader_OnlyOwner(t *testing.T) {
	coord1, _ := newTestRedisCoordinator(t, "node-1")
	coord2 := &RedisCoordinator{
		client:    coord1.client,
		nodeID:    "node-2",
		logger:    zaptest.NewLogger(t).Named("coord2"),
		leaderTTL: 30 * time.Second,
	}
	ctx := context.Background()

	// node-1 becomes leader
	ok, _ := coord1.TryBecomeLeader(ctx)
	if !ok {
		t.Fatal("expected node-1 to become leader")
	}

	// node-2 tries to release (should fail silently — CAS)
	if err := coord2.ReleaseLeader(ctx); err != nil {
		t.Fatalf("ReleaseLeader should not error: %v", err)
	}

	// node-1 should still be leader — verify by trying to acquire again
	ok2, _ := coord2.TryBecomeLeader(ctx)
	if ok2 {
		t.Fatal("node-2 should NOT have acquired leader after failed release")
	}
}

func TestRedisCoordinator_SubmitAndClaimTasks(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{
		{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"},
		{ID: "t2", Epoch: epoch, Signal: SignalTrace, IndexName: "idx2"},
		{ID: "t3", Epoch: epoch, Signal: SignalLog, IndexName: "idx3"},
	}

	if err := coord.SubmitTasks(ctx, epoch, tasks); err != nil {
		t.Fatalf("SubmitTasks failed: %v", err)
	}

	// Verify active epoch is set
	active, err := coord.GetActiveEpoch(ctx)
	if err != nil {
		t.Fatalf("GetActiveEpoch failed: %v", err)
	}
	if active != epoch {
		t.Fatalf("expected active epoch=%d, got %d", epoch, active)
	}

	// Claim all tasks
	var claimed []PurgeTask
	for {
		task, err := coord.ClaimTask(ctx, epoch)
		if err != nil {
			t.Fatalf("ClaimTask failed: %v", err)
		}
		if task == nil {
			break
		}
		claimed = append(claimed, *task)
	}

	if len(claimed) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(claimed))
	}
}

func TestRedisCoordinator_ReportResultAndProgress(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{
		{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"},
		{ID: "t2", Epoch: epoch, Signal: SignalTrace, IndexName: "idx2"},
		{ID: "t3", Epoch: epoch, Signal: SignalTrace, IndexName: "idx3"},
	}
	_ = coord.SubmitTasks(ctx, epoch, tasks)

	// Claim and report results
	for i := 0; i < 3; i++ {
		task, _ := coord.ClaimTask(ctx, epoch)
		status := TaskStatusSuccess
		errMsg := ""
		if i == 2 {
			status = TaskStatusFailed
			errMsg = "ES timeout"
		}
		_ = coord.ReportResult(ctx, epoch, task.ID, TaskResult{
			Status:    status,
			NodeID:    "node-1",
			Error:     errMsg,
			StartedAt: time.Now(),
			DoneAt:    time.Now(),
		})
	}

	// Check progress
	progress, err := coord.GetProgress(ctx, epoch)
	if err != nil {
		t.Fatalf("GetProgress failed: %v", err)
	}
	if progress.TotalTasks != 3 {
		t.Errorf("expected total=3, got %d", progress.TotalTasks)
	}
	if progress.Completed != 2 {
		t.Errorf("expected completed=2, got %d", progress.Completed)
	}
	if progress.Failed != 1 {
		t.Errorf("expected failed=1, got %d", progress.Failed)
	}
	if progress.Remaining != 0 {
		t.Errorf("expected remaining=0, got %d", progress.Remaining)
	}
	if progress.Status != "done" {
		t.Errorf("expected status=done, got %s", progress.Status)
	}
}

func TestRedisCoordinator_CompleteEpoch(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"}}
	_ = coord.SubmitTasks(ctx, epoch, tasks)

	// Complete epoch
	if err := coord.CompleteEpoch(ctx, epoch); err != nil {
		t.Fatalf("CompleteEpoch failed: %v", err)
	}

	// Active epoch should be cleared
	active, _ := coord.GetActiveEpoch(ctx)
	if active != 0 {
		t.Fatalf("expected 0 after CompleteEpoch, got %d", active)
	}
}

func TestRedisCoordinator_GetFailedTasks(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{
		{ID: fmt.Sprintf("%d:trace:idx1", epoch), Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"},
		{ID: fmt.Sprintf("%d:trace:idx2", epoch), Epoch: epoch, Signal: SignalTrace, IndexName: "idx2"},
		{ID: fmt.Sprintf("%d:log:idx3", epoch), Epoch: epoch, Signal: SignalLog, IndexName: "idx3"},
	}
	_ = coord.SubmitTasks(ctx, epoch, tasks)

	// Claim all and report: 2 success, 1 failed
	for i := 0; i < 3; i++ {
		task, _ := coord.ClaimTask(ctx, epoch)
		status := TaskStatusSuccess
		errMsg := ""
		if task.ID == fmt.Sprintf("%d:trace:idx2", epoch) {
			status = TaskStatusFailed
			errMsg = "connection reset"
		}
		_ = coord.ReportResult(ctx, epoch, task.ID, TaskResult{
			Status: status, NodeID: "node-1", Error: errMsg,
			StartedAt: time.Now(), DoneAt: time.Now(),
		})
	}

	// Get failed tasks
	failed, err := coord.GetFailedTasks(ctx, epoch, 3)
	if err != nil {
		t.Fatalf("GetFailedTasks failed: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed task, got %d", len(failed))
	}
	if failed[0].IndexName != "idx2" {
		t.Errorf("expected failed task index=idx2, got %s", failed[0].IndexName)
	}
}

func TestRedisCoordinator_EmptySubmit(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()

	// Empty submit should not error
	err := coord.SubmitTasks(ctx, 12345, nil)
	if err != nil {
		t.Fatalf("SubmitTasks with empty list should not error: %v", err)
	}
}

func TestRedisCoordinator_ClaimFromEmptyPool(t *testing.T) {
	coord, _ := newTestRedisCoordinator(t, "node-1")
	ctx := context.Background()

	task, err := coord.ClaimTask(ctx, 99999)
	if err != nil {
		t.Fatalf("ClaimTask on non-existent key should not error: %v", err)
	}
	if task != nil {
		t.Fatal("expected nil task from empty pool")
	}
}

// ═══════════════════════════════════════════════════
// Multi-Node Simulation Tests
// ═══════════════════════════════════════════════════

func TestMultiNode_ConcurrentClaimNoDuplication(t *testing.T) {
	// Simulates 5 nodes competing to claim 100 tasks from the same Redis queue.
	// Verifies no task is claimed by more than one node (atomicity guarantee).
	coord, _ := newTestRedisCoordinator(t, "leader")
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	// Submit 100 tasks
	tasks := make([]PurgeTask, 100)
	for i := range tasks {
		tasks[i] = PurgeTask{
			ID:        fmt.Sprintf("%d:trace:idx-%04d", epoch, i),
			Epoch:     epoch,
			Signal:    SignalTrace,
			IndexName: fmt.Sprintf("otel-traces-idx-%04d", i),
		}
	}
	_ = coord.SubmitTasks(ctx, epoch, tasks)

	// 5 "nodes" (goroutines) competing to claim tasks
	const numNodes = 5
	var wg sync.WaitGroup
	nodeClaimed := make([][]string, numNodes)

	for n := 0; n < numNodes; n++ {
		wg.Add(1)
		go func(nodeIdx int) {
			defer wg.Done()
			nodeCoord := &RedisCoordinator{
				client:    coord.client,
				nodeID:    fmt.Sprintf("node-%d", nodeIdx),
				logger:    zap.NewNop(),
				leaderTTL: 30 * time.Second,
			}
			for {
				task, err := nodeCoord.ClaimTask(ctx, epoch)
				if err != nil {
					t.Errorf("node-%d ClaimTask error: %v", nodeIdx, err)
					return
				}
				if task == nil {
					return // pool empty
				}
				nodeClaimed[nodeIdx] = append(nodeClaimed[nodeIdx], task.ID)
			}
		}(n)
	}
	wg.Wait()

	// Verify total claimed == 100 (no duplication, no loss)
	total := 0
	allIDs := make(map[string]int) // taskID → claiming node count
	for n, ids := range nodeClaimed {
		total += len(ids)
		for _, id := range ids {
			allIDs[id]++
			if allIDs[id] > 1 {
				t.Errorf("task %s claimed by multiple nodes (including node-%d)", id, n)
			}
		}
	}

	if total != 100 {
		t.Errorf("expected 100 total claims, got %d", total)
	}

	// Verify work distribution (at least some nodes got tasks)
	activeNodes := 0
	for _, ids := range nodeClaimed {
		if len(ids) > 0 {
			activeNodes++
		}
	}
	if activeNodes < 2 {
		t.Logf("Warning: only %d nodes got tasks (possible but unlikely with 100 tasks)", activeNodes)
	}
}

func TestMultiNode_LeaderElection(t *testing.T) {
	// Multiple nodes compete for leadership — exactly one should win
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	const numNodes = 10
	var winners int32
	var wg sync.WaitGroup

	for i := 0; i < numNodes; i++ {
		wg.Add(1)
		go func(nodeIdx int) {
			defer wg.Done()
			coord := NewRedisCoordinator(client, fmt.Sprintf("node-%d", nodeIdx), zap.NewNop())
			ok, err := coord.TryBecomeLeader(ctx)
			if err != nil {
				t.Errorf("node-%d leader election error: %v", nodeIdx, err)
				return
			}
			if ok {
				atomic.AddInt32(&winners, 1)
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("expected exactly 1 leader, got %d", winners)
	}
}
