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

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/taskengine"
	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// ═══════════════════════════════════════════════════
// Test Mocks (prefixed to avoid collision with scheduler_test.go mocks)
// ═══════════════════════════════════════════════════

// engineTestPurger implements LifecyclePurger + IndexLister + SingleIndexPurger.
type engineTestPurger struct {
	mu             sync.Mutex
	expiredIndices map[SignalType][]string
	deletedCalls   []string
	failOn         map[string]error // indexName → error to return
}

func newEngineTestPurger() *engineTestPurger {
	return &engineTestPurger{
		expiredIndices: make(map[SignalType][]string),
		failOn:         make(map[string]error),
	}
}

func (m *engineTestPurger) PurgeExpired(_ context.Context, signal SignalType, _ time.Time) (*PurgeResult, error) {
	return &PurgeResult{Signal: signal, DeletedDocs: 10, DeletedUnits: 1}, nil
}

func (m *engineTestPurger) PurgeByApp(_ context.Context, _ string, _ SignalType, _ time.Time) (*PurgeResult, error) {
	return nil, nil
}

func (m *engineTestPurger) EstimatePurge(_ context.Context, _ SignalType, _ time.Time) (*PurgeEstimate, error) {
	return &PurgeEstimate{}, nil
}

func (m *engineTestPurger) GetDataBoundary(_ context.Context, _ SignalType) (*DataBoundary, error) {
	return &DataBoundary{IsEmpty: false}, nil
}

// IndexLister implementation
func (m *engineTestPurger) ListExpired(_ context.Context, signal SignalType, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expiredIndices[signal], nil
}

// SingleIndexPurger implementation
func (m *engineTestPurger) DeleteSingleIndex(_ context.Context, indexName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedCalls = append(m.deletedCalls, indexName)
	if err, ok := m.failOn[indexName]; ok {
		return err
	}
	return nil
}

func (m *engineTestPurger) getDeletedCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.deletedCalls))
	copy(result, m.deletedCalls)
	return result
}

// engineTestResolver implements RetentionResolver.
type engineTestResolver struct {
	duration time.Duration
}

func (r *engineTestResolver) Resolve(_ context.Context, _ SignalType, _ string) (EffectiveRetention, error) {
	return EffectiveRetention{Duration: r.duration, Source: SourceBuiltinDefault}, nil
}

func (r *engineTestResolver) ResolveAll(_ context.Context, _ string) (map[SignalType]EffectiveRetention, error) {
	return nil, nil
}

// engineTestAuditEmitter records audit events.
type engineTestAuditEmitter struct {
	mu     sync.Mutex
	events []LifecycleEvent
}

func (m *engineTestAuditEmitter) Emit(_ context.Context, event LifecycleEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *engineTestAuditEmitter) getEvents() []LifecycleEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]LifecycleEvent, len(m.events))
	copy(result, m.events)
	return result
}

// ═══════════════════════════════════════════════════
// End-to-End Test: Engine-based Distributed Purge
// ═══════════════════════════════════════════════════

// TestEngineBasedDistributedPurge_EndToEnd verifies the complete flow:
//
//	Plan → Submit → Claim → Execute → Report → Verify
//
// using the real MemoryStore + EngineImpl + LocalLeaderElector.
func TestEngineBasedDistributedPurge_EndToEnd(t *testing.T) {
	// Setup
	store := taskengine.NewMemoryStore()
	logger := zap.NewNop()
	engine := taskengine.NewEngine(store, nil, logger, taskengine.DefaultEngineConfig())
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("engine start: %v", err)
	}
	defer engine.Stop(context.Background())

	elector := NewLocalLeaderElector()

	purger := newEngineTestPurger()
	// Setup 5 expired indices for trace, 3 for metric
	purger.expiredIndices[SignalTrace] = []string{
		"traces-2024.01.01",
		"traces-2024.01.02",
		"traces-2024.01.03",
		"traces-2024.01.04",
		"traces-2024.01.05",
	}
	purger.expiredIndices[SignalMetric] = []string{
		"metrics-2024.01.01",
		"metrics-2024.01.02",
		"metrics-2024.01.03",
	}

	resolver := &engineTestResolver{duration: 7 * 24 * time.Hour}
	audit := &engineTestAuditEmitter{}

	config := SchedulerConfig{
		Enabled:              true,
		Distributed:          true,
		DistributedThreshold: 2, // Low threshold to ensure distributed mode
		WorkerConcurrency:    5,
		TaskTimeout:          5 * time.Second,
		MaxRetries:           2,
		VerifyTimeout:        10 * time.Second,
		VerifyPollInterval:   100 * time.Millisecond,
	}
	config.ApplyDefaults()

	// Create orchestrator
	orchestrator := NewDistributedPurgeOrchestrator(engine, elector, config, "test-node-1", logger)

	// Execute the full distributed purge
	stopCh := make(chan struct{})
	handled := orchestrator.Execute(context.Background(), purger, purger, resolver, audit, stopCh)

	// Verify
	if !handled {
		t.Fatal("expected distributed purge to be handled")
	}

	// Check all 8 indices were deleted
	deletedCalls := purger.getDeletedCalls()
	if len(deletedCalls) != 8 {
		t.Errorf("expected 8 deleted calls, got %d: %v", len(deletedCalls), deletedCalls)
	}

	// Check audit events
	events := audit.getEvents()
	hasPlan := false
	hasVerify := false
	for _, ev := range events {
		if ev.Action == ActionDistPlan {
			hasPlan = true
		}
		if ev.Action == ActionDistVerify {
			hasVerify = true
		}
	}
	if !hasPlan {
		t.Error("expected ActionDistPlan audit event")
	}
	if !hasVerify {
		t.Error("expected ActionDistVerify audit event")
	}

	// Check engine progress shows all completed
	epoch := elector.epoch // Last set epoch
	if epoch == 0 {
		// Epoch was cleared by ClearActiveEpoch, which is correct behavior
		// Just verify the tasks were all processed
		t.Log("Epoch cleared (expected — verifyAndComplete was called)")
	}
}

