// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// QueueGlobal is the queue ID for the global pending queue.
const QueueGlobal = ""

// TaskInfo contains detailed task information.
type TaskInfo struct {
	Task            *model.Task       `json:"task"`
	Status          model.TaskStatus  `json:"status"`
	AgentID         string            `json:"agent_id,omitempty"`
	AppID           string            `json:"app_id,omitempty"`
	ServiceName     string            `json:"service_name,omitempty"`
	CreatedAtMillis int64             `json:"created_at_millis"`
	StartedAtMillis int64             `json:"started_at_millis,omitempty"`
	Result          *model.TaskResult `json:"result,omitempty"`

	// Version is incremented on each status update for optimistic concurrency.
	Version int64 `json:"version,omitempty"`

	// LastUpdatedAtMillis records when the status was last changed.
	LastUpdatedAtMillis int64 `json:"last_updated_at_millis,omitempty"`
}

// TaskEvent represents a task-related event.
type TaskEvent struct {
	Type   string // "submitted", "completed", "cancelled"
	TaskID string
}

// ErrTaskNotFound is returned when a task detail record does not exist.
var ErrTaskNotFound = errors.New("task not found")

// TaskNotFound wraps ErrTaskNotFound with a task ID context.
func TaskNotFound(taskID string) error {
	return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
}

// ApplyTaskUpdateCode describes the result of an atomic task detail update.
// It is used by "权威状态机" APIs to avoid optimistic lock retries.
type ApplyTaskUpdateCode int32

const (
	// ApplyTaskUpdated indicates the task detail was updated.
	ApplyTaskUpdated ApplyTaskUpdateCode = 1
	// ApplyTaskNoop indicates the update was accepted but no state change was needed.
	ApplyTaskNoop ApplyTaskUpdateCode = 2
	// ApplyTaskRejected indicates the update was rejected (invalid transition for this operation).
	ApplyTaskRejected ApplyTaskUpdateCode = -2
)

// ApplyTaskUpdateResult carries the outcome and the task's current status snapshot.
type ApplyTaskUpdateResult struct {
	Code    ApplyTaskUpdateCode
	Status  model.TaskStatus
	AgentID string
}

// IsTerminalStatus returns true if the status is a terminal state.
func IsTerminalStatus(status model.TaskStatus) bool {
	switch status {
	case model.TaskStatusSuccess, model.TaskStatusFailed,
		model.TaskStatusTimeout, model.TaskStatusCancelled, model.TaskStatusResultTooLarge:
		return true
	default:
		return false
	}
}

