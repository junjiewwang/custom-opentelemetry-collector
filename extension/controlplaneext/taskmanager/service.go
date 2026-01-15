// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// TaskService provides high-level task management operations.
// It encapsulates business logic, state machine protection, and orchestration.
type TaskService struct {
	logger *zap.Logger
	config Config
	store  store.TaskStore
	helper *TaskHelper
}

// NewTaskService creates a new TaskService with the given store.
func NewTaskService(logger *zap.Logger, config Config, taskStore store.TaskStore) *TaskService {
	return &TaskService{
		logger: logger,
		config: config,
		store:  taskStore,
		helper: NewTaskHelper(),
	}
}

// Ensure TaskService implements TaskManager.
var _ TaskManager = (*TaskService)(nil)

// ===== Task Submission =====

// SubmitTask submits a task to the global queue.
func (s *TaskService) SubmitTask(ctx context.Context, task *controlplanev1.Task) error {
	return s.submitTaskInternal(ctx, nil, task)
}

// SubmitTaskForAgent submits a task for a specific agent.
func (s *TaskService) SubmitTaskForAgent(ctx context.Context, agentMeta *AgentMeta, task *controlplanev1.Task) error {
	return s.submitTaskInternal(ctx, agentMeta, task)
}

func (s *TaskService) submitTaskInternal(ctx context.Context, agentMeta *AgentMeta, task *controlplanev1.Task) error {
	// Step 1: Validate task
	nowMillis, err := s.helper.ValidateTask(task)
	if err != nil {
		return err
	}

	// Step 2: Create TaskInfo
	info := s.helper.NewTaskInfo(task, agentMeta, nowMillis)
	storeInfo := toStoreTaskInfo(info)

	// Step 3: Save to store (atomic check for duplicate)
	if err := s.store.SaveTaskInfo(ctx, storeInfo, true /* isNew */); err != nil {
		return err
	}

	// Step 4: Enqueue
	agentID := s.helper.ExtractAgentID(agentMeta)
	queueID := agentID
	if queueID == "" {
		queueID = store.QueueGlobal
	}

	if err := s.store.EnqueueTask(ctx, queueID, task.TaskID, task.Priority, task.CreatedAtMillis); err != nil {
		// Rollback: delete the saved TaskInfo
		_ = s.store.DeleteTaskInfo(ctx, task.TaskID)
		return err
	}

	// Step 5: Publish event (best effort)
	_ = s.store.PublishEvent(ctx, "submitted", task.TaskID)

	s.logger.Debug("Task submitted",
		zap.String("task_id", task.TaskID),
		zap.String("task_type", task.TaskType),
		zap.String("agent_id", agentID),
	)
	return nil
}

// ===== Task Fetching =====

// FetchTask fetches the next task for an agent.
func (s *TaskService) FetchTask(ctx context.Context, agentID string, timeout time.Duration) (*controlplanev1.Task, error) {
	deadline := time.Now().Add(timeout)

	// Queue order: agent-specific first, then global
	queueIDs := []string{agentID, store.QueueGlobal}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		remainingTimeout := time.Until(deadline)
		if remainingTimeout <= 0 {
			break
		}
		if remainingTimeout > time.Second {
			remainingTimeout = time.Second
		}

		taskID, err := s.store.DequeueTaskMulti(ctx, queueIDs, remainingTimeout)
		if err != nil {
			return nil, err
		}
		if taskID == "" {
			continue
		}

		// Check if cancelled
		cancelled, err := s.store.IsCancelled(ctx, taskID)
		if err != nil {
			s.logger.Warn("Failed to check cancelled status", zap.String("task_id", taskID), zap.Error(err))
			continue
		}
		if cancelled {
			continue
		}

		// Get task info
		info, err := s.store.GetTaskInfo(ctx, taskID)
		if err != nil {
			s.logger.Warn("Failed to get task info", zap.String("task_id", taskID), zap.Error(err))
			continue
		}
		if info == nil || info.Task == nil {
			continue
		}

		// Check if dispatchable
		if !s.helper.IsTaskInfoDispatchable(fromStoreTaskInfo(info), false) {
			continue
		}

		return info.Task, nil
	}

	return nil, nil // Timeout
}

// ===== Task Queries =====