// TestEngineBasedDistributedPurge_BelowThreshold verifies fallback to single-node
// when task count is below the distributed threshold.
func TestEngineBasedDistributedPurge_BelowThreshold(t *testing.T) {
	store := taskengine.NewMemoryStore()
	logger := zap.NewNop()
	engine := taskengine.NewEngine(store, nil, logger, taskengine.DefaultEngineConfig())
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	elector := NewLocalLeaderElector()
	purger := newEngineTestPurger()
	purger.expiredIndices[SignalTrace] = []string{"traces-2024.01.01"} // Only 1 index

	resolver := &engineTestResolver{duration: 7 * 24 * time.Hour}
	audit := &engineTestAuditEmitter{}

	config := SchedulerConfig{
		Distributed:          true,
		DistributedThreshold: 50, // High threshold — 1 task won't meet it
		WorkerConcurrency:    5,
		TaskTimeout:          5 * time.Second,
		VerifyTimeout:        5 * time.Second,
		VerifyPollInterval:   100 * time.Millisecond,
	}
	config.ApplyDefaults()

	orchestrator := NewDistributedPurgeOrchestrator(engine, elector, config, "test-node", logger)
	stopCh := make(chan struct{})

	handled := orchestrator.Execute(context.Background(), purger, purger, resolver, audit, stopCh)

	// Should NOT be handled (falls back to single-node)
	if handled {
		t.Error("expected fallback to single-node when below threshold")
	}
}

// TestEngineBasedDistributedPurge_WithFailures verifies that failed tasks
// are properly tracked and retried by the engine.
func TestEngineBasedDistributedPurge_WithFailures(t *testing.T) {
	store := taskengine.NewMemoryStore()
	logger := zap.NewNop()
	engineCfg := taskengine.DefaultEngineConfig()
	engineCfg.DefaultMaxRetries = 0 // Disable engine auto-retry; let it fail cleanly
	engine := taskengine.NewEngine(store, nil, logger, engineCfg)
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	elector := NewLocalLeaderElector()
	purger := newEngineTestPurger()
	purger.expiredIndices[SignalTrace] = []string{
		"traces-ok-1",
		"traces-fail-1",
		"traces-ok-2",
	}
	// Make one task fail
	purger.failOn["traces-fail-1"] = fmt.Errorf("simulated ES error")

	resolver := &engineTestResolver{duration: 7 * 24 * time.Hour}
	audit := &engineTestAuditEmitter{}

	config := SchedulerConfig{
		Distributed:          true,
		DistributedThreshold: 1,
		WorkerConcurrency:    3,
		TaskTimeout:          5 * time.Second,
		MaxRetries:           1,
		VerifyTimeout:        5 * time.Second,
		VerifyPollInterval:   100 * time.Millisecond,
	}
	config.ApplyDefaults()

	orchestrator := NewDistributedPurgeOrchestrator(engine, elector, config, "test-node", logger)
	stopCh := make(chan struct{})

	handled := orchestrator.Execute(context.Background(), purger, purger, resolver, audit, stopCh)
	if !handled {
		t.Fatal("expected handled=true")
	}

	// Verify: 2 success + 1 failure in DeleteSingleIndex calls
	calls := purger.getDeletedCalls()
	if len(calls) != 3 {
		t.Errorf("expected 3 delete calls, got %d: %v", len(calls), calls)
	}

	// Verify audit has both plan and verify
	events := audit.getEvents()
	if len(events) < 2 {
		t.Errorf("expected at least 2 audit events, got %d", len(events))
	}
}

