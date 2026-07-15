// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/taskengine"
)

// StaleTaskReaperConfig configures the stale task reaper.
type StaleTaskReaperConfig struct {
	// Enabled controls whether the reaper is active.
	Enabled bool `mapstructure:"enabled"`

	// ScanInterval is how often the reaper scans for stale tasks.
	// Default: 30s
	ScanInterval time.Duration `mapstructure:"scan_interval"`

	// RunningTimeout is the default timeout for a RUNNING task before being
	// considered stale and marked as TIMEOUT. Default: 120s.
	// If a task has its own TimeoutMillis, that value is used instead
	// (whichever is larger).
	RunningTimeout time.Duration `mapstructure:"running_timeout"`
}

// DefaultStaleTaskReaperConfig returns the default reaper configuration.
func DefaultStaleTaskReaperConfig() StaleTaskReaperConfig {
	return StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   30 * time.Second,
		RunningTimeout: 120 * time.Second,
	}
}

// StaleTaskReaperEngine periodically scans for stale RUNNING tasks in the
// unified task engine and marks them as TIMEOUT.
//
// Architecture (v2 — optimized):
//   - Uses Store.GetOverdueRunningTasks for O(logN+K) deadline-indexed queries
//   - Falls back to Engine.ListTasks when Store is nil (backward compat)
//   - Circuit Breaker isolates Reaper from Redis failures
//   - Exponential backoff eliminates log floods on sustained failures
type StaleTaskReaperEngine struct {
	logger *zap.Logger
	config StaleTaskReaperConfig
	engine taskengine.Engine
	store  taskengine.Store // optional: enables optimized path

	breaker *taskengine.CircuitBreaker
	backoff *taskengine.Backoff

	mu       sync.Mutex
	stopChan chan struct{}
	doneChan chan struct{}
	started  bool
	stopped  bool
}

// NewStaleTaskReaperEngine creates a new engine-backed stale task reaper.
// The store parameter is optional — if nil, the reaper falls back to
// the legacy ListTasks path (no optimization, no circuit breaker).
func NewStaleTaskReaperEngine(logger *zap.Logger, config StaleTaskReaperConfig, engine taskengine.Engine, opts ...ReaperOption) *StaleTaskReaperEngine {
	r := &StaleTaskReaperEngine{
		logger:   logger,
		config:   config,
		engine:   engine,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	// Initialize circuit breaker and backoff if store is provided (optimized path).
	if r.store != nil {
		r.breaker = taskengine.NewCircuitBreaker(taskengine.DefaultCircuitBreakerConfig())
		r.backoff = taskengine.NewBackoff(taskengine.DefaultBackoffConfig())
	}
	return r
}

// ReaperOption configures optional dependencies for the reaper.
type ReaperOption func(*StaleTaskReaperEngine)

// WithStore injects a Store for the optimized GetOverdueRunningTasks path.
func WithStore(store taskengine.Store) ReaperOption {
	return func(r *StaleTaskReaperEngine) {
		r.store = store
	}
}

// Start begins the periodic scan loop.
func (r *StaleTaskReaperEngine) Start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	if !r.config.Enabled {
		r.logger.Info("Engine stale task reaper is disabled")
		close(r.doneChan)
		r.started = true
		r.stopped = true
		return nil
	}

	r.logger.Info("Starting engine stale task reaper",
		zap.Duration("scan_interval", r.config.ScanInterval),
		zap.Duration("running_timeout", r.config.RunningTimeout),
	)

	r.started = true
	go r.run()
	return nil
}

// Stop stops the reaper gracefully.
func (r *StaleTaskReaperEngine) Stop() {
	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()

	close(r.stopChan)
	<-r.doneChan
}

func (r *StaleTaskReaperEngine) run() {
	defer close(r.doneChan)

	ticker := time.NewTicker(r.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			r.logger.Info("Engine stale task reaper stopped")
			return
		case <-ticker.C:
			r.scan()
		}
	}
}

func (r *StaleTaskReaperEngine) scan() {
	// Use optimized path if store is available, otherwise fall back to legacy.
	if r.store != nil {
		r.scanOptimized()
	} else {
		r.scanLegacy()
	}
}

