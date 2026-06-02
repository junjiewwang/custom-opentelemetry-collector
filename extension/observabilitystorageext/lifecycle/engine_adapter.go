// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/taskengine"
	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// EngineAdapter bridges the LifecycleScheduler to the unified taskengine.Engine.
// It replaces the old TaskCoordinator interface, using the Engine for:
//   - Task submission (Submit/SubmitBatch)
//   - Task claiming (Claim)
//   - Result reporting (Report)
//   - Progress tracking (GetProgress)
//
// Leader election remains a separate concern (handled by LeaderElector interface).
type EngineAdapter struct {
	engine   taskengine.Engine
	nodeID   string
	consumer *taskengine.ConsumerDescriptor
	logger   *zap.Logger
}

// NewEngineAdapter creates a new adapter that connects the scheduler to the engine.
func NewEngineAdapter(engine taskengine.Engine, nodeID string, logger *zap.Logger) *EngineAdapter {
	return &EngineAdapter{
		engine: engine,
		nodeID: nodeID,
		consumer: &taskengine.ConsumerDescriptor{
			ID:           nodeID,
			Roles:        []node.Role{node.RolePurger},
			Capabilities: node.NewCapabilitySet(node.CapPurgeExecute, node.CapStorageRead, node.CapStorageDelete, node.CapPurgePlan),
		},
		logger: logger,
	}
}

// PurgeTaskPayload is the JSON payload embedded in taskengine.Task for purge operations.
type PurgeTaskPayload struct {
	Signal    SignalType `json:"signal"`
	IndexName string    `json:"indexName"`
	Cutoff    time.Time `json:"cutoff"`
	Retry     int       `json:"retry"`
}

// SubmitPurgeTasks converts PurgeTask list to engine Tasks and submits them.
// The groupID is the epoch (as string) for progress tracking.
func (a *EngineAdapter) SubmitPurgeTasks(ctx context.Context, epoch int64, tasks []PurgeTask) error {
	groupID := fmt.Sprintf("lifecycle:purge:%d", epoch)
	engineTasks := make([]*taskengine.Task, 0, len(tasks))

	for _, pt := range tasks {
		payload, err := json.Marshal(PurgeTaskPayload{
			Signal:    pt.Signal,
			IndexName: pt.IndexName,
			Cutoff:    pt.Cutoff,
			Retry:     pt.Retry,
		})
		if err != nil {
			return fmt.Errorf("marshal purge payload for %s: %w", pt.ID, err)
		}

		engineTasks = append(engineTasks, &taskengine.Task{
			ID:         pt.ID,
			Type:       taskengine.TaskTypePurgeIndex,
			Payload:    payload,
			GroupID:    groupID,
			MaxRetries: pt.Retry, // Engine handles retry internally
			Routing: taskengine.TaskRouting{
				Strategy:             taskengine.RoutingCapability,
				RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
			},
			Metadata: map[string]string{
				"epoch":  fmt.Sprintf("%d", epoch),
				"signal": string(pt.Signal),
			},
		})
	}

	return a.engine.SubmitBatch(ctx, engineTasks)
}

// ClaimTask claims one purge task from the engine.
// Returns nil when no tasks are available.
func (a *EngineAdapter) ClaimTask(ctx context.Context) (*taskengine.Task, error) {
	return a.engine.Claim(ctx, a.consumer)
}

// ReportSuccess reports a successful task completion.
func (a *EngineAdapter) ReportSuccess(ctx context.Context, taskID string, startedAt time.Time) error {
	return a.engine.Report(ctx, &taskengine.TaskResult{
		TaskID:      taskID,
		NodeID:      a.nodeID,
		Status:      taskengine.StatusSuccess,
		StartedAt:   startedAt.UnixMilli(),
		CompletedAt: time.Now().UnixMilli(),
	})
}

// ReportFailed reports a failed task execution.
func (a *EngineAdapter) ReportFailed(ctx context.Context, taskID string, startedAt time.Time, errMsg string) error {
	return a.engine.Report(ctx, &taskengine.TaskResult{
		TaskID:      taskID,
		NodeID:      a.nodeID,
		Status:      taskengine.StatusFailed,
		Error:       errMsg,
		StartedAt:   startedAt.UnixMilli(),
		CompletedAt: time.Now().UnixMilli(),
	})
}

