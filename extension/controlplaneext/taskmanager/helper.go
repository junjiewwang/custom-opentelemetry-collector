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

// The following helpers were removed as dead code (no callers in production or
// tests; the engines implement state transitions directly):
//   NewTaskInfo, TaskResultEffects, ResultEffects, UpdateTaskInfoWithResult,
//   EnsureStartedAtMillis, IsTaskInfoDispatchable, ResolveAgentID,
//   MarkTaskInfoRunning, MarkTaskInfoCancelled, isTerminal

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

// ErrTaskNotFound returns a standardized "task not found" error.
func (h *TaskHelper) ErrTaskNotFound(taskID string) error {
	return errors.New("task not found: " + taskID)
}
