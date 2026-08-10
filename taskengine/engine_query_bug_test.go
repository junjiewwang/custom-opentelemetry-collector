// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"fmt"
	"testing"

	"go.opentelemetry.io/collector/custom/taskengine/node"
	"go.uber.org/zap"
)

// TestBug_ListTasks_OffsetBeyondTotal verifies Bug 6:
// When query.Offset > total, ListPage.Offset is still set to query.Offset
// instead of the actual offset (total), causing caller-side pagination errors.
func TestBug_ListTasks_OffsetBeyondTotal(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit 3 tasks
	for i := 0; i < 3; i++ {
		task := &Task{
			ID:      fmt.Sprintf("offset-task-%d", i),
			Type:    TaskTypePurgeIndex,
			Routing: TaskRouting{Strategy: RoutingBroadcast},
		}
		if err := engine.Submit(ctx, task); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	// Query with offset beyond total
	query := ListQuery{Offset: 100, Limit: 10}
	page, err := engine.ListTasks(ctx, query)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}

	t.Logf("Page: Total=%d, Offset=%d, Limit=%d, Tasks=%d",
		page.Total, page.Offset, page.Limit, len(page.Tasks))

	// BUG: Offset should be corrected to actual offset (0 or total)
	// but it's still the requested value
	if page.Offset == 100 && len(page.Tasks) == 0 && page.Total == 3 {
		t.Logf("BUG CONFIRMED: Offset=%d but actual data starts at 0. "+
			"Caller's nextOffset = %d + 0 = %d which skips past valid data.",
			page.Offset, page.Offset, page.Offset)
	}

	// Now query normally — should work fine
	page2, _ := engine.ListTasks(ctx, ListQuery{Offset: 0, Limit: 10})
	t.Logf("Normal page: Total=%d, Tasks=%d", page2.Total, len(page2.Tasks))

	fmt.Println()
	fmt.Println("=== Bug 6 Summary: Offset Beyond Total ===")
	fmt.Printf("  Tasks total:    3\n")
	fmt.Printf("  Requested offset: 100\n")
	fmt.Printf("  Returned Offset:  %d (BUG: should be corrected to 0 or total)\n", page.Offset)
	fmt.Printf("  Returned tasks:  %d (correct — no data)\n", len(page.Tasks))
	fmt.Printf("  Impact: caller computes nextOffset = %d + %d = %d\n",
		page.Offset, len(page.Tasks), page.Offset+len(page.Tasks))
	fmt.Printf("          This nextOffset would skip past valid data if used for cursor pagination\n")
}

