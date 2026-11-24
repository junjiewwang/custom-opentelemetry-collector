// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package arthaslifecycle owns the Control Plane's observation of Arthas
// runtime state and idle reclaim. It deliberately does not know about the
// Tunnel WebSocket extension: commands are submitted and reported through the
// Control Plane task chain, so activity and Runtime state are always visible.
package arthaslifecycle

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// TaskSubmitter is the subset of ControlPlane used to submit detach tasks.
type TaskSubmitter interface {
	SubmitTaskForAgent(ctx context.Context, agentID string, task *model.Task) error
}

type taskMeta struct {
	agentID   string
	typeName  string
	expiresAt int64
}

// Manager wires task hooks, lifecycle state and the idle sweeper together.
type Manager struct {
	cfg       Config
	tracker   Tracker
	submitter TaskSubmitter
	isOnline  func(agentID string) bool
	logger    *zap.Logger

	mu        sync.Mutex
	taskIndex map[string]taskMeta

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager creates a lifecycle manager.
func NewManager(cfg Config, tracker Tracker, submitter TaskSubmitter, isOnline func(string) bool, logger *zap.Logger) *Manager {
	if tracker == nil {
		tracker = NewMemoryTracker()
	}
	if isOnline == nil {
		isOnline = func(string) bool { return true }
	}
	return &Manager{
		cfg:       cfg,
		tracker:   tracker,
		submitter: submitter,
		isOnline:  isOnline,
		logger:    logger,
		taskIndex: make(map[string]taskMeta),
	}
}

// Start launches the idle sweeper. It is safe to call more than once.
func (m *Manager) Start(ctx context.Context) {
	if m == nil || !m.cfg.Enabled || m.ctx != nil {
		return
	}
	if err := m.cfg.Validate(); err != nil {
		m.logger.Warn("arthas_lifecycle disabled due to invalid config", zap.Error(err))
		return
	}
	if m.submitter == nil {
		m.logger.Warn("arthas_lifecycle disabled: no task submitter")
		return
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go m.loop()
	m.logger.Info("arthas_lifecycle idle reclaim enabled",
		zap.Duration("idle_threshold", m.cfg.IdleThreshold),
		zap.Duration("min_attach_age", m.cfg.MinAttachAge),
		zap.Duration("cooldown", m.cfg.Cooldown),
		zap.Duration("sweep_interval", m.cfg.SweepInterval),
	)
}

// Stop terminates the sweeper.
func (m *Manager) Stop() {
	if m == nil || m.cancel == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) loop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.scan()
		}
	}
}

// OnTaskSubmitted records activity and the task type for the later result.
func (m *Manager) OnTaskSubmitted(agentID string, task *model.Task) {
	if m == nil || task == nil || agentID == "" || !isArthasTask(task.TypeName) {
		return
	}
	now := time.Now().UnixMilli()
	ttl := task.TimeoutMillis
	if ttl <= 0 {
		ttl = int64(m.cfg.TaskTimeout / time.Millisecond)
	}
	m.mu.Lock()
	m.taskIndex[task.ID] = taskMeta{agentID: agentID, typeName: task.TypeName, expiresAt: now + ttl + 60_000}
	m.mu.Unlock()

	if err := m.tracker.Touch(context.Background(), agentID, "task:"+task.TypeName, now); err != nil {
		m.logger.Warn("arthas_lifecycle touch failed", zap.String("agent_id", agentID), zap.Error(err))
	}
}

// OnTaskReported updates Runtime state, session counters and activity.
func (m *Manager) OnTaskReported(result *model.TaskResult) {
	if m == nil || result == nil {
		return
	}
	m.mu.Lock()
	meta, ok := m.taskIndex[result.TaskID]
	delete(m.taskIndex, result.TaskID)
	m.mu.Unlock()
	if !ok {
		return
	}

	now := time.Now().UnixMilli()
	ctx := context.Background()
	agentID := meta.agentID
	if agentID == "" {
		agentID = result.AgentID
	}
	if agentID == "" {
		return
	}

	if err := m.tracker.Touch(ctx, agentID, "result:"+meta.typeName, now); err != nil {
		m.logger.Warn("arthas_lifecycle touch failed", zap.String("agent_id", agentID), zap.Error(err))
	}

	success := isSuccessfulResult(result)
	m.logger.Debug("arthas_lifecycle task result observed",
		zap.String("agent_id", agentID),
		zap.String("task_type", meta.typeName),
		zap.Int32("status", int32(result.Status)),
		zap.Bool("success", success),
	)

	switch meta.typeName {
	case model.ArthasTaskTypeSessionOpen:
		if success {
			_ = m.tracker.ChangeSessions(ctx, agentID, 1, now)
		}
	case model.ArthasTaskTypeSessionClose:
		_ = m.tracker.ChangeSessions(ctx, agentID, -1, now)
	case model.ArthasTaskTypeDetach:
		if success {
			_ = m.tracker.SetRuntime(ctx, agentID, RuntimeStopped, now)
		}
	default:
		if success {
			_ = m.tracker.SetRuntime(ctx, agentID, RuntimeRunning, now)
		} else if isNotRunningError(result.ErrorCode) {
			_ = m.tracker.SetRuntime(ctx, agentID, RuntimeStopped, now)
		}
	}
	// Session activity keeps the Runtime alive while a session is running.
	if isSessionCommand(meta.typeName) && success {
		_ = m.tracker.Touch(ctx, agentID, "session:"+meta.typeName, now)
	}
}

