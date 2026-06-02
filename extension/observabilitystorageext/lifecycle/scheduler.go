// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// LifecycleScheduler orchestrates periodic data lifecycle operations.
// It depends ONLY on abstractions (DIP), making it testable and provider-agnostic.
//
// Responsibilities (SRP):
//  1. Periodic tick management (start/stop)
//  2. Orchestrate: resolve retention → compute cutoff → invoke purger → audit
//
// NOT responsible for: HOW to purge, WHERE policies are stored, HOW to measure usage.
type LifecycleScheduler struct {
	resolver    RetentionResolver
	purger      LifecyclePurger
	usage       UsageReporter
	audit       AuditEmitter
	coordinator TaskCoordinator
	config      SchedulerConfig
	nodeID      string
	logger      *zap.Logger

	// Usage trend buffer
	trendMu  sync.RWMutex
	trendBuf *TrendBuffer

	// Lifecycle control
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// SchedulerOption configures the LifecycleScheduler via functional options.
type SchedulerOption func(*LifecycleScheduler)

// WithResolver sets the retention resolver.
func WithResolver(r RetentionResolver) SchedulerOption {
	return func(s *LifecycleScheduler) { s.resolver = r }
}

// WithPurger sets the lifecycle purger.
func WithPurger(p LifecyclePurger) SchedulerOption {
	return func(s *LifecycleScheduler) { s.purger = p }
}

// WithUsageReporter sets the usage reporter.
func WithUsageReporter(u UsageReporter) SchedulerOption {
	return func(s *LifecycleScheduler) { s.usage = u }
}

// WithAuditEmitter sets the audit emitter.
func WithAuditEmitter(a AuditEmitter) SchedulerOption {
	return func(s *LifecycleScheduler) { s.audit = a }
}

// WithConfig sets the scheduler configuration.
func WithConfig(cfg SchedulerConfig) SchedulerOption {
	return func(s *LifecycleScheduler) { s.config = cfg }
}

// WithLogger sets the logger.
func WithLogger(l *zap.Logger) SchedulerOption {
	return func(s *LifecycleScheduler) { s.logger = l }
}

// WithCoordinator sets the distributed task coordinator.
// When set along with config.Distributed=true, the scheduler uses
// cooperative multi-node purge instead of single-node mode.
func WithCoordinator(c TaskCoordinator) SchedulerOption {
	return func(s *LifecycleScheduler) { s.coordinator = c }
}

// NewScheduler creates a new LifecycleScheduler with the given options.
func NewScheduler(opts ...SchedulerOption) *LifecycleScheduler {
	s := &LifecycleScheduler{
		stopCh: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}

	// Apply config defaults
	s.config.ApplyDefaults()

	// Initialize nodeID from config or generate one
	if s.config.NodeID != "" {
		s.nodeID = s.config.NodeID
	} else {
		s.nodeID = fmt.Sprintf("node-%d", time.Now().UnixNano())
	}

	// Initialize trend buffer
	s.trendBuf = NewTrendBuffer(s.config.TrendBufferSize)

	// Default logger
	if s.logger == nil {
		s.logger = zap.NewNop()
	}

	// Default no-op audit if not provided
	if s.audit == nil {
		s.audit = noOpAuditEmitter{}
	}

	return s
}

// Start begins the background scheduling goroutine.
func (s *LifecycleScheduler) Start(_ context.Context) {
	if !s.config.Enabled {
		s.logger.Info("Lifecycle scheduler is disabled")
		return
	}

	s.logger.Info("Starting lifecycle scheduler",
		zap.Duration("interval", s.config.Interval),
		zap.Bool("dry_run", s.config.DryRun),
		zap.Bool("distributed", s.config.Distributed),
		zap.String("node_id", s.nodeID),
	)

	s.wg.Add(1)
	go s.loop()
}

// Stop gracefully stops the scheduler and waits for the loop to exit.
func (s *LifecycleScheduler) Stop() {
	if !s.config.Enabled {
		return
	}

	s.logger.Info("Stopping lifecycle scheduler")
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("Lifecycle scheduler stopped")
}

// GetTrend returns the usage trend snapshots (thread-safe).
func (s *LifecycleScheduler) GetTrend() []UsageSnapshot {
	s.trendMu.RLock()
	defer s.trendMu.RUnlock()
	return s.trendBuf.All()
}

// loop is the main scheduling goroutine.
func (s *LifecycleScheduler) loop() {
	defer s.wg.Done()

	// Run immediately on start
	s.runCycle(context.Background())

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.runCycle(context.Background())
		}
	}
}

