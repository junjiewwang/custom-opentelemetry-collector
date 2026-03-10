// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
)

// StaleTaskReaperConfig holds configuration for the stale task reaper.
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

// StaleTaskReaper periodically scans for stale RUNNING tasks and marks them
// as TIMEOUT (terminal state). It does NOT requeue tasks — the upstream
// (control plane / user) decides whether to re-submit a timed-out task.
type StaleTaskReaper struct {
	logger *zap.Logger
	config StaleTaskReaperConfig
	store  store.TaskStore

	stopChan chan struct{}
	doneChan chan struct{}
}

// NewStaleTaskReaper creates a new StaleTaskReaper.
func NewStaleTaskReaper(logger *zap.Logger, config StaleTaskReaperConfig, taskStore store.TaskStore) *StaleTaskReaper {
	return &StaleTaskReaper{
		logger:   logger,
		config:   config,
		store:    taskStore,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

// Start begins the periodic scan loop.
func (r *StaleTaskReaper) Start(ctx context.Context) error {
	if !r.config.Enabled {
		r.logger.Info("Stale task reaper is disabled")
		close(r.doneChan)
		return nil
	}

	r.logger.Info("Starting stale task reaper",
		zap.Duration("scan_interval", r.config.ScanInterval),
		zap.Duration("running_timeout", r.config.RunningTimeout),
	)

	go r.run()
	return nil
}

// Stop stops the reaper gracefully.
func (r *StaleTaskReaper) Stop() {
	close(r.stopChan)
	<-r.doneChan
}

func (r *StaleTaskReaper) run() {
	defer close(r.doneChan)

	ticker := time.NewTicker(r.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			r.logger.Info("Stale task reaper stopped")
			return
		case <-ticker.C:
			r.scan()
		}
	}
}

func (r *StaleTaskReaper) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.ScanInterval)
	defer cancel()

	infos, err := r.store.ListTaskInfos(ctx)
	if err != nil {
		r.logger.Warn("Stale task reaper: failed to list tasks", zap.Error(err))
		return
	}

	nowMillis := time.Now().UnixMilli()
	reaped := 0

	for _, info := range infos {
		if info.Status != model.TaskStatusRunning {
			continue
		}

		// Determine timeout for this task
		timeout := r.config.RunningTimeout
		if info.Task != nil && info.Task.TimeoutMillis > 0 {
			taskTimeout := time.Duration(info.Task.TimeoutMillis) * time.Millisecond
			// Use the larger of task-level timeout and config default
			if taskTimeout > timeout {
				timeout = taskTimeout
			}
		}
		// Add a grace period (2x) to account for network delays and clock skew
		staleThreshold := timeout * 2

		// Determine when the task started running
		startedAt := info.StartedAtMillis
		if startedAt == 0 {
			startedAt = info.LastUpdatedAtMillis
		}
		if startedAt == 0 {
			startedAt = info.CreatedAtMillis
		}
		if startedAt == 0 {
			continue // Cannot determine age, skip
		}

		elapsed := time.Duration(nowMillis-startedAt) * time.Millisecond
		if elapsed < staleThreshold {
			continue
		}

		taskID := ""
		if info.Task != nil {
			taskID = info.Task.ID
		}
		if taskID == "" {
			continue
		}

		// Task is stale — mark as TIMEOUT
		r.logger.Warn("Stale RUNNING task detected, marking as TIMEOUT",
			zap.String("task_id", taskID),
			zap.String("agent_id", info.AgentID),
			zap.Duration("elapsed", elapsed),
			zap.Duration("threshold", staleThreshold),
		)

		if err := r.markTimeout(ctx, info, nowMillis); err != nil {
			r.logger.Warn("Failed to mark stale task as TIMEOUT",
				zap.String("task_id", taskID),
				zap.Error(err),
			)
			continue
		}
		reaped++
	}

	if reaped > 0 {
		r.logger.Info("Stale task reaper: reaped tasks",
			zap.Int("reaped_count", reaped),
		)
	}
}

// markTimeout marks a stale RUNNING task as TIMEOUT (terminal state).
// No requeue is performed — the upstream (control plane / user) decides
// whether to re-submit the task.
func (r *StaleTaskReaper) markTimeout(ctx context.Context, info *store.TaskInfo, nowMillis int64) error {
	taskID := info.Task.ID

	timeoutResult := &model.TaskResult{
		TaskID:            taskID,
		AgentID:           info.AgentID,
		Status:            model.TaskStatusTimeout,
		ErrorCode:         "STALE_RUNNING_TIMEOUT",
		ErrorMessage:      "task stuck in RUNNING state, marked as TIMEOUT by reaper",
		CompletedAtMillis: nowMillis,
	}

	// ApplyTaskResult atomically transitions RUNNING → TIMEOUT.
	// If another node already transitioned it (e.g., agent reported result),
	// this becomes a no-op thanks to the state machine in the Lua script.
	_, err := r.store.ApplyTaskResult(ctx, taskID, timeoutResult, nowMillis)
	if err != nil {
		return err
	}

	// Persist the timeout result
	_ = r.store.SaveResult(ctx, timeoutResult)

	// Clear running tracking
	_ = r.store.ClearRunning(ctx, taskID)

	// Publish completed event so waiters can be notified
	_ = r.store.PublishEvent(ctx, "completed", taskID)

	return nil
}
