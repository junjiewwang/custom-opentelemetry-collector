// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// EngineImpl is the concrete implementation of the Engine interface.
// It composes Router, Store, and the state machine into a coherent task orchestrator.
type EngineImpl struct {
	router Router
	store  Store
	logger *zap.Logger

	// config
	defaultTimeout    time.Duration
	defaultMaxRetries int

	// lifecycle
	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// EngineConfig holds configuration for the engine.
type EngineConfig struct {
	// DefaultTimeout is applied to tasks that don't specify their own timeout.
	DefaultTimeout time.Duration
	// DefaultMaxRetries is applied to tasks that don't specify retry count.
	DefaultMaxRetries int
}

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		DefaultTimeout:    5 * time.Minute,
		DefaultMaxRetries: 3,
	}
}

// NewEngine creates a new Engine implementation.
func NewEngine(store Store, router Router, logger *zap.Logger, cfg EngineConfig) *EngineImpl {
	if router == nil {
		router = NewCompositeRouter()
	}
	return &EngineImpl{
		router:            router,
		store:             store,
		logger:            logger,
		defaultTimeout:    cfg.DefaultTimeout,
		defaultMaxRetries: cfg.DefaultMaxRetries,
		stopCh:            make(chan struct{}),
	}
}

// ═══ Producer API ═══

// Submit enqueues a single task.
func (e *EngineImpl) Submit(ctx context.Context, task *Task) error {
	// Apply defaults
	e.applyDefaults(task)

	// Validate
	if task.ID == "" {
		return fmt.Errorf("task ID is required")
	}
	if task.Type == "" {
		return fmt.Errorf("task type is required")
	}

	// Set initial state
	task.Status = StatusPending
	task.CreatedAt = time.Now().UnixMilli()

	// Persist the task
	if err := e.store.SaveTask(ctx, task); err != nil {
		return fmt.Errorf("save task: %w", err)
	}

	// Route to queue
	queueID := e.router.Route(task)
	if err := e.store.Enqueue(ctx, queueID, task.ID, task.Priority); err != nil {
		// Best effort: task is saved but not enqueued — reaper can detect this
		e.logger.Error("failed to enqueue task",
			zap.String("taskID", task.ID),
			zap.String("queueID", queueID),
			zap.Error(err),
		)
		return fmt.Errorf("enqueue task: %w", err)
	}

	// Publish event (TargetNodeID enables long poll to wake the correct agent)
	_ = e.store.PublishEvent(ctx, TaskEvent{
		Type:         EventTaskSubmitted,
		TaskID:       task.ID,
		TargetNodeID: task.Routing.TargetNodeID,
		Status:       StatusPending,
		At:           task.CreatedAt,
	})

	e.logger.Debug("task submitted",
		zap.String("taskID", task.ID),
		zap.String("type", string(task.Type)),
		zap.String("queue", queueID),
		zap.String("strategy", string(task.Routing.Strategy)),
	)
	return nil
}

// SubmitBatch enqueues multiple tasks.
func (e *EngineImpl) SubmitBatch(ctx context.Context, tasks []*Task) error {
	var firstErr error
	for _, task := range tasks {
		if err := e.Submit(ctx, task); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			e.logger.Warn("batch submit failed for task",
				zap.String("taskID", task.ID),
				zap.Error(err),
			)
		}
	}
	return firstErr
}

// Cancel cancels a task.
func (e *EngineImpl) Cancel(ctx context.Context, taskID string) error {
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %s not found", taskID)
	}

	// If already terminal, no-op
	if task.Status.IsTerminal() {
		return nil
	}

	// Transition to cancelled
	if err := e.store.UpdateTaskStatus(ctx, taskID, StatusCancelled, ""); err != nil {
		return err
	}

	// Remove from queue
	queueID := e.router.Route(task)
	_ = e.store.RemoveFromQueue(ctx, queueID, taskID)

	// Publish event
	_ = e.store.PublishEvent(ctx, TaskEvent{
		Type:   EventTaskCancelled,
		TaskID: taskID,
		Status: StatusCancelled,
		At:     time.Now().UnixMilli(),
	})

	e.logger.Info("task cancelled", zap.String("taskID", taskID))
	return nil
}

// ═══ Consumer API ═══

// Claim atomically dequeues a task and marks it as running.
func (e *EngineImpl) Claim(ctx context.Context, consumer *ConsumerDescriptor) (*Task, error) {
	if consumer == nil || consumer.ID == "" {
		return nil, fmt.Errorf("consumer descriptor with ID is required")
	}

	// Determine which queues this consumer should monitor
	queues := e.router.MatchQueues(consumer)
	if len(queues) == 0 {
		return nil, nil
	}

	// Dequeue from the first non-empty queue
	taskID, err := e.store.Dequeue(ctx, queues)
	if err != nil {
		return nil, fmt.Errorf("dequeue: %w", err)
	}
	if taskID == "" {
		return nil, nil // No tasks available
	}

	// Transition to Running
	if err := e.store.UpdateTaskStatus(ctx, taskID, StatusRunning, consumer.ID); err != nil {
		// If transition fails (e.g., already claimed by another), return nil
		if IsInvalidTransition(err) {
			e.logger.Debug("task already transitioned",
				zap.String("taskID", taskID),
				zap.Error(err),
			)
			return nil, nil
		}
		return nil, fmt.Errorf("transition task %s to running: %w", taskID, err)
	}

	// Fetch the full task
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task %s after claim: %w", taskID, err)
	}

	// Publish event
	_ = e.store.PublishEvent(ctx, TaskEvent{
		Type:   EventTaskClaimed,
		TaskID: taskID,
		NodeID: consumer.ID,
		Status: StatusRunning,
		At:     time.Now().UnixMilli(),
	})

	e.logger.Debug("task claimed",
		zap.String("taskID", taskID),
		zap.String("consumer", consumer.ID),
	)
	return task, nil
}