// runCycle executes one full lifecycle management cycle.
func (s *LifecycleScheduler) runCycle(ctx context.Context) {
	start := time.Now()
	s.logger.Debug("Starting lifecycle cycle")

	// Phase 1: Collect usage snapshot (observe)
	s.collectUsageSnapshot(ctx)

	// Phase 2: Evaluate capacity alerts
	s.evaluateAlerts(ctx)

	// Phase 3: Purge expired data
	if s.coordinator != nil && s.config.Distributed {
		s.distributedPurge(ctx)
	} else {
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
	}

	s.logger.Debug("Lifecycle cycle completed", zap.Duration("elapsed", time.Since(start)))
}

// collectUsageSnapshot queries current usage and stores in the trend buffer.
func (s *LifecycleScheduler) collectUsageSnapshot(ctx context.Context) {
	if s.usage == nil {
		return
	}

	usage, err := s.usage.GetUsage(ctx)
	if err != nil {
		s.logger.Warn("Failed to collect usage snapshot", zap.Error(err))
		return
	}

	snapshot := UsageSnapshot{
		Timestamp:  time.Now(),
		TotalBytes: usage.TotalBytes,
		UsedBytes:  usage.UsedBytes,
		BySignal:   usage.BySignal,
	}

	s.trendMu.Lock()
	s.trendBuf.Push(snapshot)
	s.trendMu.Unlock()
}

// evaluateAlerts checks if usage exceeds configured thresholds.
func (s *LifecycleScheduler) evaluateAlerts(ctx context.Context) {
	if s.usage == nil {
		return
	}

	usage, err := s.usage.GetUsage(ctx)
	if err != nil {
		return
	}

	if usage.TotalBytes == 0 {
		return
	}

	ratio := float64(usage.UsedBytes) / float64(usage.TotalBytes)

	if ratio >= s.config.UsageCriticalRatio {
		s.logger.Error("Storage usage CRITICAL",
			zap.Float64("ratio", ratio),
			zap.Int64("used_bytes", usage.UsedBytes),
			zap.Int64("total_bytes", usage.TotalBytes),
		)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionAlert,
			Operator:  "scheduler",
			Result:    map[string]any{"level": "critical", "ratio": ratio},
		})
	} else if ratio >= s.config.UsageWarningRatio {
		s.logger.Warn("Storage usage WARNING",
			zap.Float64("ratio", ratio),
			zap.Int64("used_bytes", usage.UsedBytes),
			zap.Int64("total_bytes", usage.TotalBytes),
		)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionAlert,
			Operator:  "scheduler",
			Result:    map[string]any{"level": "warning", "ratio": ratio},
		})
	}
}

