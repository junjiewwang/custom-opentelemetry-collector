// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Mock implementations for distributed purge testing
// ═══════════════════════════════════════════════════

// mockDistPurger implements LifecyclePurger + IndexLister + SingleIndexPurger
// for testing the distributed purge pipeline.
type mockDistPurger struct {
	mu             sync.Mutex
	expiredIndices map[SignalType][]string
	deletedIndices []string
	deleteErr      error
}

var _ LifecyclePurger = (*mockDistPurger)(nil)
var _ IndexLister = (*mockDistPurger)(nil)
var _ SingleIndexPurger = (*mockDistPurger)(nil)

func newMockDistPurger(expired map[SignalType][]string) *mockDistPurger {
	return &mockDistPurger{
		expiredIndices: expired,
	}
}

func (m *mockDistPurger) PurgeExpired(_ context.Context, signal SignalType, _ time.Time) (*PurgeResult, error) {
	return &PurgeResult{Signal: signal, DeletedDocs: 0, DeletedUnits: 0}, nil
}

func (m *mockDistPurger) PurgeByApp(_ context.Context, _ string, signal SignalType, _ time.Time) (*PurgeResult, error) {
	return &PurgeResult{Signal: signal}, nil
}

func (m *mockDistPurger) EstimatePurge(_ context.Context, signal SignalType, _ time.Time) (*PurgeEstimate, error) {
	return &PurgeEstimate{Signal: signal}, nil
}

func (m *mockDistPurger) GetDataBoundary(_ context.Context, signal SignalType) (*DataBoundary, error) {
	return &DataBoundary{Signal: signal, IsEmpty: true}, nil
}

func (m *mockDistPurger) ListExpired(_ context.Context, signal SignalType, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expiredIndices[signal], nil
}

func (m *mockDistPurger) DeleteSingleIndex(_ context.Context, indexName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedIndices = append(m.deletedIndices, indexName)
	return nil
}

func (m *mockDistPurger) GetDeletedIndices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.deletedIndices))
	copy(result, m.deletedIndices)
	return result
}

// mockDistResolver implements RetentionResolver for tests.
type mockDistResolver struct {
	retention time.Duration
}

func (r *mockDistResolver) Resolve(_ context.Context, _ SignalType, _ string) (EffectiveRetention, error) {
	return EffectiveRetention{Duration: r.retention, Source: SourcePlatformDefault}, nil
}

func (r *mockDistResolver) ResolveAll(_ context.Context, _ string) (map[SignalType]EffectiveRetention, error) {
	result := make(map[SignalType]EffectiveRetention)
	for _, s := range AllSignals() {
		result[s] = EffectiveRetention{Duration: r.retention, Source: SourcePlatformDefault}
	}
	return result, nil
}

// mockAuditCollector collects audit events for assertion.
type mockAuditCollector struct {
	mu     sync.Mutex
	events []LifecycleEvent
}

func (a *mockAuditCollector) Emit(_ context.Context, event LifecycleEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *mockAuditCollector) GetEvents() []LifecycleEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]LifecycleEvent, len(a.events))
	copy(result, a.events)
	return result
}

// ═══════════════════════════════════════════════════
// Test Cases
// ═══════════════════════════════════════════════════

func TestDistributedPurge_FullPipeline(t *testing.T) {
	// Setup: 60 expired trace indices (above threshold of 50)
	expired := make([]string, 60)
	for i := range expired {
		expired[i] = fmt.Sprintf("otel-traces-2026.01.%02d", i+1)
	}

	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace: expired,
	})
	audit := &mockAuditCollector{}
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithAuditEmitter(audit),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 50, // 60 > 50, will trigger distributed
			WorkerConcurrency:    5,
			TaskTimeout:          10 * time.Second,
			MaxRetries:           3,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	// Run one distributed purge cycle
	ctx := context.Background()
	s.distributedPurge(ctx)

	// Verify all 60 indices were deleted
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 60 {
		t.Errorf("expected 60 indices deleted, got %d", len(deleted))
	}

	// Verify audit events
	events := audit.GetEvents()
	hasPlan := false
	hasVerify := false
	for _, e := range events {
		if e.Action == ActionDistPlan {
			hasPlan = true
		}
		if e.Action == ActionDistVerify {
			hasVerify = true
		}
	}
	if !hasPlan {
		t.Error("expected ActionDistPlan audit event")
	}
	if !hasVerify {
		t.Error("expected ActionDistVerify audit event")
	}
}

func TestDistributedPurge_BelowThreshold_FallsBackToSingleNode(t *testing.T) {
	// Setup: Only 10 expired indices (below threshold of 50)
	expired := make([]string, 10)
	for i := range expired {
		expired[i] = fmt.Sprintf("otel-traces-2026.01.%02d", i+1)
	}

	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace: expired,
	})
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 50, // 10 < 50, should fall back
			WorkerConcurrency:    5,
			TaskTimeout:          10 * time.Second,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.distributedPurge(ctx)

	// In single-node fallback, DeleteSingleIndex won't be called
	// (purgeSignal uses PurgeExpired instead)
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 0 {
		t.Errorf("expected 0 DeleteSingleIndex calls in fallback mode, got %d", len(deleted))
	}
}

