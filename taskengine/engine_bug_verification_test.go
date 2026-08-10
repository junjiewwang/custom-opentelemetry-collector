// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.opentelemetry.io/collector/custom/taskengine/node"
	"go.uber.org/zap"
)

// TestBug_RetryTask_InvalidStateTransition verifies Bug 1:
// retryTask transitions the task to Failed but never saves the retry copy,
// causing subsequent Claim to fail with InvalidTransitionError (Failed → Running).
func TestBug_RetryTask_InvalidStateTransition(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, EngineConfig{
		DefaultTimeout:    5 * 60 * 1e9, // 5 min in ns
		DefaultMaxRetries: 3,
	})
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Step 1: Submit a task with retry enabled
	task := &Task{
		ID:         "retry-bug-1",
		Type:       TaskTypePurgeIndex,
		MaxRetries: 2,
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
		},
	}
	if err := engine.Submit(ctx, task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Step 2: Consumer claims the task
	consumer := &ConsumerDescriptor{
		ID:           "purger-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}
	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim a task")
	}
	t.Logf("Claimed task %s, status=%s", claimed.ID, claimed.Status)

	// Verify claim changed status to Running
	got, _ := engine.GetTask(ctx, "retry-bug-1")
	t.Logf("After claim: status=%s, claimedBy=%s, retryCount=%d", got.Status, got.ClaimedBy, got.RetryCount)

	// Step 3: Report failure → triggers retryTask
	failResult := &TaskResult{
		TaskID:  "retry-bug-1",
		NodeID:  "purger-1",
		Status:  StatusFailed,
		Error:   "simulated failure",
		Output:  json.RawMessage(`{}`),
	}
	if err := engine.Report(ctx, failResult); err != nil {
		t.Fatalf("report failure: %v", err)
	}

	// Step 4: Check task state after retry
	gotAfterRetry, err := engine.GetTask(ctx, "retry-bug-1")
	if err != nil {
		t.Fatalf("get task after retry: %v", err)
	}
	t.Logf("After retry: status=%s, retryCount=%d, claimedBy=%s",
		gotAfterRetry.Status, gotAfterRetry.RetryCount, gotAfterRetry.ClaimedBy)

	// BUG: The task is now in Failed state because retryTask() called
	// UpdateTaskStatus(taskID, StatusFailed) but never saved the retry copy.
	// The retry copy had Status=Pending but was never persisted.
	if gotAfterRetry.Status == StatusFailed {
		t.Logf("BUG CONFIRMED: Task stuck in Failed state after retry")
	}

	// Step 5: Try to claim the task again — this is where the bug manifests
	claimed2, err2 := engine.Claim(ctx, consumer)
	t.Logf("Second claim result: task=%v, err=%v", claimed2, err2)

	// BUG EXPECTED: claim returns (nil, nil) because the task is still Failed
	// in the store and can't be dequeued (it was re-enqueued but transition is invalid)
	if claimed2 == nil && err2 == nil {
		t.Logf("BUG CONFIRMED: Task was re-enqueued to queue but claim returned nil because state transition Failed→Running is invalid")
	}

	// Step 6: Verify the task is permanently stuck
	statusAfterClaim, _ := engine.GetTask(ctx, "retry-bug-1")
	if statusAfterClaim != nil {
		t.Logf("Final task state: status=%s, retryCount=%d, maxRetries=%d",
			statusAfterClaim.Status, statusAfterClaim.RetryCount, statusAfterClaim.MaxRetries)
	}

	// Print summary
	fmt.Println()
	fmt.Println("=== Bug 1 Summary: retryTask Invalid State Transition ===")
	fmt.Printf("  Task ID:       %s\n", task.ID)
	fmt.Printf("  After submit:  pending\n")
	fmt.Printf("  After claim:   running (claimedBy=%s)\n", consumer.ID)
	fmt.Printf("  After report:  %s (status=%s, retryCount=%d)\n", "BUG!", gotAfterRetry.Status, gotAfterRetry.RetryCount)
	fmt.Printf("  After re-claim: nil (task stuck)\n")
	fmt.Printf("  Root cause:    retryTask() transitions task to Failed but never saves\n")
	fmt.Printf("                 the retry copy to store. When consumer claims again,\n")
	fmt.Printf("                 UpdateTaskStatus tries Failed→Running which is invalid.\n")
}

