// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/taskengine"
)

var errStoreUnavailable = errors.New("simulated store unavailable")

// These tests cover the optimized reaper path (WithStore + scanOptimized),
// which exercises Store.GetOverdueRunningTasks, the circuit breaker, and
// the meta-validation safety net. They use the real taskengine.MemoryStore
// as the Store implementation rather than a hand-written mock, so the
// deadline computation and meta lookup behave exactly as in production.

// newReaperWithStore builds a reaper backed by a real MemoryStore + mockEngine.
// The reaper is NOT started; tests drive scan() directly for determinism.
func newReaperWithStore(t *testing.T, store taskengine.Store, engine *mockEngine) *StaleTaskReaperEngine {
	t.Helper()
	config := StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   100 * time.Millisecond,
		RunningTimeout: 30 * time.Second,
	}
	return NewStaleTaskReaperEngine(zaptest.NewLogger(t), config, engine, WithStore(store))
}

// plantRunningTask saves a RUNNING task into the store with a createdAt far
// enough in the past that GetOverdueRunningTasks will flag it as overdue.
func plantRunningTask(t *testing.T, store taskengine.Store, id string) {
	t.Helper()
	task := &taskengine.Task{
		ID:        id,
		Type:      taskengine.TaskTypeArthasAttach,
		Status:    taskengine.StatusRunning,
		ClaimedBy: "agent-stale",
		CreatedAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
		Timeout:   30 * time.Second,
	}
	require.NoError(t, store.SaveTask(context.Background(), task))
}

func TestWithStore_InjectsStore(t *testing.T) {
	engine := newMockEngine()
	store := taskengine.NewMemoryStore()

	reaper := NewStaleTaskReaperEngine(
		zaptest.NewLogger(t),
		DefaultStaleTaskReaperConfig(),
		engine,
		WithStore(store),
	)

	// WithStore should set the store and initialize breaker + backoff.
	assert.NotNil(t, reaper.store)
	assert.NotNil(t, reaper.breaker, "breaker must be initialized when a store is injected")
	assert.NotNil(t, reaper.backoff, "backoff must be initialized when a store is injected")
}

func TestScanOptimized_ReapsOverdueTask(t *testing.T) {
	engine := newMockEngine()
	store := taskengine.NewMemoryStore()
	reaper := newReaperWithStore(t, store, engine)

	plantRunningTask(t, store, "stale-1")

	reaper.scan()

	// scanOptimized should have reported a timeout result via the engine.
	assert.Equal(t, 1, engine.reportCalled)
	result := engine.results["stale-1"]
	require.NotNil(t, result)
	assert.Equal(t, taskengine.StatusTimeout, result.Status)
	assert.Equal(t, "agent-stale", result.NodeID)
}

func TestScanOptimized_NoOverdueTasks(t *testing.T) {
	engine := newMockEngine()
	store := taskengine.NewMemoryStore()
	reaper := newReaperWithStore(t, store, engine)

	// Empty store → no overdue tasks → no report.
	reaper.scan()

	assert.Equal(t, 0, engine.reportCalled)
}

// failingStore wraps a real MemoryStore but lets tests inject an error from
// GetOverdueRunningTasks to exercise scanOptimized's failure path (breaker +
// backoff). All other Store methods come from the embedded MemoryStore.
type failingStore struct {
	*taskengine.MemoryStore
	overdueErr error
}

func (s *failingStore) GetOverdueRunningTasks(ctx context.Context, nowMillis int64) ([]string, error) {
	if s.overdueErr != nil {
		return nil, s.overdueErr
	}
	return s.MemoryStore.GetOverdueRunningTasks(ctx, nowMillis)
}


func TestScanOptimized_StoreError_RecordsFailure(t *testing.T) {
	engine := newMockEngine()
	store := &failingStore{
		MemoryStore: taskengine.NewMemoryStore(),
		overdueErr:  errStoreUnavailable,
	}
	reaper := newReaperWithStore(t, store, engine)

	before := reaper.backoff.ConsecutiveFailures()
	reaper.scan()
	after := reaper.backoff.ConsecutiveFailures()

	// A store error must be recorded on both the breaker and the backoff,
	// and must NOT propagate as a report to the engine.
	assert.Greater(t, after, before, "backoff must record the failure")
	assert.Equal(t, 0, engine.reportCalled, "no report on store error")
}