// RecordActivity records an external Arthas interaction (for example a Tunnel
// WebSocket session) so idle reclaim does not interrupt an active terminal.
func (m *Manager) RecordActivity(agentID, source string) {
	if m == nil || agentID == "" {
		return
	}
	if err := m.tracker.Touch(context.Background(), agentID, source, time.Now().UnixMilli()); err != nil {
		m.logger.Warn("arthas_lifecycle activity record failed", zap.String("agent_id", agentID), zap.Error(err))
	}
}

// Reset clears the Runtime observation when an Agent re-registers.
func (m *Manager) Reset(agentID string) {
	if m == nil || agentID == "" {
		return
	}
	if err := m.tracker.Reset(context.Background(), agentID, time.Now().UnixMilli()); err != nil {
		m.logger.Warn("arthas_lifecycle reset failed", zap.String("agent_id", agentID), zap.Error(err))
	}
}

// Get returns the current lifecycle observation.
func (m *Manager) Get(agentID string) (State, bool) {
	if m == nil || agentID == "" {
		return State{}, false
	}
	st, ok, err := m.tracker.Get(context.Background(), agentID)
	if err != nil {
		m.logger.Warn("arthas_lifecycle get failed", zap.String("agent_id", agentID), zap.Error(err))
		return State{}, false
	}
	if !ok {
		return State{AgentID: agentID, Runtime: RuntimeUnknown}, true
	}
	return st, true
}

func (m *Manager) scan() {
	now := time.Now().UnixMilli()
	ctx := m.ctx
	candidates, err := m.tracker.ListIdle(ctx, now, m.cfg.IdleThreshold.Milliseconds(), m.cfg.MaxTasksPerSweep)
	if err != nil {
		m.logger.Warn("arthas_lifecycle idle scan failed", zap.Error(err))
		return
	}

	submitted := 0
	for _, st := range candidates {
		if st.AgentID == "" || !m.isOnline(st.AgentID) {
			continue
		}
		if m.cfg.MinAttachAge > 0 && (st.AttachedAt <= 0 || now-st.AttachedAt < m.cfg.MinAttachAge.Milliseconds()) {
			continue
		}
		ok, err := m.tracker.TryAcquireDetach(ctx, st.AgentID, m.cfg.Cooldown.Milliseconds(), now)
		if err != nil || !ok {
			continue
		}
		task := buildDetachTask(st.AgentID, now, st.LastActivityAt, m.cfg.TaskTimeout)
		submitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = m.submitter.SubmitTaskForAgent(submitCtx, st.AgentID, task)
		cancel()
		if err != nil {
			m.logger.Warn("arthas_lifecycle detach submit failed",
				zap.String("agent_id", st.AgentID), zap.Error(err))
			continue
		}
		submitted++
	}
	if submitted > 0 {
		m.logger.Info("arthas_lifecycle detach tasks submitted", zap.Int("count", submitted))
	}
	m.pruneTaskIndex(now)
}

func (m *Manager) pruneTaskIndex(now int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, meta := range m.taskIndex {
		if meta.expiresAt <= now {
			delete(m.taskIndex, id)
		}
	}
}

func isArthasTask(typeName string) bool {
	switch typeName {
	case model.ArthasTaskTypeAttach,
		model.ArthasTaskTypeDetach,
		model.ArthasTaskTypeExecSync,
		model.ArthasTaskTypeSessionOpen,
		model.ArthasTaskTypeSessionExec,
		model.ArthasTaskTypeSessionPull,
		model.ArthasTaskTypeSessionInterrupt,
		model.ArthasTaskTypeSessionClose:
		return true
	default:
		return false
	}
}

func isSessionCommand(typeName string) bool {
	switch typeName {
	case model.ArthasTaskTypeSessionExec,
		model.ArthasTaskTypeSessionPull,
		model.ArthasTaskTypeSessionInterrupt:
		return true
	default:
		return false
	}
}

func isNotRunningError(code string) bool {
	return code == model.ArthasErrNotRunning || code == model.ArthasErrStopped
}

// isSuccessfulResult tolerates agents that report a payload without an explicit
// SUCCESS status. A failure/timeout/cancelled status or an Arthas error code
// always wins over the payload.
func isSuccessfulResult(result *model.TaskResult) bool {
	if result == nil {
		return false
	}
	switch result.Status {
	case model.TaskStatusSuccess:
		return true
	case model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled:
		return false
	}
	if result.ErrorCode != "" {
		return false
	}
	return len(result.ResultJSON) > 0 || len(result.ResultData) > 0
}

func buildDetachTask(agentID string, now, lastActivityAt int64, timeout time.Duration) *model.Task {
	params := map[string]any{
		"reason":          "control_plane_idle",
		"agent_id":        agentID,
		"idle_millis":     now - lastActivityAt,
		"last_activity_at": lastActivityAt,
	}
	b, _ := json.Marshal(params)
	return &model.Task{
		ID:              uuid.NewString(),
		TypeName:        model.ArthasTaskTypeDetach,
		TargetAgentID:   agentID,
		TimeoutMillis:   int64(timeout / time.Millisecond),
		CreatedAtMillis: now,
		ParametersJSON:  b,
	}
}