// ReportTimeout reports a timed-out task execution.
func (a *EngineAdapter) ReportTimeout(ctx context.Context, taskID string, startedAt time.Time) error {
	return a.engine.Report(ctx, &taskengine.TaskResult{
		TaskID:      taskID,
		NodeID:      a.nodeID,
		Status:      taskengine.StatusTimeout,
		Error:       "task timeout",
		StartedAt:   startedAt.UnixMilli(),
		CompletedAt: time.Now().UnixMilli(),
	})
}

// ReportSkipped reports a skipped task (e.g., index already deleted).
func (a *EngineAdapter) ReportSkipped(ctx context.Context, taskID string, startedAt time.Time) error {
	return a.engine.Report(ctx, &taskengine.TaskResult{
		TaskID:      taskID,
		NodeID:      a.nodeID,
		Status:      taskengine.StatusSkipped,
		StartedAt:   startedAt.UnixMilli(),
		CompletedAt: time.Now().UnixMilli(),
	})
}

// GetProgress returns the progress for the given epoch's purge batch.
func (a *EngineAdapter) GetProgress(ctx context.Context, epoch int64) (*taskengine.Progress, error) {
	groupID := fmt.Sprintf("lifecycle:purge:%d", epoch)
	return a.engine.GetProgress(ctx, taskengine.TaskTypePurgeIndex, groupID)
}

// ParsePurgePayload extracts the PurgeTaskPayload from an engine Task.
func ParsePurgePayload(task *taskengine.Task) (*PurgeTaskPayload, error) {
	var payload PurgeTaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal purge payload: %w", err)
	}
	return &payload, nil
}

// ═══════════════════════════════════════════════════
// LeaderElector — Extracted from TaskCoordinator
// ═══════════════════════════════════════════════════

// LeaderElector handles leader election for distributed scheduling.
// This is separate from the task engine — it's an orchestration concern.
type LeaderElector interface {
	// TryBecomeLeader attempts to acquire leader role (non-blocking).
	TryBecomeLeader(ctx context.Context) (bool, error)
	// ReleaseLeader releases the leader lock.
	ReleaseLeader(ctx context.Context) error
	// GetActiveEpoch returns the current epoch, or 0 if none.
	GetActiveEpoch(ctx context.Context) (int64, error)
	// SetActiveEpoch sets the current active epoch.
	SetActiveEpoch(ctx context.Context, epoch int64) error
	// ClearActiveEpoch clears the active epoch marker.
	ClearActiveEpoch(ctx context.Context) error
}

// ═══════════════════════════════════════════════════
// EngineBasedDistributedPurge — New distributed purge implementation
// ═══════════════════════════════════════════════════

// DistributedPurgeOrchestrator manages the three-phase distributed purge
// using the unified task engine. It replaces the old coordinator-based approach.
type DistributedPurgeOrchestrator struct {
	adapter  *EngineAdapter
	elector  LeaderElector
	config   SchedulerConfig
	nodeID   string
	logger   *zap.Logger
}

// NewDistributedPurgeOrchestrator creates the orchestrator.
func NewDistributedPurgeOrchestrator(
	engine taskengine.Engine,
	elector LeaderElector,
	config SchedulerConfig,
	nodeID string,
	logger *zap.Logger,
) *DistributedPurgeOrchestrator {
	return &DistributedPurgeOrchestrator{
		adapter: NewEngineAdapter(engine, nodeID, logger),
		elector: elector,
		config:  config,
		nodeID:  nodeID,
		logger:  logger,
	}
}