// scanOptimized uses Store.GetOverdueRunningTasks (ZRANGEBYSCORE, O(logN+K), 0 decode)
// with Circuit Breaker and exponential backoff for fault isolation.
func (r *StaleTaskReaperEngine) scanOptimized() {
	// Backoff check: skip if we're in a backoff window.
	if r.backoff.ShouldWait() {
		return
	}

	// Circuit breaker check: skip if circuit is open.
	if !r.breaker.Allow() {
		r.logger.Debug("Reaper circuit breaker is open, skipping scan",
			zap.String("state", r.breaker.State().String()),
		)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nowMillis := time.Now().UnixMilli()
	staleTaskIDs, err := r.store.GetOverdueRunningTasks(ctx, nowMillis)
	if err != nil {
		r.breaker.RecordFailure()
		r.backoff.RecordFailure()
		r.logger.Warn("Reaper: failed to get overdue running tasks",
			zap.Error(err),
			zap.String("breaker_state", r.breaker.State().String()),
			zap.Int("consecutive_failures", r.backoff.ConsecutiveFailures()),
		)
		return
	}
	r.breaker.RecordSuccess()
	r.backoff.RecordSuccess()

	if len(staleTaskIDs) == 0 {
		return
	}

	reaped := 0
	for _, taskID := range staleTaskIDs {
		// Validate: fetch meta to confirm the task is still running (Safety-Net).
		// This handles index/meta inconsistency gracefully.
		meta, err := r.store.GetTaskMeta(ctx, taskID)
		if err != nil {
			r.logger.Debug("Reaper: failed to get task meta for validation",
				zap.String("task_id", taskID), zap.Error(err))
			continue
		}
		if meta == nil || meta.Status != taskengine.StatusRunning {
			// Index stale — task already transitioned. This is expected.
			// Cleanup will happen naturally via UpdateTaskStatus ZREM.
			continue
		}

		r.logger.Warn("Stale RUNNING task detected, marking as TIMEOUT",
			zap.String("task_id", taskID),
			zap.String("claimed_by", meta.ClaimedBy),
			zap.Int64("created_at", meta.CreatedAt),
		)

		timeoutResult := &taskengine.TaskResult{
			TaskID:      taskID,
			NodeID:      meta.ClaimedBy,
			Status:      taskengine.StatusTimeout,
			Error:       "task stuck in RUNNING state, marked as TIMEOUT by reaper",
			CompletedAt: nowMillis,
		}

		if err := r.engine.Report(ctx, timeoutResult); err != nil {
			r.logger.Warn("Failed to report timeout for stale task",
				zap.String("task_id", taskID), zap.Error(err))
			continue
		}
		reaped++
	}

	if reaped > 0 {
		r.logger.Info("Reaper: reaped stale tasks", zap.Int("reaped_count", reaped))
	}
}

// scanLegacy is the original implementation using Engine.ListTasks.
// Kept for backward compatibility when Store is not injected.
func (r *StaleTaskReaperEngine) scanLegacy() {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.ScanInterval)
	defer cancel()

	page, err := r.engine.ListTasks(ctx, taskengine.ListQuery{
		Status: taskengine.StatusRunning,
		Limit:  1000,
	})
	if err != nil {
		r.logger.Warn("Engine stale reaper: failed to list running tasks", zap.Error(err))
		return
	}
	if page == nil || len(page.Tasks) == 0 {
		return
	}

	nowMillis := time.Now().UnixMilli()
	reaped := 0

	for _, task := range page.Tasks {
		if task.Status != taskengine.StatusRunning {
			continue
		}

		timeout := r.config.RunningTimeout
		if task.Timeout > 0 && task.Timeout > timeout {
			timeout = task.Timeout
		}

		// 2x grace period for network delays and clock skew
		staleThreshold := timeout * 2

		startedAt := task.CreatedAt
		if startedAt == 0 {
			continue
		}

		elapsed := time.Duration(nowMillis-startedAt) * time.Millisecond
		if elapsed < staleThreshold {
			continue
		}

		r.logger.Warn("Stale RUNNING task detected, marking as TIMEOUT via engine",
			zap.String("task_id", task.ID),
			zap.String("claimed_by", task.ClaimedBy),
			zap.Duration("elapsed", elapsed),
			zap.Duration("threshold", staleThreshold),
		)

		timeoutResult := &taskengine.TaskResult{
			TaskID:      task.ID,
			NodeID:      task.ClaimedBy,
			Status:      taskengine.StatusTimeout,
			Error:       "task stuck in RUNNING state, marked as TIMEOUT by reaper",
			CompletedAt: nowMillis,
		}

		if err := r.engine.Report(ctx, timeoutResult); err != nil {
			r.logger.Warn("Failed to report timeout for stale task",
				zap.String("task_id", task.ID), zap.Error(err))
			continue
		}
		reaped++
	}

	if reaped > 0 {
		r.logger.Info("Engine stale task reaper: reaped tasks", zap.Int("reaped_count", reaped))
	}
}