// purgeSignal handles the purge logic for a single signal type.
func (s *LifecycleScheduler) purgeSignal(ctx context.Context, signal SignalType) {
	if s.resolver == nil || s.purger == nil {
		return
	}

	// Step 1: Resolve effective retention
	retention, err := s.resolver.Resolve(ctx, signal, "" /* platform level */)
	if err != nil {
		s.logger.Error("Failed to resolve retention",
			zap.String("signal", string(signal)),
			zap.Error(err),
		)
		return
	}

	if retention.Duration <= 0 {
		s.logger.Debug("Retention is zero or negative, skipping purge",
			zap.String("signal", string(signal)),
		)
		return
	}

	// Step 2: Compute cutoff time
	cutoff := time.Now().Add(-retention.Duration)

	// Step 3: Check if there's data to purge (avoid unnecessary operations)
	boundary, err := s.purger.GetDataBoundary(ctx, signal)
	if err != nil {
		s.logger.Warn("Failed to get data boundary, proceeding with purge anyway",
			zap.String("signal", string(signal)),
			zap.Error(err),
		)
	} else if boundary.IsEmpty || boundary.OldestAt == nil || !boundary.OldestAt.Before(cutoff) {
		s.logger.Debug("No expired data to purge",
			zap.String("signal", string(signal)),
			zap.Time("cutoff", cutoff),
		)
		return
	}

	// Step 4: Execute or estimate
	if s.config.DryRun {
		estimate, err := s.purger.EstimatePurge(ctx, signal, cutoff)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionEstimate,
			Signal:    signal,
			Operator:  "scheduler",
			DryRun:    true,
			Input:     map[string]any{"cutoff": cutoff, "retention": retention.Duration.String()},
			Result:    estimate,
			Error:     errStr(err),
		})
		if err != nil {
			s.logger.Warn("Estimate purge failed",
				zap.String("signal", string(signal)),
				zap.Error(err),
			)
		} else {
			s.logger.Info("[DRY-RUN] Would purge expired data",
				zap.String("signal", string(signal)),
				zap.Int64("estimated_docs", estimate.EstimatedDocs),
				zap.Int64("estimated_bytes", estimate.EstimatedBytes),
				zap.Int("affected_units", len(estimate.AffectedUnits)),
			)
		}
		return
	}

	// Execute actual purge
	result, err := s.purger.PurgeExpired(ctx, signal, cutoff)
	s.audit.Emit(ctx, LifecycleEvent{
		Timestamp: time.Now(),
		Action:    ActionAutoPurge,
		Signal:    signal,
		Operator:  "scheduler",
		Input:     map[string]any{"cutoff": cutoff, "retention": retention.Duration.String()},
		Result:    result,
		Error:     errStr(err),
	})

	if err != nil {
		s.logger.Error("Purge failed",
			zap.String("signal", string(signal)),
			zap.Time("cutoff", cutoff),
			zap.Error(err),
		)
		return
	}

	if result != nil && result.DeletedDocs > 0 {
		s.logger.Info("Purge completed",
			zap.String("signal", string(signal)),
			zap.Int64("deleted_docs", result.DeletedDocs),
			zap.Int("deleted_units", result.DeletedUnits),
			zap.Int64("freed_bytes", result.FreedBytes),
			zap.Duration("duration", result.Duration),
		)
	}
}

// ═══════════════════════════════════════════════════
// Distributed Purge Orchestration
// ═══════════════════════════════════════════════════

