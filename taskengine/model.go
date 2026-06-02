// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package taskengine provides the unified task engine that replaces both
// controlplane/taskmanager and lifecycle/coordinator with a single model,
// state machine, queue, store, and routing system.
//
// Architecture: L1(Node) → L2(Store) → L3(Transport) → L4(Engine) → L5(Application)
package taskengine

import (
	"encoding/json"
	"time"

	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// ─── Task Type ───

// TaskType identifies the domain and action of a task.
// Convention: "{domain}:{action}" for easy classification and routing.
type TaskType string

// Lifecycle domain task types.
const (
	TaskTypePurgeIndex   TaskType = "lifecycle:purge_index"
	TaskTypeArchiveIndex TaskType = "lifecycle:archive_index" // reserved
	TaskTypeCompactIndex TaskType = "lifecycle:compact_index" // reserved
)

// Arthas/Controlplane domain task types.
const (
	TaskTypeArthasAttach       TaskType = "arthas:attach"
	TaskTypeArthasDetach       TaskType = "arthas:detach"
	TaskTypeArthasExecSync     TaskType = "arthas:exec_sync"
	TaskTypeArthasSessionOpen  TaskType = "arthas:session_open"
	TaskTypeArthasSessionExec  TaskType = "arthas:session_exec"
	TaskTypeArthasSessionPull  TaskType = "arthas:session_pull"
	TaskTypeArthasSessionClose TaskType = "arthas:session_close"
)

// Instrumentation domain task types.
const (
	TaskTypeInstrApply  TaskType = "instrumentation:apply"  // reserved
	TaskTypeInstrRemove TaskType = "instrumentation:remove" // reserved
)

// ─── Routing ───

// RoutingStrategy determines how a task is dispatched to consumers.
type RoutingStrategy string

const (
	// RoutingDirect sends the task to a specific node/agent by ID.
	RoutingDirect RoutingStrategy = "direct"
	// RoutingCapability sends the task to any node with matching capabilities.
	RoutingCapability RoutingStrategy = "capability"
	// RoutingBroadcast sends the task to the global queue (any consumer can pick it up).
	RoutingBroadcast RoutingStrategy = "broadcast"
)

// TaskRouting declares how a task should be routed to consumers.
type TaskRouting struct {
	// Strategy selects the routing algorithm.
	Strategy RoutingStrategy `json:"strategy"`

	// TargetNodeID is used with RoutingDirect — routes to a specific node.
	TargetNodeID string `json:"targetNodeId,omitempty"`

	// RequiredCapabilities is used with RoutingCapability —
	// task goes to a queue monitored by nodes with these capabilities.
	RequiredCapabilities []node.Capability `json:"requiredCapabilities,omitempty"`
}

// ─── Task Model ───

// Task is the unified task model replacing both controlplane model.Task and lifecycle PurgeTask.
type Task struct {
	// ID is the unique identifier for this task.
	ID string `json:"id"`

	// Type identifies the domain and action.
	Type TaskType `json:"type"`

	// Payload carries business-specific parameters as opaque JSON.
	// Each domain deserializes it to its own struct.
	Payload json.RawMessage `json:"payload"`

	// Priority determines dequeue order (higher = processed first).
	// 0 is the default priority.
	Priority int32 `json:"priority"`

	// CreatedAt is the task creation timestamp (unix milliseconds).
	CreatedAt int64 `json:"createdAt"`

	// ExpiresAt is an optional deadline after which the task is discarded.
	// 0 means no expiration.
	ExpiresAt int64 `json:"expiresAt,omitempty"`

	// Timeout is the maximum execution duration for a single attempt.
	Timeout time.Duration `json:"timeout"`

	// MaxRetries is the maximum number of retry attempts after failure.
	MaxRetries int `json:"maxRetries"`

	// RetryCount tracks how many times this task has been retried.
	RetryCount int `json:"retryCount"`

	// Status is the current lifecycle status of the task.
	Status TaskStatus `json:"status"`

	// ClaimedBy records which node is currently executing this task.
	ClaimedBy string `json:"claimedBy,omitempty"`

	// Routing declares how this task should be dispatched.
	Routing TaskRouting `json:"routing"`

	// GroupID associates this task with a batch/epoch (lifecycle uses epoch, controlplane optional).
	GroupID string `json:"groupId,omitempty"`

	// Metadata carries optional key-value pairs for extensibility.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// IsExpired returns true if the task has passed its expiration time.
func (t *Task) IsExpired() bool {
	if t.ExpiresAt == 0 {
		return false
	}
	return time.Now().UnixMilli() > t.ExpiresAt
}

// CanRetry returns true if the task has retry attempts remaining.
func (t *Task) CanRetry() bool {
	return t.RetryCount < t.MaxRetries
}

// ─── Task Result ───

// TaskResult records the outcome of a task execution.
type TaskResult struct {
	// TaskID links back to the original task.
	TaskID string `json:"taskId"`

	// NodeID identifies which node executed this task.
	NodeID string `json:"nodeId"`

	// Status is the terminal status after execution.
	Status TaskStatus `json:"status"`

	// Output carries business-specific result data as opaque JSON.
	Output json.RawMessage `json:"output,omitempty"`

	// Error contains a human-readable error message on failure.
	Error string `json:"error,omitempty"`

	// StartedAt is when execution began (unix milliseconds).
	StartedAt int64 `json:"startedAt"`

	// CompletedAt is when execution finished (unix milliseconds).
	CompletedAt int64 `json:"completedAt"`

	// RetryCount records which retry attempt produced this result.
	RetryCount int `json:"retryCount"`
}

// ─── Task Status ───

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	// StatusPending — task is queued and waiting to be claimed.
	StatusPending TaskStatus = "pending"
	// StatusRunning — task has been claimed and is executing.
	StatusRunning TaskStatus = "running"
	// StatusSuccess — task completed successfully.
	StatusSuccess TaskStatus = "success"
	// StatusFailed — task failed (may be retried if retries remain).
	StatusFailed TaskStatus = "failed"
	// StatusTimeout — task exceeded its execution timeout.
	StatusTimeout TaskStatus = "timeout"
	// StatusSkipped — task was skipped (e.g., idempotent — target already deleted).
	StatusSkipped TaskStatus = "skipped"
	// StatusCancelled — task was explicitly cancelled.
	StatusCancelled TaskStatus = "cancelled"
)

// IsTerminal returns true if this status is a final state (no further transitions).
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case StatusSuccess, StatusFailed, StatusTimeout, StatusSkipped, StatusCancelled:
		return true
	default:
		return false
	}
}