// Report submits the result of a task execution.
func (e *EngineImpl) Report(ctx context.Context, result *TaskResult) error {
	if result == nil || result.TaskID == "" {
		return fmt.Errorf("result with task ID is required")
	}

	// Set completion time if not already set
	if result.CompletedAt == 0 {
		result.CompletedAt = time.Now().UnixMilli()
	}

	// Get current task to check state
	task, err := e.store.GetTask(ctx, result.TaskID)
	if err != nil {
		return fmt.Errorf("get task for result: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task %s not found", result.TaskID)
	}

	// Handle retry logic for failed tasks
	if result.Status == StatusFailed && task.CanRetry() {
		return e.retryTask(ctx, task, result)
	}

	// Transition to terminal status
	if err := e.store.UpdateTaskStatus(ctx, result.TaskID, result.Status, ""); err != nil {
		if IsInvalidTransition(err) {
			// Task already in terminal state (race condition) — save result anyway
			e.logger.Warn("task already in terminal state, saving result",
				zap.String("taskID", result.TaskID),
				zap.Error(err),
			)
		} else {
			return err
		}
	}

	// Save result
	if err := e.store.SaveResult(ctx, result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}

	// Publish event
	eventType := EventTaskCompleted
	switch result.Status {
	case StatusFailed:
		eventType = EventTaskFailed
	case StatusTimeout:
		eventType = EventTaskTimeout
	case StatusCancelled:
		eventType = EventTaskCancelled
	}
	_ = e.store.PublishEvent(ctx, TaskEvent{
		Type:   eventType,
		TaskID: result.TaskID,
		NodeID: result.NodeID,
		Status: result.Status,
		At:     result.CompletedAt,
	})

	e.logger.Debug("task result reported",
		zap.String("taskID", result.TaskID),
		zap.String("status", string(result.Status)),
		zap.String("node", result.NodeID),
	)
	return nil
}

// retryTask re-enqueues a failed task for retry.
func (e *EngineImpl) retryTask(ctx context.Context, task *Task, result *TaskResult) error {
	e.logger.Info("retrying task",
		zap.String("taskID", task.ID),
		zap.Int("attempt", task.RetryCount+1),
		zap.Int("maxRetries", task.MaxRetries),
	)

	// Save the failure result for audit
	result.RetryCount = task.RetryCount
	if err := e.store.SaveResult(ctx, result); err != nil {
		e.logger.Warn("failed to save retry result", zap.Error(err))
	}

	// Reset status to Pending for retry
	// Note: Running → Failed is valid, then we re-submit which creates a new cycle.
	// For simplicity, we mark it as Failed first, then directly re-enqueue.
	if err := e.store.UpdateTaskStatus(ctx, task.ID, StatusFailed, ""); err != nil {
		if !IsInvalidTransition(err) {
			return err
		}
	}

	// Increment retry count and re-save task as Pending
	// Create a new entry since the original is now Failed
	retryTask := *task
	retryTask.RetryCount++
	retryTask.Status = StatusPending
	retryTask.ClaimedBy = ""
	retryTask.CreatedAt = time.Now().UnixMilli()

	// For retry, we use a different approach: update in-place via a new status cycle
	// This is simpler — directly re-enqueue to the same queue
	queueID := e.router.Route(&retryTask)
	if err := e.store.Enqueue(ctx, queueID, retryTask.ID, retryTask.Priority); err != nil {
		return fmt.Errorf("re-enqueue for retry: %w", err)
	}

	return nil
}

// ═══ Observer API ═══

func (e *EngineImpl) GetTask(ctx context.Context, taskID string) (*Task, error) {
	return e.store.GetTask(ctx, taskID)
}

func (e *EngineImpl) GetResult(ctx context.Context, taskID string) (*TaskResult, error) {
	return e.store.GetResult(ctx, taskID)
}

func (e *EngineImpl) GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error) {
	return e.store.GetProgress(ctx, taskType, groupID)
}

func (e *EngineImpl) ListTasks(ctx context.Context, query ListQuery) (*ListPage, error) {
	return e.store.ListTasks(ctx, query)
}

// ═══ Lifecycle ═══

func (e *EngineImpl) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	if err := e.store.Start(ctx); err != nil {
		return fmt.Errorf("start store: %w", err)
	}

	e.started = true
	e.logger.Info("task engine started")
	return nil
}

func (e *EngineImpl) Stop(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return nil
	}

	close(e.stopCh)
	e.wg.Wait()

	if err := e.store.Close(); err != nil {
		e.logger.Warn("error closing store", zap.Error(err))
	}

	e.started = false
	e.logger.Info("task engine stopped")
	return nil
}

// SubscribeEvents implements the TaskEventSubscriber optional interface.
// It delegates to the underlying store's SubscribeEvents method.
func (e *EngineImpl) SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error) {
	return e.store.SubscribeEvents(ctx)
}

// Ensure EngineImpl implements both Engine and TaskEventSubscriber.
var (
	_ Engine               = (*EngineImpl)(nil)
	_ TaskEventSubscriber  = (*EngineImpl)(nil)
)

// ═══ Internal helpers ═══

func (e *EngineImpl) applyDefaults(task *Task) {
	if task.Timeout == 0 {
		task.Timeout = e.defaultTimeout
	}
	if task.MaxRetries == 0 {
		task.MaxRetries = e.defaultMaxRetries
	}
	if task.Routing.Strategy == "" {
		task.Routing.Strategy = RoutingBroadcast
	}
}