// distributedPurge implements the three-phase cooperative purge pipeline:
//
//	Phase 1 (Leader): Scan expired indices → build tasks → submit to queue
//	Phase 2 (All):    Claim tasks from pool → execute deletions → report results
//	Phase 3 (Leader): Verify progress → audit → mark epoch complete
//
// If the purger doesn't implement IndexLister/SingleIndexPurger, or task count
// is below threshold, it falls back to single-node purge.
func (s *LifecycleScheduler) distributedPurge(ctx context.Context) {
	// Check if purger supports distributed mode (optional interfaces)
	lister, hasLister := s.purger.(IndexLister)
	singlePurger, hasSingle := s.purger.(SingleIndexPurger)
	if !hasLister || !hasSingle {
		s.logger.Debug("Purger does not support distributed mode, falling back to single-node")
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
		return
	}

	// Check if there's already an active epoch (another cycle in progress)
	activeEpoch, err := s.coordinator.GetActiveEpoch(ctx)
	if err != nil {
		s.logger.Warn("Failed to check active epoch, falling back to single-node", zap.Error(err))
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
		return
	}

	if activeEpoch > 0 {
		// Epoch already in progress — participate as worker only (Phase 2)
		s.logger.Info("Joining active epoch as worker", zap.Int64("epoch", activeEpoch))
		s.executeTasks(ctx, activeEpoch, singlePurger)
		return
	}

	// Try to become leader for planning
	isLeader, err := s.coordinator.TryBecomeLeader(ctx)
	if err != nil {
		s.logger.Warn("Leader election failed, falling back to single-node", zap.Error(err))
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
		return
	}

	if isLeader {
		defer func() {
			if releaseErr := s.coordinator.ReleaseLeader(ctx); releaseErr != nil {
				s.logger.Warn("Failed to release leader", zap.Error(releaseErr))
			}
		}()

		// Phase 1: Plan tasks
		epoch, tasks := s.planTasks(ctx, lister)
		if len(tasks) == 0 {
			s.logger.Debug("No tasks to distribute")
			return
		}

		// Adaptive threshold: fall back to single-node for small batches
		if len(tasks) <= s.config.DistributedThreshold {
			s.logger.Info("Task count below threshold, using single-node purge",
				zap.Int("tasks", len(tasks)),
				zap.Int("threshold", s.config.DistributedThreshold),
			)
			for _, signal := range AllSignals() {
				s.purgeSignal(ctx, signal)
			}
			return
		}

		// Submit tasks to coordinator
		if err := s.coordinator.SubmitTasks(ctx, epoch, tasks); err != nil {
			s.logger.Error("Failed to submit tasks", zap.Error(err))
			return
		}

		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionDistPlan,
			Operator:  "scheduler:" + s.nodeID,
			Result:    map[string]any{"epoch": epoch, "total_tasks": len(tasks)},
		})

		// Phase 2: Leader also participates as worker
		s.executeTasks(ctx, epoch, singlePurger)

		// Phase 3: Verify and complete
		s.verifyAndComplete(ctx, epoch)
	} else {
		// Not leader — wait briefly for tasks to appear, then work as worker
		time.Sleep(2 * time.Second)
		epoch, err := s.coordinator.GetActiveEpoch(ctx)
		if err != nil || epoch == 0 {
			s.logger.Debug("No active epoch found, skipping this cycle")
			return
		}
		s.executeTasks(ctx, epoch, singlePurger)
	}
}

// planTasks scans all signals for expired indices and builds PurgeTask list.
// Returns the epoch ID and the task list.
func (s *LifecycleScheduler) planTasks(ctx context.Context, lister IndexLister) (int64, []PurgeTask) {
	epoch := time.Now().UnixMilli()
	var tasks []PurgeTask

	for _, signal := range AllSignals() {
		if s.resolver == nil {
			continue
		}

		retention, err := s.resolver.Resolve(ctx, signal, "")
		if err != nil {
			s.logger.Error("Failed to resolve retention for planning",
				zap.String("signal", string(signal)),
				zap.Error(err),
			)
			continue
		}

		if retention.Duration <= 0 {
			continue
		}

		cutoff := time.Now().Add(-retention.Duration)

		// Scan expired indices
		expired, err := lister.ListExpired(ctx, signal, cutoff)
		if err != nil {
			s.logger.Error("Failed to list expired indices",
				zap.String("signal", string(signal)),
				zap.Error(err),
			)
			continue
		}

		// Build tasks from expired indices
		for _, indexName := range expired {
			taskID := fmt.Sprintf("%d:%s:%s", epoch, signal, indexName)
			tasks = append(tasks, PurgeTask{
				ID:        taskID,
				Epoch:     epoch,
				Signal:    signal,
				IndexName: indexName,
				Cutoff:    cutoff,
				Retry:     0,
			})
		}
	}

	s.logger.Info("Planning complete",
		zap.Int64("epoch", epoch),
		zap.Int("total_tasks", len(tasks)),
	)
	return epoch, tasks
}

