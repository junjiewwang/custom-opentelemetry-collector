// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthaslifecycle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

type fakeSubmitter struct {
	mu    sync.Mutex
	tasks []*model.Task
}

func (f *fakeSubmitter) SubmitTaskForAgent(_ context.Context, _ string, task *model.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	return nil
}

func (f *fakeSubmitter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks)
}

func newTestManager(cfg Config) (*Manager, *fakeSubmitter, Tracker) {
	tracker := NewMemoryTracker()
	submitter := &fakeSubmitter{}
	m := NewManager(cfg, tracker, submitter, func(string) bool { return true }, zap.NewNop())
	m.ctx = context.Background()
	return m, submitter, tracker
}

func reportSuccess(m *Manager, taskID string) {
	m.OnTaskReported(&model.TaskResult{
		TaskID: taskID,
		Status: model.TaskStatusSuccess,
	})
}

func TestIdleReclaim_SubmitsDetachWithoutTunnel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = time.Minute
	cfg.MinAttachAge = 0
	cfg.Cooldown = 0
	m, submitter, tracker := newTestManager(cfg)

	task := &model.Task{ID: "task-1", TypeName: model.ArthasTaskTypeExecSync, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-1", task)
	reportSuccess(m, task.ID)

	state, ok := m.Get("agent-1")
	require.True(t, ok)
	require.Equal(t, RuntimeRunning, state.Runtime)

	old := time.Now().Add(-2 * time.Minute).UnixMilli()
	require.NoError(t, tracker.Touch(context.Background(), "agent-1", "test", old))

	m.scan()
	require.Equal(t, 1, submitter.count())
	require.Equal(t, model.ArthasTaskTypeDetach, submitter.tasks[0].TypeName)
}

func TestIdleReclaim_SkipsUnknownRuntime(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = time.Minute
	cfg.MinAttachAge = 0
	m, submitter, tracker := newTestManager(cfg)

	old := time.Now().Add(-2 * time.Minute).UnixMilli()
	require.NoError(t, tracker.Touch(context.Background(), "agent-unknown", "test", old))

	m.scan()
	require.Equal(t, 0, submitter.count())
}

func TestIdleReclaim_SkipsActiveSession(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = time.Minute
	cfg.MinAttachAge = 0
	m, submitter, tracker := newTestManager(cfg)

	open := &model.Task{ID: "session-1", TypeName: model.ArthasTaskTypeSessionOpen, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-2", open)
	reportSuccess(m, open.ID)

	state, _ := m.Get("agent-2")
	require.Equal(t, 1, state.ActiveSessions)

	old := time.Now().Add(-2 * time.Minute).UnixMilli()
	require.NoError(t, tracker.Touch(context.Background(), "agent-2", "test", old))
	m.scan()
	require.Equal(t, 0, submitter.count())
}

func TestIdleReclaim_CooldownPreventsDuplicate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = time.Minute
	cfg.MinAttachAge = 0
	cfg.Cooldown = 10 * time.Minute
	m, submitter, tracker := newTestManager(cfg)

	task := &model.Task{ID: "task-2", TypeName: model.ArthasTaskTypeExecSync, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-3", task)
	reportSuccess(m, task.ID)

	old := time.Now().Add(-2 * time.Minute).UnixMilli()
	require.NoError(t, tracker.Touch(context.Background(), "agent-3", "test", old))
	m.scan()
	m.scan()
	require.Equal(t, 1, submitter.count())
}

func TestPayloadWithoutExplicitStatusMarksRuntimeRunning(t *testing.T) {
	cfg := DefaultConfig()
	m, _, _ := newTestManager(cfg)

	task := &model.Task{ID: "task-payload", TypeName: model.ArthasTaskTypeExecSync, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-payload", task)
	m.OnTaskReported(&model.TaskResult{
		TaskID:     task.ID,
		Status:     model.TaskStatusUnspecified,
		ResultJSON: []byte(`{"command":"version","results":[]}`),
	})

	state, ok := m.Get("agent-payload")
	require.True(t, ok)
	require.Equal(t, RuntimeRunning, state.Runtime)
}

func TestDetachResultSetsRuntimeStopped(t *testing.T) {
	cfg := DefaultConfig()
	m, _, _ := newTestManager(cfg)

	task := &model.Task{ID: "detach-1", TypeName: model.ArthasTaskTypeDetach, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-4", task)
	reportSuccess(m, task.ID)

	state, ok := m.Get("agent-4")
	require.True(t, ok)
	require.Equal(t, RuntimeStopped, state.Runtime)
}

func TestResetClearsRuntimeObservation(t *testing.T) {
	cfg := DefaultConfig()
	m, _, _ := newTestManager(cfg)

	task := &model.Task{ID: "task-3", TypeName: model.ArthasTaskTypeExecSync, TimeoutMillis: 30_000}
	m.OnTaskSubmitted("agent-5", task)
	reportSuccess(m, task.ID)
	m.Reset("agent-5")

	state, _ := m.Get("agent-5")
	require.Equal(t, RuntimeUnknown, state.Runtime)
	require.Equal(t, 0, state.ActiveSessions)
}
