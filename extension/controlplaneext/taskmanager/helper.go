// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"errors"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
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
func (h *TaskHelper) ValidateTask(task *controlplanev1.Task) (nowMillis int64, err error) {
	if task == nil {
		return 0, errors.New("task cannot be nil")
	}
	if task.TaskID == "" {
		return 0, errors.New("task_id is required")
	}
	if task.TaskType == "" {
		return 0, errors.New("task_type is required")
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

// NewTaskInfo creates a TaskInfo with standard initialization.
func (h *TaskHelper) NewTaskInfo(task *controlplanev1.Task, agentID string, nowMillis int64) *TaskInfo {
	return &TaskInfo{
		Task:            task,
		Status:          controlplanev1.TaskStatusPending,
		AgentID:         agentID,
		CreatedAtMillis: nowMillis,
	}
}