// executeTasks claims and executes tasks from the pool until empty.
// Uses bounded concurrency to control ES pressure.
func (s *LifecycleScheduler) executeTasks(ctx context.Context, epoch int64, purger SingleIndexPurger) {
	sem := make(chan struct{}, s.config.WorkerConcurrency)
	var wg sync.WaitGroup

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			s.logger.Debug("Context cancelled, stopping task execution")
			wg.Wait()
			return
		case <-s.stopCh:
			s.logger.Debug("Scheduler stopping, finishing current tasks")
			wg.Wait()
			return
		default:
		}

		// Claim a task atomically
		task, err := s.coordinator.ClaimTask(ctx, epoch)
		if err != nil {
			s.logger.Warn("Failed to claim task", zap.Error(err))
			break
		}
		if task == nil {
			// Pool empty — all tasks claimed
			break
		}

		// Execute with bounded concurrency
		sem <- struct{}{}
		wg.Add(1)
		go func(t PurgeTask) {
			defer wg.Done()
			defer func() { <-sem }()
			s.executeSingleTask(ctx, epoch, t, purger)
		}(*task)
	}

	wg.Wait()
}

// executeSingleTask executes one purge task and reports the result.
func (s *LifecycleScheduler) executeSingleTask(
	ctx context.Context, epoch int64, task PurgeTask, purger SingleIndexPurger,
) {
	startedAt := time.Now()

	// Create timeout context for the individual task
	taskCtx, cancel := context.WithTimeout(ctx, s.config.TaskTimeout)
	defer cancel()

	// Execute the deletion (idempotent)
	err := purger.DeleteSingleIndex(taskCtx, task.IndexName)

	result := TaskResult{
		NodeID:    s.nodeID,
		StartedAt: startedAt,
		DoneAt:    time.Now(),
	}

	switch {
	case err == nil:
		result.Status = TaskStatusSuccess
		s.logger.Debug("Task completed",
			zap.String("task_id", task.ID),
			zap.String("index", task.IndexName),
			zap.Duration("duration", result.DoneAt.Sub(result.StartedAt)),
		)
	case taskCtx.Err() != nil:
		result.Status = TaskStatusTimeout
		result.Error = "task timeout"
		s.logger.Warn("Task timed out",
			zap.String("task_id", task.ID),
			zap.String("index", task.IndexName),
		)
	default:
		result.Status = TaskStatusFailed
		result.Error = err.Error()
		s.logger.Warn("Task failed",
			zap.String("task_id", task.ID),
			zap.String("index", task.IndexName),
			zap.Error(err),
		)
	}

	// Report result to coordinator (best-effort; if this fails, task is still done)
	if reportErr := s.coordinator.ReportResult(ctx, epoch, task.ID, result); reportErr != nil {
		s.logger.Warn("Failed to report task result",
			zap.String("task_id", task.ID),
			zap.Error(reportErr),
		)
	}
}

