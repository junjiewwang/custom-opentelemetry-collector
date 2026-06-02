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

func newTestEngine() *EngineImpl {
	store := NewMemoryStore()
	logger := zap.NewNop()
	return NewEngine(store, nil, logger, DefaultEngineConfig())
}

func TestEngine_Submit_Basic(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	task := &Task{
		ID:   "task-1",
		Type: TaskTypePurgeIndex,
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
		},
	}

	if err := engine.Submit(ctx, task); err != nil {
		t.Fatalf("submit error: %v", err)
	}

	// Verify task was saved
	got, err := engine.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("get task error: %v", err)
	}
	if got == nil {
		t.Fatal("expected task to be found")
	}
	if got.Status != StatusPending {
		t.Errorf("expected status Pending, got %s", got.Status)
	}
	if got.CreatedAt == 0 {
		t.Error("createdAt should be set")
	}
}

func TestEngine_Submit_DuplicateID(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	task := &Task{ID: "dup-1", Type: TaskTypePurgeIndex}
	_ = engine.Submit(ctx, task)

	err := engine.Submit(ctx, &Task{ID: "dup-1", Type: TaskTypePurgeIndex})
	if err == nil {
		t.Error("expected error for duplicate task ID")
	}
}

func TestEngine_Submit_MissingID(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)

	err := engine.Submit(ctx, &Task{Type: TaskTypePurgeIndex})
	if err == nil {
		t.Error("expected error for missing task ID")
	}
}

func TestEngine_Claim_Basic(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit a purge task
	task := &Task{
		ID:   "claim-1",
		Type: TaskTypePurgeIndex,
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
		},
	}
	_ = engine.Submit(ctx, task)

	// Consumer with matching capability claims it
	consumer := &ConsumerDescriptor{
		ID:           "purger-node-1",
		Roles:        []node.Role{node.RolePurger},
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute, node.CapStorageRead, node.CapStorageDelete, node.CapPurgePlan),
	}

	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim a task")
	}
	if claimed.ID != "claim-1" {
		t.Errorf("expected task-1, got %s", claimed.ID)
	}

	// Verify status changed
	got, _ := engine.GetTask(ctx, "claim-1")
	if got.Status != StatusRunning {
		t.Errorf("expected Running after claim, got %s", got.Status)
	}
}

func TestEngine_Claim_EmptyQueue(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	consumer := &ConsumerDescriptor{
		ID:           "node-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}

	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if claimed != nil {
		t.Error("expected nil when no tasks available")
	}
}

func TestEngine_Claim_CapabilityMismatch(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit arthas task to direct queue
	task := &Task{
		ID:   "arthas-1",
		Type: TaskTypeArthasAttach,
		Routing: TaskRouting{
			Strategy:     RoutingDirect,
			TargetNodeID: "agent-42",
		},
	}
	_ = engine.Submit(ctx, task)

	// Different consumer tries to claim
	consumer := &ConsumerDescriptor{
		ID:           "purger-node",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}

	claimed, err := engine.Claim(ctx, consumer)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if claimed != nil {
		t.Error("consumer without matching direct queue should not get this task")
	}
}

func TestEngine_Report_Success(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit and claim
	task := &Task{
		ID:   "report-1",
		Type: TaskTypePurgeIndex,
		Routing: TaskRouting{
			Strategy: RoutingBroadcast,
		},
	}
	_ = engine.Submit(ctx, task)

	consumer := &ConsumerDescriptor{
		ID:           "node-1",
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute),
	}
	_, _ = engine.Claim(ctx, consumer)

	// Report success
	result := &TaskResult{
		TaskID: "report-1",
		NodeID: "node-1",
		Status: StatusSuccess,
		Output: json.RawMessage(`{"deleted": 42}`),
	}
	if err := engine.Report(ctx, result); err != nil {
		t.Fatalf("report error: %v", err)
	}

	// Verify result stored
	got, _ := engine.GetResult(ctx, "report-1")
	if got == nil {
		t.Fatal("expected result to be stored")
	}
	if got.Status != StatusSuccess {
		t.Errorf("expected Success, got %s", got.Status)
	}
}

