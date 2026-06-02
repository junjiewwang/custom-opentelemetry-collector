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

// StaleTaskReaperEngine periodically scans for stale RUNNING tasks in the
// unified task engine and marks them as TIMEOUT. It replaces the legacy
// StaleTaskReaper that operated directly on store.TaskStore.
//
// Key differences from the legacy reaper:
//   - Uses taskengine.Engine for task listing and result reporting
//   - Does not need direct store access (ClearRunning, RemoveFromQueue, etc.)
//   - The engine handles state machine transitions internally
//   - Simpler: just list running tasks, check timeout, report timeout result
type StaleTaskReaperEngine struct {
	logger *zap.Logger
	config StaleTaskReaperConfig
	engine taskengine.Engine

	mu       sync.Mutex
	stopChan chan struct{}
	doneChan chan struct{}
	started  bool
	stopped  bool
}

// NewStaleTaskReaperEngine creates a new engine-backed stale task reaper.
func NewStaleTaskReaperEngine(logger *zap.Logger, config StaleTaskReaperConfig, engine taskengine.Engine) *StaleTaskReaperEngine {
	return &StaleTaskReaperEngine{
		logger:   logger,
		config:   config,
		engine:   engine,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
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
	ctx, cancel := context.WithTimeout(context.Background(), r.config.ScanInterval)
	defer cancel()

	// List all RUNNING tasks from the engine
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

		// Determine timeout for this task
		timeout := r.config.RunningTimeout
		if task.Timeout > 0 && task.Timeout > timeout {
			timeout = task.Timeout
		}

		// Add a grace period (2x) to account for network delays and clock skew
		staleThreshold := timeout * 2

		// Determine when the task started running.
		// Use CreatedAt as fallback if no better indicator is available.
		startedAt := task.CreatedAt
		if startedAt == 0 {
			continue // Cannot determine age, skip
		}

		elapsed := time.Duration(nowMillis-startedAt) * time.Millisecond
		if elapsed < staleThreshold {
			continue
		}

		// Task is stale — report timeout result via engine
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
				zap.String("task_id", task.ID),
				zap.Error(err),
			)
			continue
		}
		reaped++
	}

	if reaped > 0 {
		r.logger.Info("Engine stale task reaper: reaped tasks", zap.Int("reaped_count", reaped))
	}
}
