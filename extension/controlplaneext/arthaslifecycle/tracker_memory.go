// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthaslifecycle

import (
	"context"
	"sort"
	"sync"
)

type memoryTracker struct {
	mu     sync.Mutex
	states map[string]State
	locks  map[string]int64
}

// NewMemoryTracker returns a single-process Tracker.
func NewMemoryTracker() Tracker {
	return &memoryTracker{
		states: make(map[string]State),
		locks:  make(map[string]int64),
	}
}

func (t *memoryTracker) get(agentID string) State {
	st, ok := t.states[agentID]
	if !ok {
		return State{AgentID: agentID, Runtime: RuntimeUnknown}
	}
	return st
}

func (t *memoryTracker) Touch(_ context.Context, agentID, source string, now int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(agentID)
	st.LastActivityAt = now
	st.LastActivitySource = source
	t.states[agentID] = st
	return nil
}

func (t *memoryTracker) SetRuntime(_ context.Context, agentID string, runtime RuntimeState, now int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(agentID)
	st.Runtime = runtime
	st.RuntimeObservedAt = now
	if runtime == RuntimeRunning && st.AttachedAt == 0 {
		st.AttachedAt = now
	}
	if runtime == RuntimeStopped {
		st.AttachedAt = 0
		st.ActiveSessions = 0
	}
	t.states[agentID] = st
	return nil
}

func (t *memoryTracker) ChangeSessions(_ context.Context, agentID string, delta int, now int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(agentID)
	st.ActiveSessions += delta
	if st.ActiveSessions < 0 {
		st.ActiveSessions = 0
	}
	st.LastActivityAt = now
	st.LastActivitySource = "session"
	t.states[agentID] = st
	return nil
}

func (t *memoryTracker) Reset(_ context.Context, agentID string, now int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(agentID)
	st.Runtime = RuntimeUnknown
	st.RuntimeObservedAt = now
	st.AttachedAt = 0
	st.ActiveSessions = 0
	st.LastActivityAt = now
	st.LastActivitySource = "agent_re_register"
	t.states[agentID] = st
	return nil
}

func (t *memoryTracker) Get(_ context.Context, agentID string) (State, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[agentID]
	return st, ok, nil
}

func (t *memoryTracker) ListIdle(_ context.Context, now, idleThresholdMillis int64, limit int) ([]State, error) {
	t.mu.Lock()
	candidates := make([]State, 0)
	for _, st := range t.states {
		if st.Runtime != RuntimeRunning || st.ActiveSessions > 0 || st.LastActivityAt <= 0 {
			continue
		}
		if now-st.LastActivityAt < idleThresholdMillis {
			continue
		}
		candidates = append(candidates, st)
	}
	t.mu.Unlock()

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastActivityAt < candidates[j].LastActivityAt
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (t *memoryTracker) TryAcquireDetach(_ context.Context, agentID string, cooldownMillis, now int64) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.locks[agentID]; ok && cooldownMillis > 0 && now-last < cooldownMillis {
		return false, nil
	}
	t.locks[agentID] = now
	st := t.get(agentID)
	st.LastDetachSubmittedAt = now
	t.states[agentID] = st
	return true, nil
}
