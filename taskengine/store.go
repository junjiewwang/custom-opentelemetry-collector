// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import "context"

// Store abstracts the persistence layer for the task engine.
// It handles task CRUD, queue operations, result storage, and event publishing.
//
// Implementations:
//   - RedisStore:  production multi-node via Redis (LIST + HASH + Pub/Sub)
//   - MemoryStore: single-node / testing via in-memory structures
type Store interface {
	// ─── Task CRUD ───

	// SaveTask persists a new task. Returns error if task ID already exists.
	SaveTask(ctx context.Context, task *Task) error

	// GetTask retrieves a task by ID. Returns nil, nil if not found.
	GetTask(ctx context.Context, taskID string) (*Task, error)

	// GetTasks retrieves multiple tasks by ID in a single batch operation.
	// Implementations should use MGet (Redis) or equivalent to minimize round-trips.
	// Missing/unavailable tasks are silently omitted from the result.
	GetTasks(ctx context.Context, taskIDs []string) ([]*Task, error)

	// UpdateTaskStatus atomically transitions the task to a new status.
	// Returns InvalidTransitionError if the state machine rejects the transition.
	// claimedBy is set when transitioning to Running.
	UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, claimedBy string) error

	// DeleteTask removes a task and its associated queue entries.
	DeleteTask(ctx context.Context, taskID string) error

	// ListTasks returns a paginated list of tasks matching the query.
	ListTasks(ctx context.Context, query ListQuery) (*ListPage, error)

	// ─── Queue Operations ───

	// Enqueue adds a task ID to the specified queue.
	// Priority is used for ordering (higher priority = dequeued first).
	Enqueue(ctx context.Context, queueID string, taskID string, priority int32) error

	// Dequeue atomically removes and returns the next task ID from the first
	// non-empty queue in the given list. Returns ("", nil) if all queues are empty.
	Dequeue(ctx context.Context, queueIDs []string) (string, error)

	// RemoveFromQueue removes a specific task ID from a queue (e.g., on cancellation).
	RemoveFromQueue(ctx context.Context, queueID string, taskID string) error

	// ─── Result Storage ───

	// SaveResult persists a task execution result.
	SaveResult(ctx context.Context, result *TaskResult) error

	// GetResult retrieves the result for a task. Returns nil, nil if not found.
	GetResult(ctx context.Context, taskID string) (*TaskResult, error)

	// ─── Progress ───

	// GetProgress computes aggregated progress for tasks matching the filter.
	GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error)

	// ─── Events ───

	// PublishEvent publishes a task lifecycle event for Pub/Sub listeners.
	PublishEvent(ctx context.Context, event TaskEvent) error

	// SubscribeEvents returns a channel that receives task lifecycle events from all nodes.
	// The channel is closed when ctx is cancelled or the store is closed.
	// Callers must NOT close the returned channel.
	//
	// Implementations:
	//   - RedisStore: PSubscribe {prefix}:events:* with JSON unmarshaling
	//   - MemoryStore: in-process channel fan-out from PublishEvent
	SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error)

	// ─── Lifecycle ───

	// Start initializes store resources (connections, background goroutines).
	Start(ctx context.Context) error

	// Close releases all store resources.
	Close() error
}