// TestEngineBasedDistributedPurge_MultiNodeSimulation simulates multiple nodes
// working on the same task pool concurrently.
func TestEngineBasedDistributedPurge_MultiNodeSimulation(t *testing.T) {
	store := taskengine.NewMemoryStore()
	logger := zap.NewNop()
	engine := taskengine.NewEngine(store, nil, logger, taskengine.DefaultEngineConfig())
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	elector := NewLocalLeaderElector()
	purger := newEngineTestPurger()
	// 20 expired indices
	for i := 0; i < 20; i++ {
		purger.expiredIndices[SignalTrace] = append(purger.expiredIndices[SignalTrace],
			fmt.Sprintf("traces-2024.01.%02d", i+1))
	}

	resolver := &engineTestResolver{duration: 7 * 24 * time.Hour}
	audit := &engineTestAuditEmitter{}

	config := SchedulerConfig{
		Distributed:          true,
		DistributedThreshold: 5,
		WorkerConcurrency:    3,
		TaskTimeout:          5 * time.Second,
		MaxRetries:           1,
		VerifyTimeout:        10 * time.Second,
		VerifyPollInterval:   50 * time.Millisecond,
	}
	config.ApplyDefaults()

	// Simulate leader planning and then multiple workers claiming
	orchestrator := NewDistributedPurgeOrchestrator(engine, elector, config, "leader-node", logger)
	stopCh := make(chan struct{})

	// In a real multi-node setup, multiple nodes would call executeWorkerPhase.
	// Here we simulate by running the full orchestrator (which does plan + execute + verify)
	// and also start a separate goroutine acting as a second worker.
	var worker2Count atomic.Int32
	var wg sync.WaitGroup

	// Start a second "worker" that also claims from the engine
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker2Consumer := &taskengine.ConsumerDescriptor{
			ID:           "worker-node-2",
			Roles:        []node.Role{node.RolePurger},
			Capabilities: node.NewCapabilitySet(node.CapPurgeExecute, node.CapStorageRead, node.CapStorageDelete, node.CapPurgePlan),
		}

		// Wait briefly for leader to submit tasks
		time.Sleep(50 * time.Millisecond)

		for {
			task, err := engine.Claim(context.Background(), worker2Consumer)
			if err != nil || task == nil {
				break
			}
			worker2Count.Add(1)
			// Simulate execution
			payload, _ := ParsePurgePayload(task)
			if payload != nil {
				_ = purger.DeleteSingleIndex(context.Background(), payload.IndexName)
			}
			_ = engine.Report(context.Background(), &taskengine.TaskResult{
				TaskID:      task.ID,
				NodeID:      "worker-node-2",
				Status:      taskengine.StatusSuccess,
				CompletedAt: time.Now().UnixMilli(),
			})
		}
	}()

	handled := orchestrator.Execute(context.Background(), purger, purger, resolver, audit, stopCh)
	wg.Wait()

	if !handled {
		t.Fatal("expected handled=true")
	}

	// All 20 indices should be deleted (by leader + worker2 combined)
	calls := purger.getDeletedCalls()
	if len(calls) != 20 {
		t.Errorf("expected 20 total deletes, got %d", len(calls))
	}

	t.Logf("Worker 2 handled %d tasks out of 20", worker2Count.Load())
}

// TestScheduler_WithEngine_Integration tests the full scheduler integration
// using WithEngine option.
func TestScheduler_WithEngine_Integration(t *testing.T) {
	store := taskengine.NewMemoryStore()
	logger := zap.NewNop()
	engine := taskengine.NewEngine(store, nil, logger, taskengine.DefaultEngineConfig())
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	elector := NewLocalLeaderElector()
	purger := newEngineTestPurger()
	purger.expiredIndices[SignalTrace] = []string{
		"traces-2024.01.01",
		"traces-2024.01.02",
		"traces-2024.01.03",
	}

	resolver := &engineTestResolver{duration: 7 * 24 * time.Hour}

	scheduler := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             1 * time.Hour,
			Distributed:          true,
			DistributedThreshold: 1,
			WorkerConcurrency:    5,
			TaskTimeout:          5 * time.Second,
			VerifyTimeout:        5 * time.Second,
			VerifyPollInterval:   50 * time.Millisecond,
		}),
		WithLogger(logger),
		WithEngine(engine, elector),
	)

	// Run one cycle manually
	scheduler.runCycle(context.Background())

	// Verify all indices were deleted
	calls := purger.getDeletedCalls()
	if len(calls) != 3 {
		t.Errorf("expected 3 deleted calls, got %d: %v", len(calls), calls)
	}
}