// ─── Progress ───

// Progress aggregates task status counts for a given filter.
type Progress struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Completed int `json:"completed"` // success + skipped
	Failed    int `json:"failed"`
	Timeout   int `json:"timeout"`
	Cancelled int `json:"cancelled"`
}

// IsAllDone returns true if no tasks are pending or running.
func (p *Progress) IsAllDone() bool {
	return p.Pending == 0 && p.Running == 0
}

// ─── List/Query Types ───

// ListQuery specifies pagination and filter parameters for listing tasks.
type ListQuery struct {
	// TaskType filters by type (empty = all types).
	TaskType TaskType
	// Status filters by status (empty = all statuses).
	Status TaskStatus
	// GroupID filters by group/epoch.
	GroupID string
	// Offset for pagination.
	Offset int
	// Limit for pagination (0 = default 100).
	Limit int
}

// ListPage is a paginated result.
type ListPage struct {
	Tasks  []*Task `json:"tasks"`
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Limit  int     `json:"limit"`
}

// ─── Events ───

// TaskEventType identifies what happened to a task.
type TaskEventType string

const (
	EventTaskSubmitted TaskEventType = "submitted"
	EventTaskClaimed   TaskEventType = "claimed"
	EventTaskCompleted TaskEventType = "completed"
	EventTaskFailed    TaskEventType = "failed"
	EventTaskCancelled TaskEventType = "cancelled"
	EventTaskTimeout   TaskEventType = "timeout"
)

// TaskEvent is emitted when a task changes state (for Pub/Sub notifications).
type TaskEvent struct {
	Type   TaskEventType `json:"type"`
	TaskID string        `json:"taskId"`
	NodeID string        `json:"nodeId,omitempty"`
	Status TaskStatus    `json:"status"`
	At     int64         `json:"at"` // unix millis
}

// ─── Consumer Descriptor ───

// ConsumerDescriptor identifies a task consumer (Collector Node or Remote Agent).
// Used by Engine.Claim() to determine which queues to poll.
type ConsumerDescriptor struct {
	// ID is the unique identifier of the consumer (nodeID or agentID).
	ID string `json:"id"`
	// Roles are the consumer's declared roles.
	Roles []node.Role `json:"roles"`
	// Capabilities is the consumer's effective capability set.
	Capabilities *node.CapabilitySet `json:"capabilities"`
}