// TaskStore defines the authoritative storage boundary for task management.
//
// It is more than a CRUD repository: implementations are responsible for
// persistence, backend-specific atomicity, and applying shared task state
// machine semantics consistently. Business orchestration (for example reaping,
// retries, event policy, and workflow decisions) still belongs in upper layers
// such as `TaskService` and `StaleTaskReaper`.
type TaskStore interface {

	// ===== Task Detail Operations =====

	// SaveTaskInfo persists a TaskInfo.
	// If isNew is true, returns error if task already exists.
	SaveTaskInfo(ctx context.Context, info *TaskInfo, isNew bool) error

	// GetTaskInfo retrieves a TaskInfo by taskID.
	// Returns (nil, nil) if not found.
	GetTaskInfo(ctx context.Context, taskID string) (*TaskInfo, error)

	// UpdateTaskInfo atomically updates a TaskInfo using the provided updater function.
	// The updater receives the current TaskInfo and should modify it in place.
	// Returns ErrTaskNotFound if task doesn't exist.
	UpdateTaskInfo(ctx context.Context, taskID string, updater func(*TaskInfo) error) error

	// ===== Atomic State Machine Operations (Authoritative) =====
	// These APIs apply state machine logic atomically inside the store backend.
	// They are designed to avoid optimistic lock retry storms under concurrency.

	// ApplyTaskResult applies a task result update to the task detail record.
	// Semantics: once the task reaches a terminal state, further updates become a no-op.
	ApplyTaskResult(ctx context.Context, taskID string, result *model.TaskResult, nowMillis int64) (ApplyTaskUpdateResult, error)

	// ApplyCancel marks a task as cancelled in the task detail record.
	// Semantics: cancelling a non-cancelled terminal task is rejected.
	ApplyCancel(ctx context.Context, taskID string, nowMillis int64) (ApplyTaskUpdateResult, error)

	// ApplySetRunning marks a task as running in the task detail record.
	// Semantics: setting running on a terminal task is rejected.
	ApplySetRunning(ctx context.Context, taskID string, agentID string, nowMillis int64) (ApplyTaskUpdateResult, error)

	// ListTaskInfos returns all TaskInfos.
	ListTaskInfos(ctx context.Context) ([]*TaskInfo, error)

	// ListTaskInfosPage returns a filtered and paged task list for read-model queries.
	ListTaskInfosPage(ctx context.Context, query TaskListQuery) (TaskListPage, error)

	// DeleteTaskInfo removes a TaskInfo.
	DeleteTaskInfo(ctx context.Context, taskID string) error

	// ===== Queue Operations =====

	// EnqueueTask adds a task to the pending queue.
	// queueID: QueueGlobal ("") for global queue, or agentID for agent-specific queue.
	EnqueueTask(ctx context.Context, queueID string, taskID string, priority int32, createdAtMillis int64) error

	// DequeueTask removes and returns the next task ID from the queue.
	// Returns ("", nil) on timeout.
	DequeueTask(ctx context.Context, queueID string, timeout time.Duration) (string, error)

	// DequeueTaskMulti tries multiple queues in order.
	// Returns the first available task ID, or ("", nil) on timeout.
	DequeueTaskMulti(ctx context.Context, queueIDs []string, timeout time.Duration) (string, error)

	// PeekQueue returns all task IDs in a queue without removing them.
	PeekQueue(ctx context.Context, queueID string) ([]string, error)

	// RemoveFromQueue removes a specific task from a queue.
	RemoveFromQueue(ctx context.Context, queueID string, taskID string) error

	// RemoveFromAllQueues removes a task from global queue and agent-specific queue.
	RemoveFromAllQueues(ctx context.Context, taskID string, agentID string) error

	// ===== Result Operations =====

	// SaveResult persists a TaskResult.
	SaveResult(ctx context.Context, result *model.TaskResult) error

	// GetResult retrieves a TaskResult by taskID.
	// Returns (nil, false, nil) if not found.
	GetResult(ctx context.Context, taskID string) (*model.TaskResult, bool, error)

	// ===== Cancellation Operations =====

	// SetCancelled marks a task as cancelled.
	SetCancelled(ctx context.Context, taskID string) error

	// IsCancelled checks if a task is cancelled.
	IsCancelled(ctx context.Context, taskID string) (bool, error)

	// ===== Running State Operations =====

	// SetRunning marks a task as running by an agent.
	SetRunning(ctx context.Context, taskID string, agentID string) error

	// GetRunning returns the agentID running a task, or "" if not running.
	GetRunning(ctx context.Context, taskID string) (string, error)

	// ListRunningTaskInfos returns task details for tasks currently tracked as RUNNING.
	// Implementations may best-effort clean stale running markers whose detail is missing
	// or no longer in RUNNING state.
	ListRunningTaskInfos(ctx context.Context) ([]*TaskInfo, error)

	// ClearRunning removes the running marker for a task.
	ClearRunning(ctx context.Context, taskID string) error

	// ===== Event Operations =====

	// PublishEvent publishes an event (e.g., task submitted/completed/cancelled).
	// No-op for backends that don't support pub/sub.
	PublishEvent(ctx context.Context, eventType string, taskID string) error

	// ===== Lifecycle =====

	// Start initializes the store.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}