// GetPendingTasks returns all pending tasks for an agent.
func (s *TaskService) GetPendingTasks(ctx context.Context, agentID string) ([]*controlplanev1.Task, error) {
	var tasks []*controlplanev1.Task

	// Get from agent queue
	agentTaskIDs, err := s.store.PeekQueue(ctx, agentID)
	if err != nil {
		return nil, err
	}

	for _, taskID := range agentTaskIDs {
		task, err := s.getDispatchableTask(ctx, taskID)
		if err != nil {
			continue
		}
		if task != nil {
			tasks = append(tasks, task)
		}
	}

	// Get from global queue
	globalTaskIDs, err := s.store.PeekQueue(ctx, store.QueueGlobal)
	if err != nil {
		return nil, err
	}

	for _, taskID := range globalTaskIDs {
		task, err := s.getDispatchableTask(ctx, taskID)
		if err != nil {
			continue
		}
		if task != nil {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

// GetGlobalPendingTasks returns all pending tasks in the global queue.
func (s *TaskService) GetGlobalPendingTasks(ctx context.Context) ([]*controlplanev1.Task, error) {
	taskIDs, err := s.store.PeekQueue(ctx, store.QueueGlobal)
	if err != nil {
		return nil, err
	}

	var tasks []*controlplanev1.Task
	for _, taskID := range taskIDs {
		task, err := s.getDispatchableTask(ctx, taskID)
		if err != nil {
			continue
		}
		if task != nil {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (s *TaskService) getDispatchableTask(ctx context.Context, taskID string) (*controlplanev1.Task, error) {
	cancelled, err := s.store.IsCancelled(ctx, taskID)
	if err != nil {
		return nil, err
	}

	info, err := s.store.GetTaskInfo(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if info == nil || info.Task == nil {
		return nil, nil
	}

	if !s.helper.IsTaskInfoDispatchable(fromStoreTaskInfo(info), cancelled) {
		return nil, nil
	}

	return info.Task, nil
}

// GetAllTasks returns all tasks from detail storage.
func (s *TaskService) GetAllTasks(ctx context.Context) ([]*TaskInfo, error) {
	storeInfos, err := s.store.ListTaskInfos(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*TaskInfo, 0, len(storeInfos))
	for _, si := range storeInfos {
		result = append(result, fromStoreTaskInfo(si))
	}
	return result, nil
}

// ===== Task Cancellation =====

// CancelTask cancels a task by ID.
func (s *TaskService) CancelTask(ctx context.Context, taskID string) error {
	nowMillis := s.helper.NowMillis()

	res, err := s.store.ApplyCancel(ctx, taskID, nowMillis)
	if err != nil {
		// If task not found in detail, still mark as cancelled
		if errors.Is(err, store.ErrTaskNotFound) {
			_ = s.store.SetCancelled(ctx, taskID)
			s.logger.Info("Task cancelled (no detail found)", zap.String("task_id", taskID))
			return nil
		}
		return err
	}

	if res.Code == store.ApplyTaskRejected {
		transition := ValidateStateTransition(res.Status, controlplanev1.TaskStatusCancelled)
		return NewStateTransitionError(taskID, res.Status, controlplanev1.TaskStatusCancelled, transition)
	}

	// Ensure cancelled marker is set when the final status is CANCELLED.
	if res.Status == controlplanev1.TaskStatusCancelled {
		_ = s.store.SetCancelled(ctx, taskID)
	}

	// Publish event only when we actually changed state.
	if res.Code == store.ApplyTaskUpdated {
		_ = s.store.PublishEvent(ctx, "cancelled", taskID)
		s.logger.Info("Task cancelled", zap.String("task_id", taskID))
	}

	return nil
}

// IsTaskCancelled checks if a task has been cancelled.
func (s *TaskService) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	return s.store.IsCancelled(ctx, taskID)
}

// ===== Task Result Reporting =====

// ReportTaskResult reports the result of a task execution.
// Includes state machine protection with proper idempotency handling.
func (s *TaskService) ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	// Step 1: Validate
	if err := s.helper.ValidateResult(result); err != nil {
		return err
	}

	// Step 2: Calculate effects based on status
	effects := s.helper.ResultEffects(result.Status)

	// Step 3: Apply authoritative state machine update (atomic in store)
	nowMillis := s.helper.NowMillis()
	applyRes, err := s.store.ApplyTaskResult(ctx, result.TaskID, result, nowMillis)
	if err != nil {
		// If task not found, still save the result (best effort).
		// This typically indicates store/keyPrefix mismatch across components.
		if errors.Is(err, store.ErrTaskNotFound) {
			s.logger.Warn("Task result reported but task detail not found; result saved only",
				zap.String("task_id", result.TaskID),
				zap.String("status", result.Status.String()),
				zap.String("agent_id", result.AgentID),
				zap.String("store", fmt.Sprintf("%T", s.store)),
			)
			_ = s.store.SaveResult(ctx, result)
			_ = s.store.ClearRunning(ctx, result.TaskID)
			return nil
		}
		return err
	}

	if applyRes.Code == store.ApplyTaskRejected {
		transition := ValidateStateTransition(applyRes.Status, result.Status)
		return NewStateTransitionError(result.TaskID, applyRes.Status, result.Status, transition)
	}

	// Step 4: Save result (best-effort persistence of payload)
	if err := s.store.SaveResult(ctx, result); err != nil {
		return err
	}

	// If no state change was needed, skip side effects.
	if applyRes.Code == store.ApplyTaskNoop {
		return nil
	}

	agentID := applyRes.AgentID
	if agentID == "" {
		agentID = result.AgentID
	}

	// Step 5: Apply side effects
	if effects.MarkRunning {
		_ = s.store.SetRunning(ctx, result.TaskID, agentID)
	}
	if effects.ClearRunning {
		_ = s.store.ClearRunning(ctx, result.TaskID)
	}
	if effects.RemoveFromPending {
		_ = s.store.RemoveFromAllQueues(ctx, result.TaskID, agentID)
	}
	if effects.PublishCompleted {
		_ = s.store.PublishEvent(ctx, "completed", result.TaskID)
	}

	if result.Status == controlplanev1.TaskStatusRunning {
		s.logger.Debug("Task status updated to RUNNING",
			zap.String("task_id", result.TaskID),
			zap.String("agent_id", agentID),
		)
		return nil
	}

	s.logger.Debug("Task result reported",
		zap.String("task_id", result.TaskID),
		zap.String("status", result.Status.String()),
	)

	return nil
}

// GetTaskResult retrieves the result of a task.
func (s *TaskService) GetTaskResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error) {
	return s.store.GetResult(ctx, taskID)
}

// ===== Task Status =====

// GetTaskStatus retrieves the status of a task.
func (s *TaskService) GetTaskStatus(ctx context.Context, taskID string) (*TaskInfo, error) {
	info, err := s.store.GetTaskInfo(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, s.helper.ErrTaskNotFound(taskID)
	}
	return fromStoreTaskInfo(info), nil
}

// SetTaskRunning marks a task as running by an agent.
func (s *TaskService) SetTaskRunning(ctx context.Context, taskID string, agentID string) error {
	nowMillis := s.helper.NowMillis()

	res, err := s.store.ApplySetRunning(ctx, taskID, agentID, nowMillis)
	if err != nil {
		return err
	}

	if res.Code == store.ApplyTaskRejected {
		transition := ValidateStateTransition(res.Status, controlplanev1.TaskStatusRunning)
		return NewStateTransitionError(taskID, res.Status, controlplanev1.TaskStatusRunning, transition)
	}

	if res.Code == store.ApplyTaskNoop {
		return nil
	}

	// Update running state
	return s.store.SetRunning(ctx, taskID, agentID)
}

// ===== Lifecycle =====

// Start initializes the task service.
func (s *TaskService) Start(ctx context.Context) error {
	s.logger.Info("Starting task service")
	return s.store.Start(ctx)
}

// Close releases resources.
func (s *TaskService) Close() error {
	return s.store.Close()
}

// ===== Type Conversion Helpers =====

// toStoreTaskInfo converts TaskInfo to store.TaskInfo.
func toStoreTaskInfo(info *TaskInfo) *store.TaskInfo {
	if info == nil {
		return nil
	}
	return &store.TaskInfo{
		Task:                info.Task,
		Status:              info.Status,
		AgentID:             info.AgentID,
		AppID:               info.AppID,
		ServiceName:         info.ServiceName,
		CreatedAtMillis:     info.CreatedAtMillis,
		StartedAtMillis:     info.StartedAtMillis,
		Result:              info.Result,
		Version:             0,
		LastUpdatedAtMillis: 0,
	}
}

// fromStoreTaskInfo converts store.TaskInfo to TaskInfo.
func fromStoreTaskInfo(info *store.TaskInfo) *TaskInfo {
	if info == nil {
		return nil
	}
	return &TaskInfo{
		Task:            info.Task,
		Status:          info.Status,
		AgentID:         info.AgentID,
		AppID:           info.AppID,
		ServiceName:     info.ServiceName,
		CreatedAtMillis: info.CreatedAtMillis,
		StartedAtMillis: info.StartedAtMillis,
		Result:          info.Result,
	}
}