// verifyAndComplete polls epoch progress until done or timeout, then handles
// failed task retries and marks the epoch complete.
// Only the leader calls this after participating as a worker.
func (s *LifecycleScheduler) verifyAndComplete(ctx context.Context, epoch int64) {
	deadline := time.Now().Add(s.config.VerifyTimeout)
	ticker := time.NewTicker(s.config.VerifyPollInterval)
	defer ticker.Stop()

	var finalProgress *PurgeProgress

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("Context cancelled during verification", zap.Int64("epoch", epoch))
			return
		case <-s.stopCh:
			s.logger.Warn("Scheduler stopping during verification", zap.Int64("epoch", epoch))
			return
		case <-ticker.C:
			progress, err := s.coordinator.GetProgress(ctx, epoch)
			if err != nil {
				s.logger.Warn("Failed to get epoch progress", zap.Error(err))
				continue
			}

			s.logger.Debug("Verification poll",
				zap.Int64("epoch", epoch),
				zap.Int("remaining", progress.Remaining),
				zap.Int("completed", progress.Completed),
				zap.Int("failed", progress.Failed),
			)

			// Done: no tasks remaining in queue and all results collected
			if progress.Status == "done" {
				finalProgress = progress
				goto verified
			}

			// Timeout: waited too long
			if time.Now().After(deadline) {
				s.logger.Warn("Verification timed out, proceeding with current state",
					zap.Int64("epoch", epoch),
					zap.Duration("timeout", s.config.VerifyTimeout),
					zap.Int("remaining", progress.Remaining),
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

	s.logger.Info("Epoch verification complete",
		zap.Int64("epoch", epoch),
		zap.Int("total", finalProgress.TotalTasks),
		zap.Int("completed", finalProgress.Completed),
		zap.Int("failed", finalProgress.Failed),
		zap.Int("remaining", finalProgress.Remaining),
		zap.String("status", finalProgress.Status),
	)

	// Handle failed tasks — retry if within MaxRetries limit
	if finalProgress.Failed > 0 {
		s.retryFailedTasks(ctx, epoch)
	}

	// Emit audit event
	s.audit.Emit(ctx, LifecycleEvent{
		Timestamp: time.Now(),
		Action:    ActionDistVerify,
		Operator:  "scheduler:" + s.nodeID,
		Result: map[string]any{
			"epoch":     epoch,
			"total":     finalProgress.TotalTasks,
			"completed": finalProgress.Completed,
			"failed":    finalProgress.Failed,
			"remaining": finalProgress.Remaining,
			"status":    finalProgress.Status,
		},
	})

	// Mark epoch complete
	if err := s.coordinator.CompleteEpoch(ctx, epoch); err != nil {
		s.logger.Error("Failed to complete epoch", zap.Error(err))
	}
}

// retryFailedTasks re-enqueues failed tasks (up to MaxRetries) and executes them.
// Loops until no more tasks are eligible for retry (all succeeded or hit MaxRetries).
func (s *LifecycleScheduler) retryFailedTasks(ctx context.Context, epoch int64) {
	singlePurger, ok := s.purger.(SingleIndexPurger)
	if !ok {
		return
	}

	retryCoord, hasRetry := s.coordinator.(RetryableCoordinator)
	if !hasRetry {
		s.logger.Debug("Coordinator does not support retry enumeration, skipping retries")
		return
	}

	for round := 1; round <= s.config.MaxRetries; round++ {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		failedTasks, err := retryCoord.GetFailedTasks(ctx, epoch, s.config.MaxRetries)
		if err != nil {
			s.logger.Warn("Failed to get failed tasks for retry", zap.Error(err))
			return
		}

		if len(failedTasks) == 0 {
			return
		}

		s.logger.Info("Retrying failed tasks",
			zap.Int64("epoch", epoch),
			zap.Int("retry_round", round),
			zap.Int("retry_count", len(failedTasks)),
		)

		// Re-submit and execute failed tasks with incremented retry count
		for i := range failedTasks {
			failedTasks[i].Retry++
		}

		if err := s.coordinator.SubmitTasks(ctx, epoch, failedTasks); err != nil {
			s.logger.Warn("Failed to re-submit retry tasks", zap.Error(err))
			return
		}

		// Execute retries (leader handles them)
		s.executeTasks(ctx, epoch, singlePurger)

		// Wait for retry tasks to complete before checking again
		s.waitForRetryCompletion(ctx, epoch)
	}
}

// waitForRetryCompletion polls until the current retry batch completes.
func (s *LifecycleScheduler) waitForRetryCompletion(ctx context.Context, epoch int64) {
	ticker := time.NewTicker(s.config.VerifyPollInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(s.config.VerifyTimeout)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			progress, err := s.coordinator.GetProgress(ctx, epoch)
			if err != nil {
				continue
			}
			if progress.Status == "done" || time.Now().After(deadline) {
				return
			}
		}
	}
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

// errStr safely converts an error to string, returning empty for nil.
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// noOpAuditEmitter is a safe default that does nothing.
type noOpAuditEmitter struct{}

func (noOpAuditEmitter) Emit(_ context.Context, _ LifecycleEvent) {}