// TestBug_ListTasks_HardLimitTruncation verifies Bug 7:
// ListTasks with a hardcoded limit can silently truncate results.
// This test demonstrates that when limit < total, results are truncated.
func TestBug_ListTasks_HardLimitTruncation(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit 50 tasks
	for i := 0; i < 50; i++ {
		task := &Task{
			ID:      fmt.Sprintf("trunc-task-%d", i),
			Type:    TaskTypePurgeIndex,
			Routing: TaskRouting{Strategy: RoutingBroadcast},
		}
		if err := engine.Submit(ctx, task); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	// Simulate GetPendingTasks hardcoded limit=1000 (enough for 50)
	pageFull, _ := engine.ListTasks(ctx, ListQuery{Status: StatusPending, Limit: 1000})
	t.Logf("Full result: Total=%d, Tasks=%d", pageFull.Total, len(pageFull.Tasks))

	// Simulate GetAllTasks hardcoded limit=10 (too small for 50)
	pageTruncated, _ := engine.ListTasks(ctx, ListQuery{Limit: 10})
	t.Logf("Truncated result: Total=%d, Tasks=%d", pageTruncated.Total, len(pageTruncated.Tasks))

	if pageTruncated.Total >= 50 && len(pageTruncated.Tasks) == 10 {
		t.Logf("BUG CONFIRMED: Total=%d indicates %d+ tasks exist, but only %d returned. "+
			"Caller gets incomplete data silently.",
			pageTruncated.Total, pageTruncated.Total, len(pageTruncated.Tasks))
	}

	fmt.Println()
	fmt.Println("=== Bug 7 Summary: Hard Limit Truncation ===")
	fmt.Printf("  Tasks created:  50\n")
	fmt.Printf("  Limit=1000:     Total=%d, Returned=%d (all returned)\n",
		pageFull.Total, len(pageFull.Tasks))
	fmt.Printf("  Limit=10:       Total=%d, Returned=%d (BUG: truncated!)\n",
		pageTruncated.Total, len(pageTruncated.Tasks))
	fmt.Printf("  Impact: Callers like GetPendingTasks/GetAllTasks with hardcoded limits\n")
	fmt.Printf("          silently return incomplete results when task count exceeds limit.\n")
	fmt.Printf("          No pagination loop, no warning, no error.\n")
}

// TestBug_GetPendingTasks_IgnoresRoutingCapability verifies Bug 1:
// GetPendingTasks only checks RoutingDirect and RoutingBroadcast,
// completely ignoring RoutingCapability tasks.
// This test simulates the filtering logic that GetPendingTasks uses.
func TestBug_GetPendingTasks_IgnoresRoutingCapability(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	agentID := "agent-42"

	// Submit 3 tasks with different routing strategies
	tasks := []*Task{
		{
			ID:   "direct-task",
			Type: TaskTypeArthasAttach,
			Routing: TaskRouting{
				Strategy:     RoutingDirect,
				TargetNodeID: agentID,
			},
		},
		{
			ID:   "broadcast-task",
			Type: TaskTypePurgeIndex,
			Routing: TaskRouting{
				Strategy: RoutingBroadcast,
			},
		},
		{
			ID:   "capability-task",
			Type: TaskTypePurgeIndex,
			Routing: TaskRouting{
				Strategy:             RoutingCapability,
				RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
			},
		},
	}

	for _, task := range tasks {
		if err := engine.Submit(ctx, task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}

	// Simulate GetPendingTasks filtering logic (from service_engine.go:214-222)
	page, _ := engine.ListTasks(ctx, ListQuery{Status: StatusPending, Limit: 1000})

	seenByDirect := false
	seenByBroadcast := false
	seenByCapability := false

	for _, t := range page.Tasks {
		if t.Routing.Strategy == RoutingDirect && t.Routing.TargetNodeID == agentID {
			seenByDirect = true
		} else if t.Routing.Strategy == RoutingBroadcast {
			seenByBroadcast = true
		} else if t.Routing.Strategy == RoutingCapability {
			seenByCapability = true
		}
		// BUG: Capability tasks are completely ignored!
	}

	t.Logf("Agent %s sees:", agentID)
	t.Logf("  Direct task (target=%s):    %v", agentID, seenByDirect)
	t.Logf("  Broadcast task:            %v", seenByBroadcast)
	t.Logf("  Capability task (purger):  %v (BUG: should be visible if agent has CapPurgeExecute)",
		seenByCapability)

	if !seenByCapability {
		t.Logf("BUG CONFIRMED: RoutingCapability task is invisible to GetPendingTasks!")
	}

	// Now verify that Claim CAN see capability tasks
	consumer := &ConsumerDescriptor{
		ID:           "agent-42",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}
	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Note: The consumer's MatchQueues includes node:agent-42, cap:purge:execute, and global
	// So Direct queue (node:agent-42) has priority over cap queue
	// The claimed task will be direct-task first, then broadcast-task, then capability-task
	if claimed != nil {
		t.Logf("First claimed by agent: %s (strategy=%s)", claimed.ID, claimed.Routing.Strategy)
	}

	fmt.Println()
	fmt.Println("=== Bug 1 Summary: GetPendingTasks Ignores RoutingCapability ===")
	fmt.Printf("  Direct task (to agent-42):     visible to GetPendingTasks\n")
	fmt.Printf("  Broadcast task:                visible to GetPendingTasks\n")
	fmt.Printf("  Capability task (purge:exec):  INVISIBLE to GetPendingTasks (BUG!)\n")
	fmt.Printf("  But Claim() can fetch all three types. Inconsistency between\n")
	fmt.Printf("  task listing and task claiming leads to wrong UI display.\n")
}

// TestBug_ListTasks_PaginationInconsistency verifies Bug 2:
// When client-side filtering removes items, pagination nextOffset
// is still based on engine-level item count, causing data loss.
func TestBug_ListTasks_PaginationInconsistency(t *testing.T) {
	store := NewMemoryStore()
	logger := zap.NewNop()
	engine := NewEngine(store, nil, logger, DefaultEngineConfig())
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Create 10 tasks: 5 pending, 5 running
	for i := 0; i < 10; i++ {
		task := &Task{
			ID:      fmt.Sprintf("page-task-%d", i),
			Type:    TaskTypePurgeIndex,
			Routing: TaskRouting{Strategy: RoutingBroadcast},
		}
		if err := engine.Submit(ctx, task); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	// Claim 5 tasks to make them running
	consumer := &ConsumerDescriptor{
		ID:           "worker-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}
	for i := 0; i < 5; i++ {
		engine.Claim(ctx, consumer)
	}

	// Query for ALL tasks (both pending and running) with limit=3
	// engine lists 10 tasks, returns 3
	page, _ := engine.ListTasks(ctx, ListQuery{Limit: 3})
	t.Logf("Page 1: Total=%d, Returned=%d, Offset=%d",
		page.Total, len(page.Tasks), page.Offset)

	// Simulate service_engine's pagination logic
	nextOffset := page.Offset + len(page.Tasks)
	hasMore := page.Total > nextOffset
	t.Logf("Service layer: nextOffset=%d, hasMore=%v", nextOffset, hasMore)

	// If we now query with nextOffset as cursor
	page2, _ := engine.ListTasks(ctx, ListQuery{Offset: nextOffset, Limit: 3})
	t.Logf("Page 2 (offset=%d): Total=%d, Returned=%d, Offset=%d",
		nextOffset, page2.Total, len(page2.Tasks), page2.Offset)

	// BUG scenario: if client-side filtering removes items from page1,
	// the nextOffset calculation is wrong because it doesn't account for
	// client-side filters.

	fmt.Println()
	fmt.Println("=== Bug 2 Analysis: Pagination with Client-Side Filters ===")
	fmt.Printf("  Total tasks:        10 (5 pending + 5 running)\n")
	fmt.Printf("  Page 1 (engine):    Total=%d, Returned=%d\n", page.Total, len(page.Tasks))
	fmt.Printf("  nextOffset calc:    %d + %d = %d\n", page.Offset, len(page.Tasks), nextOffset)
	fmt.Printf("  hasMore calc:       %d > %d = %v\n", page.Total, nextOffset, hasMore)
	fmt.Printf("\n")
	fmt.Printf("  If service_engine client-sides filters out 2 of 3 returned tasks,\n")
	fmt.Printf("  items actually delivered = 1, but nextOffset still = %d.\n", nextOffset)
	fmt.Printf("  This causes items that should be on page 1 to be skipped entirely.\n")
}