func TestDistributedPurge_PurgerWithoutIndexLister_FallsBack(t *testing.T) {
	// Use a purger that does NOT implement IndexLister/SingleIndexPurger
	simplePurger := &simpleTestPurger{}
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(simplePurger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 1,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	// Should not panic, should gracefully fall back
	ctx := context.Background()
	s.distributedPurge(ctx)
}

// simpleTestPurger only implements LifecyclePurger (no IndexLister/SingleIndexPurger)
type simpleTestPurger struct{}

func (p *simpleTestPurger) PurgeExpired(_ context.Context, signal SignalType, _ time.Time) (*PurgeResult, error) {
	return &PurgeResult{Signal: signal}, nil
}
func (p *simpleTestPurger) PurgeByApp(_ context.Context, _ string, signal SignalType, _ time.Time) (*PurgeResult, error) {
	return &PurgeResult{Signal: signal}, nil
}
func (p *simpleTestPurger) EstimatePurge(_ context.Context, signal SignalType, _ time.Time) (*PurgeEstimate, error) {
	return &PurgeEstimate{Signal: signal}, nil
}
func (p *simpleTestPurger) GetDataBoundary(_ context.Context, signal SignalType) (*DataBoundary, error) {
	return &DataBoundary{Signal: signal, IsEmpty: true}, nil
}

func TestDistributedPurge_DeleteFailure_ReportsError(t *testing.T) {
	// Setup: Some indices that will fail deletion
	expired := []string{"idx1", "idx2", "idx3"}

	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace: expired,
	})
	purger.deleteErr = fmt.Errorf("ES connection refused")

	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 1, // force distributed mode
			WorkerConcurrency:    2,
			TaskTimeout:          5 * time.Second,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.distributedPurge(ctx)

	// All tasks should have been attempted but failed
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 0 {
		t.Errorf("expected 0 successful deletions, got %d", len(deleted))
	}
}

func TestDistributedPurge_ConcurrentWorkers(t *testing.T) {
	// Setup: 200 expired indices across multiple signals
	traceIndices := make([]string, 80)
	for i := range traceIndices {
		traceIndices[i] = fmt.Sprintf("otel-traces-%04d", i)
	}
	logIndices := make([]string, 70)
	for i := range logIndices {
		logIndices[i] = fmt.Sprintf("otel-logs-%04d", i)
	}
	metricIndices := make([]string, 50)
	for i := range metricIndices {
		metricIndices[i] = fmt.Sprintf("otel-metrics-%04d", i)
	}

	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace:  traceIndices,
		SignalLog:    logIndices,
		SignalMetric: metricIndices,
	})
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 50, // 200 > 50
			WorkerConcurrency:    20, // High concurrency
			TaskTimeout:          10 * time.Second,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.distributedPurge(ctx)

	// Verify all 200 indices were deleted
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 200 {
		t.Errorf("expected 200 indices deleted, got %d", len(deleted))
	}
}

func TestDistributedPurge_NoExpiredData(t *testing.T) {
	purger := newMockDistPurger(map[SignalType][]string{})
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 1,
			WorkerConcurrency:    5,
			TaskTimeout:          5 * time.Second,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.distributedPurge(ctx)

	// No deletions
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 0 {
		t.Errorf("expected 0 deletions, got %d", len(deleted))
	}
}

func TestScheduler_RunCycle_RoutesToDistributed(t *testing.T) {
	// Verify runCycle routes to distributedPurge when coordinator is set
	expired := make([]string, 60)
	for i := range expired {
		expired[i] = fmt.Sprintf("otel-traces-%04d", i)
	}

	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace: expired,
	})
	coordinator := NewLocalCoordinator()

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			DistributedThreshold: 50,
			WorkerConcurrency:    5,
			TaskTimeout:          10 * time.Second,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.runCycle(ctx)

	// If distributed mode worked, DeleteSingleIndex should have been called
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 60 {
		t.Errorf("expected 60 indices deleted via distributed mode, got %d", len(deleted))
	}
}

func TestScheduler_RunCycle_SingleNodeWhenNoCoordinator(t *testing.T) {
	// Without coordinator, should use single-node purge (purgeSignal)
	purger := newMockDistPurger(map[SignalType][]string{
		SignalTrace: {"idx1", "idx2"},
	})

	s := NewScheduler(
		WithResolver(&mockDistResolver{retention: 7 * 24 * time.Hour}),
		WithPurger(purger),
		// No WithCoordinator — single-node mode
		WithConfig(SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true, // even with Distributed=true, no coordinator → single-node
			DistributedThreshold: 1,
			NodeID:               "test-node-1",
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	ctx := context.Background()
	s.runCycle(ctx)

	// In single-node mode, DeleteSingleIndex is NOT called (PurgeExpired is used)
	deleted := purger.GetDeletedIndices()
	if len(deleted) != 0 {
		t.Errorf("expected 0 DeleteSingleIndex calls in single-node mode, got %d", len(deleted))
	}
}

func TestWithCoordinator_Option(t *testing.T) {
	coordinator := NewLocalCoordinator()
	s := NewScheduler(
		WithCoordinator(coordinator),
		WithConfig(SchedulerConfig{Distributed: true}),
		WithLogger(zap.NewNop()),
	)

	if s.coordinator == nil {
		t.Fatal("expected coordinator to be set")
	}
}

func TestNewScheduler_NodeID_FromConfig(t *testing.T) {
	s := NewScheduler(
		WithConfig(SchedulerConfig{NodeID: "my-custom-node"}),
		WithLogger(zap.NewNop()),
	)

	if s.nodeID != "my-custom-node" {
		t.Errorf("expected nodeID=my-custom-node, got %s", s.nodeID)
	}
}

func TestNewScheduler_NodeID_AutoGenerated(t *testing.T) {
	s := NewScheduler(
		WithConfig(SchedulerConfig{}),
		WithLogger(zap.NewNop()),
	)

	if s.nodeID == "" {
		t.Error("expected auto-generated nodeID, got empty string")
	}
	if len(s.nodeID) < 5 {
		t.Errorf("expected meaningful nodeID, got %s", s.nodeID)
	}
}
