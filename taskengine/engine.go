// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import "context"

// Engine is the unified task engine interface.
// It orchestrates task submission, routing, claiming, and result reporting.
//
// The interface is designed with Interface Segregation in mind:
//   - Producers use: Submit, SubmitBatch, Cancel
//   - Consumers use: Claim, Report
//   - Observers use: GetTask, GetResult, GetProgress, ListTasks
//
// Implementations should be safe for concurrent use.
type Engine interface {
	// ═══ Producer API ═══

	// Submit enqueues a single task for processing.
	// The task is routed to the appropriate queue based on its Routing field.
	Submit(ctx context.Context, task *Task) error

	// SubmitBatch enqueues multiple tasks atomically.
	// Either all tasks are enqueued or none (best-effort for Redis).
	SubmitBatch(ctx context.Context, tasks []*Task) error

	// Cancel marks a task as cancelled and removes it from its queue.
	// Returns nil if the task was already in a terminal state.
	Cancel(ctx context.Context, taskID string) error

	// ═══ Consumer API ═══

	// Claim atomically dequeues and transitions a task to Running.
	// The consumer declares its identity and capabilities; the engine
	// determines which queues to poll based on MatchQueues.
	// Returns (nil, nil) if no tasks are available.
	Claim(ctx context.Context, consumer *ConsumerDescriptor) (*Task, error)

	// Report submits the execution result for a task.
	// This transitions the task to its terminal status and optionally
	// triggers retry logic if the task failed but has retries remaining.
	Report(ctx context.Context, result *TaskResult) error

	// ═══ Observer API ═══

	// GetTask retrieves a task by ID. Returns (nil, nil) if not found.
	GetTask(ctx context.Context, taskID string) (*Task, error)

	// GetResult retrieves the execution result for a task.
	GetResult(ctx context.Context, taskID string) (*TaskResult, error)

	// GetProgress returns aggregated progress for tasks matching the filter.
	GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error)

	// ListTasks returns a paginated list of tasks matching the query.
	ListTasks(ctx context.Context, query ListQuery) (*ListPage, error)

	// ═══ Lifecycle ═══

	// Start initializes the engine (background goroutines, connections).
	Start(ctx context.Context) error

	// Stop gracefully shuts down the engine.
	Stop(ctx context.Context) error
}

// TaskEventSubscriber is an optional interface for engines that support
// subscribing to task lifecycle events (for real-time notifications).
//
// Consumers use type assertion to check if the engine supports this:
//
//	if sub, ok := engine.(TaskEventSubscriber); ok {
//	    ch, _ := sub.SubscribeEvents(ctx)
//	}
//
// If the engine does NOT implement this interface, consumers fall back
// to timeout-based polling (graceful degradation).
type TaskEventSubscriber interface {
	// SubscribeEvents returns a channel that receives task lifecycle events.
	// The channel is closed when ctx is cancelled.
	// Callers must NOT close the returned channel.
	SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error)
}