// Execute runs the full distributed purge cycle.
// Returns true if the purge was handled (distributed mode), false if fallback to single-node is needed.
func (o *DistributedPurgeOrchestrator) Execute(
	ctx context.Context,
	lister IndexLister,
	singlePurger SingleIndexPurger,
	resolver RetentionResolver,
	audit AuditEmitter,
	stopCh <-chan struct{},
) bool {
	// Check if there's already an active epoch (join as worker)
	activeEpoch, err := o.elector.GetActiveEpoch(ctx)
	if err != nil {
		o.logger.Warn("Failed to check active epoch, falling back", zap.Error(err))
		return false
	}

	if activeEpoch > 0 {
		// Existing epoch — participate as worker
		o.logger.Info("Joining active epoch as worker", zap.Int64("epoch", activeEpoch))
		o.executeWorkerPhase(ctx, singlePurger, stopCh)
		return true
	}

	// Try to become leader
	isLeader, err := o.elector.TryBecomeLeader(ctx)
	if err != nil {
		o.logger.Warn("Leader election failed", zap.Error(err))
		return false
	}

	if !isLeader {
		// Not leader — wait briefly, then join as worker if epoch appears
		time.Sleep(2 * time.Second)
		activeEpoch, err = o.elector.GetActiveEpoch(ctx)
		if err != nil || activeEpoch == 0 {
			o.logger.Debug("No active epoch found after waiting")
			return true // Still handled (just nothing to do)
		}
		o.executeWorkerPhase(ctx, singlePurger, stopCh)
		return true
	}

	// We are the leader
	defer func() {
		if releaseErr := o.elector.ReleaseLeader(ctx); releaseErr != nil {
			o.logger.Warn("Failed to release leader", zap.Error(releaseErr))
		}
	}()

	// Phase 1: Plan
	epoch, tasks := o.planTasks(ctx, lister, resolver)
	if len(tasks) == 0 {
		o.logger.Debug("No tasks to distribute")
		return true
	}

	// Adaptive threshold check
	if len(tasks) <= o.config.DistributedThreshold {
		o.logger.Info("Task count below threshold, falling back to single-node",
			zap.Int("tasks", len(tasks)),
			zap.Int("threshold", o.config.DistributedThreshold),
		)
		return false // Signal caller to use single-node mode
	}

	// Set active epoch
	if err := o.elector.SetActiveEpoch(ctx, epoch); err != nil {
		o.logger.Error("Failed to set active epoch", zap.Error(err))
		return false
	}

	// Submit tasks to engine
	if err := o.adapter.SubmitPurgeTasks(ctx, epoch, tasks); err != nil {
		o.logger.Error("Failed to submit tasks to engine", zap.Error(err))
		return false
	}

	audit.Emit(ctx, LifecycleEvent{
		Timestamp: time.Now(),
		Action:    ActionDistPlan,
		Operator:  "scheduler:" + o.nodeID,
		Result:    map[string]any{"epoch": epoch, "total_tasks": len(tasks)},
	})

	// Phase 2: Leader also participates as worker
	o.executeWorkerPhase(ctx, singlePurger, stopCh)

	// Phase 3: Verify completion
	o.verifyAndComplete(ctx, epoch, audit, stopCh)

	return true
}

// planTasks scans expired indices across all signals.
func (o *DistributedPurgeOrchestrator) planTasks(
	ctx context.Context,
	lister IndexLister,
	resolver RetentionResolver,
) (int64, []PurgeTask) {
	epoch := time.Now().UnixMilli()
	var tasks []PurgeTask

	for _, signal := range AllSignals() {
		retention, err := resolver.Resolve(ctx, signal, "")
		if err != nil {
			o.logger.Error("Failed to resolve retention", zap.String("signal", string(signal)), zap.Error(err))
			continue
		}
		if retention.Duration <= 0 {
			continue
		}

		cutoff := time.Now().Add(-retention.Duration)
		expired, err := lister.ListExpired(ctx, signal, cutoff)
		if err != nil {
			o.logger.Error("Failed to list expired", zap.String("signal", string(signal)), zap.Error(err))
			continue
		}

		for _, indexName := range expired {
			tasks = append(tasks, PurgeTask{
				ID:        fmt.Sprintf("%d:%s:%s", epoch, signal, indexName),
				Epoch:     epoch,
				Signal:    signal,
				IndexName: indexName,
				Cutoff:    cutoff,
			})
		}
	}

	o.logger.Info("Planning complete", zap.Int64("epoch", epoch), zap.Int("total_tasks", len(tasks)))
	return epoch, tasks
}

