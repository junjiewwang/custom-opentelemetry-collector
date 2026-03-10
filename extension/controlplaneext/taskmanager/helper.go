// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// TaskHelper provides common task operations shared across implementations.
type TaskHelper struct{}

// NewTaskHelper creates a new TaskHelper instance.
func NewTaskHelper() *TaskHelper {
	return &TaskHelper{}
}

// NowMillis returns the current timestamp in milliseconds.
func (h *TaskHelper) NowMillis() int64 {
	return time.Now().UnixMilli()
}

// ValidateTask validates task fields and auto-fills defaults.
// Returns the current timestamp (millis) for reuse.
func (h *TaskHelper) ValidateTask(task *model.Task) (nowMillis int64, err error) {
	if task == nil {
		return 0, errors.New("task cannot be nil")
	}
	if task.ID == "" {
		return 0, errors.New("task_id is required")
	}
	if task.TypeName == "" {
		return 0, errors.New("task_type_name is required")
	}

	nowMillis = h.NowMillis()

	// Auto-fill created_at if not set
	if task.CreatedAtMillis == 0 {
		task.CreatedAtMillis = nowMillis
	}

	// Check if task is expired
	if task.ExpiresAtMillis > 0 && nowMillis > task.ExpiresAtMillis {
		return 0, errors.New("task has expired")
	}

	return nowMillis, nil
}

// AgentMeta contains agent metadata for task association.
type AgentMeta struct {
	AgentID     string
	AppID       string
	ServiceName string
}

// NewTaskInfo creates a TaskInfo with standard initialization.
func (h *TaskHelper) NewTaskInfo(task *model.Task, agentMeta *AgentMeta, nowMillis int64) *TaskInfo {
	info := &TaskInfo{
		Task:            task,
		Status:          model.TaskStatusPending,
		CreatedAtMillis: nowMillis,
	}
	if agentMeta != nil {
		info.AgentID = agentMeta.AgentID
		info.AppID = agentMeta.AppID
		info.ServiceName = agentMeta.ServiceName
	}
	return info
}

// TaskResultEffects describes which side effects the task manager should apply
// when a TaskResult is reported.
//
// This is intentionally backend-agnostic: different implementations have
// different storage primitives, but they should share the same status semantics.
type TaskResultEffects struct {
	// MarkRunning indicates the task has entered RUNNING state and should be
	// tracked as running.
	MarkRunning bool
	// ClearRunning indicates any running-tracking should be cleared for this task.
	ClearRunning bool
	// RemoveFromPending indicates the task should be removed from pending queues
	// to avoid re-dispatch (e.g. once RUNNING or terminal).
	RemoveFromPending bool
	// PublishCompleted indicates the "completed" event should be published.
	PublishCompleted bool
}

// ResultEffects returns how a reported TaskResult should affect task bookkeeping.
func (h *TaskHelper) ResultEffects(status model.TaskStatus) TaskResultEffects {
	_ = h // keep method receiver for future extensions
	if status == model.TaskStatusRunning {
		return TaskResultEffects{
			MarkRunning:       true,
			RemoveFromPending: true,
		}
	}
	if isTerminal(status) {
		return TaskResultEffects{
			ClearRunning:      true,
			RemoveFromPending: true,
			PublishCompleted:  true,
		}
	}
	// For any other status, keep conservative behavior: clear running marker.
	return TaskResultEffects{ClearRunning: true}
}

// UpdateTaskInfoWithResult updates TaskInfo fields based on the reported result.
// This centralizes the update logic to ensure consistency across implementations.
func (h *TaskHelper) UpdateTaskInfoWithResult(info *TaskInfo, result *model.TaskResult) {
	if info == nil || result == nil {
		return
	}

	info.Status = result.Status
	info.Result = result

	if result.AgentID != "" {
		info.AgentID = result.AgentID
	}
}

// EnsureStartedAtMillis sets StartedAtMillis if it's missing.
func (h *TaskHelper) EnsureStartedAtMillis(info *TaskInfo, nowMillis int64) {
	if info == nil {
		return
	}
	if info.StartedAtMillis == 0 {
		info.StartedAtMillis = nowMillis
	}
}

// IsTaskInfoDispatchable checks if a task should be dispatched to agents.
// A task is dispatchable only if:
// - It is not cancelled
// - Its status is PENDING (not RUNNING or terminal)
// RUNNING tasks must NOT be re-dispatched to avoid duplicate execution.
func (h *TaskHelper) IsTaskInfoDispatchable(info *TaskInfo, isCancelled bool) bool {
	if isCancelled {
		return false
	}
	if info == nil {
		// If no info found, assume dispatchable (shouldn't happen normally)
		return true
	}
	// Only PENDING tasks are dispatchable.
	return info.Status == model.TaskStatusPending
}

// ValidateResult validates a TaskResult before processing.
func (h *TaskHelper) ValidateResult(result *model.TaskResult) error {
	if result == nil {
		return errors.New("result cannot be nil")
	}
	return nil
}

// ExtractAgentID extracts the agent ID from AgentMeta, returning empty string if nil.
func (h *TaskHelper) ExtractAgentID(agentMeta *AgentMeta) string {
	if agentMeta != nil {
		return agentMeta.AgentID
	}
	return ""
}

// ResolveAgentID determines the effective agent ID after a result update.
// It prefers the new agentID from result, but falls back to the previous one if empty.
func (h *TaskHelper) ResolveAgentID(agentIDBefore string, agentIDAfter string) string {
	if agentIDAfter != "" {
		return agentIDAfter
	}
	return agentIDBefore
}

// MarkTaskInfoRunning updates TaskInfo fields when marking a task as running.
func (h *TaskHelper) MarkTaskInfoRunning(info *TaskInfo, agentID string, nowMillis int64) {
	if info == nil {
		return
	}
	info.Status = model.TaskStatusRunning
	info.AgentID = agentID
	info.StartedAtMillis = nowMillis
}

// MarkTaskInfoCancelled updates TaskInfo status to cancelled.
func (h *TaskHelper) MarkTaskInfoCancelled(info *TaskInfo) {
	if info == nil {
		return
	}
	info.Status = model.TaskStatusCancelled
}

// ErrTaskNotFound returns a standardized "task not found" error.
func (h *TaskHelper) ErrTaskNotFound(taskID string) error {
	return errors.New("task not found: " + taskID)
}
