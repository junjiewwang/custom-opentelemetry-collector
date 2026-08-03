// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newAutoDetachCompat builds a local-mode compat with auto_detach enabled and a
// tiny sweep configuration, wired to the given task submitter.
func newAutoDetachCompat(t *testing.T, submitter *fakeTaskSubmitter) *arthasURICompat {
	t.Helper()
	cfg := createDefaultConfig()
	cfg.AutoDetach.IdleThreshold = 50 * time.Millisecond
	cfg.AutoDetach.SweepInterval = 20 * time.Millisecond
	cfg.AutoDetach.MinRegisterAge = 0
	cfg.AutoDetach.Cooldown = 0
	cfg.AutoDetach.RequireNoPending = false
	cfg.AutoDetach.MaxTasksPerSweep = 10
	// Start with a healthy liveness window so injected agents are not timed out.
	cfg.PongTimeout = 60 * time.Second
	cfg.LivenessGrace = 30 * time.Second
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, submitter)
	t.Cleanup(func() { s.shutdown(context.Background()) })
	return s
}

// idleAgent injects an agent whose lastActivityAt is far in the past, making it
// eligible for auto-detach. Returns the agent for field tweaking.
func idleAgent(t *testing.T, s *arthasURICompat, agentID string) *compatAgent {
	t.Helper()
	cc := injectAgent(t, s, agentID)
	t.Cleanup(func() { _ = cc.Close() })
	s.mu.Lock()
	a := s.agents[agentID]
	a.lastActivityAt = time.Now().Add(-1 * time.Hour).UnixMilli()
	s.mu.Unlock()
	return a
}

func TestRunAutoDetachSweep_SubmitsForIdleAgent(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	a := idleAgent(t, s, "idle-1")
	// Run the sweep directly.
	s.runAutoDetachSweep()

	require.Equal(t, 1, submitter.count(), "one detach task should be submitted")
	task := submitter.tasks[0]
	assert.Equal(t, "idle-1", task.TargetAgentID)
	assert.Equal(t, "arthas_detach", task.TypeName)
	// lastAutoDetachAt stamped.
	assert.Greater(t, a.lastAutoDetachAt, int64(0))
}

func TestRunAutoDetachSweep_SkipsActiveTunnel(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	a := idleAgent(t, s, "busy-1")
	atomic.StoreInt64(&a.activeTunnels, 1)

	s.runAutoDetachSweep()
	assert.Equal(t, 0, submitter.count(), "agent with active tunnel must be skipped")
}

func TestRunAutoDetachSweep_SkipsRecentActivity(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	a := idleAgent(t, s, "fresh-1")
	// Override lastActivityAt to "now" (not idle).
	a.lastActivityAt = time.Now().UnixMilli()

	s.runAutoDetachSweep()
	assert.Equal(t, 0, submitter.count(), "recently-active agent must be skipped")
}

func TestRunAutoDetachSweep_RespectsCooldown(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	s.cfg.AutoDetach.Cooldown = 1 * time.Hour
	a := idleAgent(t, s, "cool-1")
	atomic.StoreInt64(&a.lastAutoDetachAt, time.Now().UnixMilli()) // detached moments ago

	s.runAutoDetachSweep()
	assert.Equal(t, 0, submitter.count(), "agent within cooldown must be skipped")
}

func TestRunAutoDetachSweep_RespectsMinRegisterAge(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	s.cfg.AutoDetach.MinRegisterAge = 1 * time.Hour
	a := idleAgent(t, s, "young-1")
	// connectedAt is "now" (just injected) → below MinRegisterAge.
	_ = a

	s.runAutoDetachSweep()
	assert.Equal(t, 0, submitter.count(), "cold-start agent must be skipped")
}

func TestRunAutoDetachSweep_RespectsMaxTasksPerSweep(t *testing.T) {
	submitter := &fakeTaskSubmitter{}
	s := newAutoDetachCompat(t, submitter)
	s.cfg.AutoDetach.MaxTasksPerSweep = 2
	for _, id := range []string{"a", "b", "c"} {
		idleAgent(t, s, id)
	}

	s.runAutoDetachSweep()
	assert.Equal(t, 2, submitter.count(), "must cap submissions at MaxTasksPerSweep")
}

func TestSubmitAutoDetachTask_NilSubmitter(t *testing.T) {
	s := newLocalCompat(t) // no submitter
	a := idleAgent(t, s, "x")
	err := s.submitAutoDetachTask("x", a, time.Now().UnixMilli(), a.lastActivityAt)
	assert.Error(t, err)
}

func TestStartAutoDetachLoop_DisabledNoSweeps(t *testing.T) {
	// With AutoDetach disabled, startAutoDetachLoop starts no goroutine and
	// runAutoDetachSweep is an early no-op even with a submitter wired.
	cfg := createDefaultConfig()
	cfg.AutoDetach.Enabled = false
	submitter := &fakeTaskSubmitter{}
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, submitter)
	t.Cleanup(func() { s.shutdown(context.Background()) })
	idleAgent(t, s, "idle")

	// Give the (non-existent) background loop a moment, then sweep directly.
	time.Sleep(50 * time.Millisecond)
	s.runAutoDetachSweep()
	assert.Equal(t, 0, submitter.count(), "disabled auto_detach must submit nothing")
}

func TestStartAutoDetachLoop_EventuallySweeps(t *testing.T) {
	// Construct with a tiny sweep interval + submitter so the background loop
	// fires runAutoDetachSweep and submits a task without us calling it directly.
	cfg := createDefaultConfig()
	cfg.AutoDetach.IdleThreshold = 50 * time.Millisecond
	cfg.AutoDetach.SweepInterval = 20 * time.Millisecond
	cfg.AutoDetach.MinRegisterAge = 0
	cfg.AutoDetach.Cooldown = 0
	cfg.AutoDetach.RequireNoPending = false
	cfg.AutoDetach.MaxTasksPerSweep = 10
	cfg.PongTimeout = 60 * time.Second
	cfg.LivenessGrace = 30 * time.Second
	submitter := &fakeTaskSubmitter{}
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, submitter)
	t.Cleanup(func() { s.shutdown(context.Background()) })
	idleAgent(t, s, "idle-bg")

	require.Eventually(t, func() bool { return submitter.count() >= 1 },
		2*time.Second, 10*time.Millisecond, "background sweep must submit a detach task")
}