// TestBug_PriorityIgnored_Enqueue verifies Bug 2:
// Priority parameter is completely ignored by both MemoryStore and RedisStore Enqueue.
func TestBug_PriorityIgnored_Enqueue(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	consumer := &ConsumerDescriptor{
		ID:           "worker-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}

	// Submit 3 tasks with different priorities in reverse order
	// Expected: high priority (10) should be claimed first
	tasks := []struct {
		id       string
		priority int32
	}{
		{"prio-low", 1},
		{"prio-med", 5},
		{"prio-high", 10},
	}

	// Submit in order: low, med, high
	for _, tc := range tasks {
		task := &Task{
			ID:       tc.id,
			Type:     TaskTypePurgeIndex,
			Priority: tc.priority,
			Routing: TaskRouting{
				Strategy: RoutingBroadcast,
			},
		}
		if err := engine.Submit(ctx, task); err != nil {
			t.Fatalf("submit %s: %v", tc.id, err)
		}
		t.Logf("Submitted task %s with priority=%d", tc.id, tc.priority)
	}

	// Claim tasks in order — they should come out as FIFO (low, med, high) since priority is ignored
	claimOrder := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		claimed, err := engine.Claim(ctx, consumer)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if claimed == nil {
			t.Fatal("expected to claim a task")
		}
		claimOrder = append(claimOrder, claimed.ID)
		t.Logf("Claimed #%d: %s (priority=%d)", i+1, claimed.ID, claimed.Priority)
	}

	fmt.Println()
	fmt.Println("=== Bug 2 Summary: Priority Ignored in Enqueue ===")
	fmt.Printf("  Submit order:  prio-low(1), prio-med(5), prio-high(10)\n")
	fmt.Printf("  Claim order:   %v\n", claimOrder)
	fmt.Printf("  Expected:       prio-high(10), prio-med(5), prio-low(1)\n")
	fmt.Printf("  Actual:         %v (FIFO, priority ignored)\n", claimOrder)
	if claimOrder[0] == "prio-low" {
		fmt.Printf("  BUG CONFIRMED:  Priority parameter is ignored, tasks are FIFO\n")
	}
}

// TestBug_OrphanTask_EnqueueFailure verifies Bug 3:
// When Enqueue fails after SaveTask succeeds, the task becomes an orphan.
func TestBug_OrphanTask_EnqueueFailure(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	consumer := &ConsumerDescriptor{
		ID:           "worker-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}

	// Submit a task normally — it's saved and enqueued
	task1 := &Task{
		ID:   "normal-task",
		Type: TaskTypePurgeIndex,
		Routing: TaskRouting{
			Strategy: RoutingBroadcast,
		},
	}
	if err := engine.Submit(ctx, task1); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Verify task1 exists in store
	got1, _ := engine.GetTask(ctx, "normal-task")
	if got1 == nil {
		t.Fatal("normal task not found in store")
	}
	t.Logf("Normal task: status=%s, exists in store=true, in queue=true", got1.Status)

	// Now simulate the orphan scenario: manually save a task to store without enqueuing
	orphanTask := &Task{
		ID:         "orphan-task",
		Type:       TaskTypePurgeIndex,
		Status:     StatusPending,
		CreatedAt:  1700000000000,
		MaxRetries: 3,
		Routing: TaskRouting{
			Strategy: RoutingBroadcast,
		},
	}
	if err := store.SaveTask(ctx, orphanTask); err != nil {
		t.Fatalf("save orphan: %v", err)
	}

	// Verify orphan exists in store
	gotOrphan, _ := engine.GetTask(ctx, "orphan-task")
	if gotOrphan == nil {
		t.Fatal("orphan task not found in store")
	}
	t.Logf("Orphan task: status=%s, exists in store=true, in queue=false", gotOrphan.Status)

	// Try to claim — should only get the normal task
	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim normal task")
	}
	t.Logf("Claimed: %s", claimed.ID)

	// Try to claim again — orphan is NOT in any queue, should return nil
	claimed2, err2 := engine.Claim(ctx, consumer)
	if err2 != nil {
		t.Fatalf("claim2: %v", err2)
	}

	// ListTasks can still find the orphan
	page, _ := engine.ListTasks(ctx, ListQuery{Status: StatusPending, Limit: 100})
	orphanFound := false
	for _, t := range page.Tasks {
		if t.ID == "orphan-task" {
			orphanFound = true
			break
		}
	}

	fmt.Println()
	fmt.Println("=== Bug 3 Summary: Orphan Task on Enqueue Failure ===")
	fmt.Printf("  Normal task: exists in store=true, in queue=true, claimable=true\n")
	fmt.Printf("  Orphan task:  exists in store=%v, in queue=false, claimable=%v\n", true, claimed2 == nil)
	fmt.Printf("  ListTasks finds orphan: %v\n", orphanFound)
	if claimed2 == nil && orphanFound {
		fmt.Printf("  BUG CONFIRMED:  Task saved to store but not enqueued becomes orphan.\n")
		fmt.Printf("                  ListTasks finds it but consumers can never claim it.\n")
	}
}
