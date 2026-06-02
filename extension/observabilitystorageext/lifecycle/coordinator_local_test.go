// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLocalCoordinator_TryBecomeLeader(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()

	ok, err := c.TryBecomeLeader(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected to always become leader in local mode")
	}
}

func TestLocalCoordinator_SubmitAndClaimTasks(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{
		{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "otel-traces-2026.01.01"},
		{ID: "t2", Epoch: epoch, Signal: SignalTrace, IndexName: "otel-traces-2026.01.02"},
		{ID: "t3", Epoch: epoch, Signal: SignalLog, IndexName: "otel-logs-2026.01.01"},
	}

	if err := c.SubmitTasks(ctx, epoch, tasks); err != nil {
		t.Fatalf("SubmitTasks failed: %v", err)
	}

	// Claim all tasks
	var claimed []PurgeTask
	for {
		task, err := c.ClaimTask(ctx, epoch)
		if err != nil {
			t.Fatalf("ClaimTask failed: %v", err)
		}
		if task == nil {
			break
		}
		claimed = append(claimed, *task)
	}

	if len(claimed) != 3 {
		t.Fatalf("expected 3 tasks claimed, got %d", len(claimed))
	}
}

func TestLocalCoordinator_ClaimTaskEmpty(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()

	task, err := c.ClaimTask(ctx, 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task != nil {
		t.Fatal("expected nil task from empty coordinator")
	}
}

func TestLocalCoordinator_ReportResultAndProgress(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{
		{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"},
		{ID: "t2", Epoch: epoch, Signal: SignalTrace, IndexName: "idx2"},
		{ID: "t3", Epoch: epoch, Signal: SignalTrace, IndexName: "idx3"},
	}

	_ = c.SubmitTasks(ctx, epoch, tasks)

	// Claim and report
	for i := 0; i < 3; i++ {
		task, _ := c.ClaimTask(ctx, epoch)
		status := TaskStatusSuccess
		if i == 2 {
			status = TaskStatusFailed
		}
		_ = c.ReportResult(ctx, epoch, task.ID, TaskResult{
			Status:    status,
			NodeID:    "test-node",
			StartedAt: time.Now(),
			DoneAt:    time.Now(),
		})
	}

	progress, err := c.GetProgress(ctx, epoch)
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

func TestLocalCoordinator_GetActiveEpoch(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	// No active epoch initially
	active, err := c.GetActiveEpoch(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active != 0 {
		t.Fatalf("expected 0, got %d", active)
	}

	// Submit tasks → epoch becomes active
	tasks := []PurgeTask{{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"}}
	_ = c.SubmitTasks(ctx, epoch, tasks)

	active, _ = c.GetActiveEpoch(ctx)
	if active != epoch {
		t.Fatalf("expected epoch=%d, got %d", epoch, active)
	}

	// Claim and report → still active until all reported
	task, _ := c.ClaimTask(ctx, epoch)
	_ = c.ReportResult(ctx, epoch, task.ID, TaskResult{Status: TaskStatusSuccess})

	// After all tasks reported, epoch should be inactive
	active, _ = c.GetActiveEpoch(ctx)
	if active != 0 {
		t.Fatalf("expected 0 after completion, got %d", active)
	}
}

func TestLocalCoordinator_CompleteEpoch(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	tasks := []PurgeTask{{ID: "t1", Epoch: epoch, Signal: SignalTrace, IndexName: "idx1"}}
	_ = c.SubmitTasks(ctx, epoch, tasks)

	if err := c.CompleteEpoch(ctx, epoch); err != nil {
		t.Fatalf("CompleteEpoch failed: %v", err)
	}

	// After complete, no active epoch
	active, _ := c.GetActiveEpoch(ctx)
	if active != 0 {
		t.Fatalf("expected 0 after CompleteEpoch, got %d", active)
	}
}

func TestLocalCoordinator_ConcurrentClaim(t *testing.T) {
	c := NewLocalCoordinator()
	ctx := context.Background()
	epoch := time.Now().UnixMilli()

	// Submit 100 tasks
	tasks := make([]PurgeTask, 100)
	for i := range tasks {
		tasks[i] = PurgeTask{ID: "t" + string(rune('0'+i%10)) + string(rune('0'+i/10)), Epoch: epoch, Signal: SignalTrace, IndexName: "idx"}
	}
	_ = c.SubmitTasks(ctx, epoch, tasks)

	// 10 goroutines competing to claim
	var mu sync.Mutex
	var total int
	var wg sync.WaitGroup

	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := 0
			for {
				task, err := c.ClaimTask(ctx, epoch)
				if err != nil {
					t.Errorf("ClaimTask error: %v", err)
					return
				}
				if task == nil {
					break
				}
				count++
			}
			mu.Lock()
			total += count
			mu.Unlock()
		}()
	}
	wg.Wait()

	if total != 100 {
		t.Errorf("expected 100 total claimed, got %d (race condition!)", total)
	}
}