func TestEngine_Cancel(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	task := &Task{
		ID:   "cancel-1",
		Type: TaskTypePurgeIndex,
		Routing: TaskRouting{
			Strategy: RoutingBroadcast,
		},
	}
	_ = engine.Submit(ctx, task)

	if err := engine.Cancel(ctx, "cancel-1"); err != nil {
		t.Fatalf("cancel error: %v", err)
	}

	got, _ := engine.GetTask(ctx, "cancel-1")
	if got.Status != StatusCancelled {
		t.Errorf("expected Cancelled, got %s", got.Status)
	}
}

func TestEngine_Cancel_AlreadyTerminal(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	task := &Task{
		ID:      "cancel-term",
		Type:    TaskTypePurgeIndex,
		Routing: TaskRouting{Strategy: RoutingBroadcast},
	}
	_ = engine.Submit(ctx, task)
	_ = engine.Cancel(ctx, "cancel-term")

	// Cancelling again should be a no-op
	if err := engine.Cancel(ctx, "cancel-term"); err != nil {
		t.Errorf("cancel of terminal task should be no-op, got: %v", err)
	}
}

func TestEngine_GetProgress(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit 3 tasks in a group
	for i := 0; i < 3; i++ {
		task := &Task{
			ID:      fmt.Sprintf("group-task-%d", i),
			Type:    TaskTypePurgeIndex,
			GroupID: "epoch-1",
			Routing: TaskRouting{Strategy: RoutingBroadcast},
		}
		_ = engine.Submit(ctx, task)
	}

	progress, err := engine.GetProgress(ctx, TaskTypePurgeIndex, "epoch-1")
	if err != nil {
		t.Fatalf("get progress error: %v", err)
	}
	if progress.Total != 3 {
		t.Errorf("expected total 3, got %d", progress.Total)
	}
	if progress.Pending != 3 {
		t.Errorf("expected pending 3, got %d", progress.Pending)
	}
}

func TestEngine_SubmitBatch(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	tasks := []*Task{
		{ID: "batch-1", Type: TaskTypePurgeIndex, Routing: TaskRouting{Strategy: RoutingBroadcast}},
		{ID: "batch-2", Type: TaskTypePurgeIndex, Routing: TaskRouting{Strategy: RoutingBroadcast}},
		{ID: "batch-3", Type: TaskTypePurgeIndex, Routing: TaskRouting{Strategy: RoutingBroadcast}},
	}

	if err := engine.SubmitBatch(ctx, tasks); err != nil {
		t.Fatalf("submit batch error: %v", err)
	}

	for _, task := range tasks {
		got, _ := engine.GetTask(ctx, task.ID)
		if got == nil {
			t.Errorf("task %s not found after batch submit", task.ID)
		}
	}
}

func TestEngine_DirectRouting(t *testing.T) {
	engine := newTestEngine()
	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop(ctx)

	// Submit task targeted at specific agent
	task := &Task{
		ID:   "direct-1",
		Type: TaskTypeArthasAttach,
		Routing: TaskRouting{
			Strategy:     RoutingDirect,
			TargetNodeID: "agent-99",
		},
	}
	_ = engine.Submit(ctx, task)

	// Only the targeted agent should be able to claim it
	targetConsumer := &ConsumerDescriptor{
		ID:           "agent-99",
		Roles:        []node.Role{node.RoleAgent},
		Capabilities: node.NewCapabilitySet(node.CapArthasExec),
	}
	claimed, err := engine.Claim(ctx, targetConsumer)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if claimed == nil {
		t.Fatal("target agent should be able to claim direct task")
	}
	if claimed.ID != "direct-1" {
		t.Errorf("expected direct-1, got %s", claimed.ID)
	}
}