// executeWorkerPhase claims and executes tasks until the queue is empty.
func (o *DistributedPurgeOrchestrator) executeWorkerPhase(
	ctx context.Context,
	purger SingleIndexPurger,
	stopCh <-chan struct{},
) {
	sem := make(chan struct{}, o.config.WorkerConcurrency)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-stopCh:
			wg.Wait()
			return
		default:
		}

		// Claim from engine
		task, err := o.adapter.ClaimTask(ctx)
		if err != nil {
			o.logger.Warn("Failed to claim task", zap.Error(err))
			break
		}
		if task == nil {
			break // Queue empty
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(t *taskengine.Task) {
			defer wg.Done()
			defer func() { <-sem }()
			o.executeSingleTask(ctx, t, purger)
		}(task)
	}

	wg.Wait()
}

// executeSingleTask executes one purge task and reports result to the engine.
func (o *DistributedPurgeOrchestrator) executeSingleTask(
	ctx context.Context,
	task *taskengine.Task,
	purger SingleIndexPurger,
) {
	startedAt := time.Now()

	payload, err := ParsePurgePayload(task)
	if err != nil {
		o.logger.Error("Failed to parse purge payload", zap.String("taskID", task.ID), zap.Error(err))
		_ = o.adapter.ReportFailed(ctx, task.ID, startedAt, "invalid payload: "+err.Error())
		return
	}

	// Execute with timeout
	taskCtx, cancel := context.WithTimeout(ctx, o.config.TaskTimeout)
	defer cancel()

	err = purger.DeleteSingleIndex(taskCtx, payload.IndexName)

	switch {
	case err == nil:
		_ = o.adapter.ReportSuccess(ctx, task.ID, startedAt)
		o.logger.Debug("Task completed",
			zap.String("taskID", task.ID),
			zap.String("index", payload.IndexName),
			zap.Duration("duration", time.Since(startedAt)),
		)
	case taskCtx.Err() != nil:
		_ = o.adapter.ReportTimeout(ctx, task.ID, startedAt)
		o.logger.Warn("Task timed out", zap.String("taskID", task.ID), zap.String("index", payload.IndexName))
	default:
		_ = o.adapter.ReportFailed(ctx, task.ID, startedAt, err.Error())
		o.logger.Warn("Task failed", zap.String("taskID", task.ID), zap.String("index", payload.IndexName), zap.Error(err))
	}
}

// verifyAndComplete waits for all tasks to finish and marks the epoch complete.
func (o *DistributedPurgeOrchestrator) verifyAndComplete(
	ctx context.Context,
	epoch int64,
	audit AuditEmitter,
	stopCh <-chan struct{},
) {
	deadline := time.Now().Add(o.config.VerifyTimeout)
	ticker := time.NewTicker(o.config.VerifyPollInterval)
	defer ticker.Stop()

	var finalProgress *taskengine.Progress

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
			progress, err := o.adapter.GetProgress(ctx, epoch)
			if err != nil {
				o.logger.Warn("Failed to get progress", zap.Error(err))
				continue
			}

			o.logger.Debug("Verification poll",
				zap.Int64("epoch", epoch),
				zap.Int("pending", progress.Pending),
				zap.Int("running", progress.Running),
				zap.Int("completed", progress.Completed),
				zap.Int("failed", progress.Failed),
			)

			if progress.IsAllDone() {
				finalProgress = progress
				goto verified
			}

			if time.Now().After(deadline) {
				o.logger.Warn("Verification timed out",
					zap.Int64("epoch", epoch),
					zap.Int("pending", progress.Pending),
					zap.Int("running", progress.Running),
				)
				finalProgress = progress
				goto verified
			}
		}
	}

verified:
	if finalProgress == nil {
		return
	}

	o.logger.Info("Epoch verification complete",
		zap.Int64("epoch", epoch),
		zap.Int("total", finalProgress.Total),
		zap.Int("completed", finalProgress.Completed),
		zap.Int("failed", finalProgress.Failed),
	)

	// Emit audit event
	audit.Emit(ctx, LifecycleEvent{
		Timestamp: time.Now(),
		Action:    ActionDistVerify,
		Operator:  "scheduler:" + o.nodeID,
		Result: map[string]any{
			"epoch":     epoch,
			"total":     finalProgress.Total,
			"completed": finalProgress.Completed,
			"failed":    finalProgress.Failed,
			"timeout":   finalProgress.Timeout,
		},
	})

	// Clear active epoch
	if err := o.elector.ClearActiveEpoch(ctx); err != nil {
		o.logger.Error("Failed to clear active epoch", zap.Error(err))
	}
}
