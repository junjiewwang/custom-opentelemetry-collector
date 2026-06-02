// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
)

// LocalCoordinator implements TaskCoordinator for single-node mode.
// All tasks are executed in-process with no external coordination.
//
// Used when:
//   - Only one collector node is running
//   - Redis is unavailable (graceful degradation)
//   - Task count is below the distributed threshold
type LocalCoordinator struct {
	mu       sync.Mutex
	tasks    []PurgeTask
	taskMeta map[string]PurgeTask // original task metadata by ID (for retry)
	results  map[string]TaskResult
	epoch    int64
	total    int
}

// Compile-time interface satisfaction check.
var _ TaskCoordinator = (*LocalCoordinator)(nil)
var _ RetryableCoordinator = (*LocalCoordinator)(nil)

// NewLocalCoordinator creates a new single-node coordinator.
func NewLocalCoordinator() *LocalCoordinator {
	return &LocalCoordinator{
		results:  make(map[string]TaskResult),
		taskMeta: make(map[string]PurgeTask),
	}
}

func (c *LocalCoordinator) TryBecomeLeader(_ context.Context) (bool, error) {
	return true, nil // always leader in local mode
}

func (c *LocalCoordinator) ReleaseLeader(_ context.Context) error {
	return nil
}

func (c *LocalCoordinator) SubmitTasks(_ context.Context, epoch int64, tasks []PurgeTask) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.epoch != epoch {
		// New epoch: full reset
		c.epoch = epoch
		c.tasks = make([]PurgeTask, len(tasks))
		copy(c.tasks, tasks)
		c.results = make(map[string]TaskResult, len(tasks))
		c.total = len(tasks)
		c.taskMeta = make(map[string]PurgeTask)
	} else {
		// Same epoch (retry): append tasks, remove old results for retried tasks.
		// Do NOT increase total — retries replace existing failed attempts.
		c.tasks = append(c.tasks, tasks...)
		for i := range tasks {
			delete(c.results, tasks[i].ID)
		}
	}

	// Track task metadata for retry support
	for i := range tasks {
		c.taskMeta[tasks[i].ID] = tasks[i]
	}
	return nil
}

func (c *LocalCoordinator) ClaimTask(_ context.Context, _ int64) (*PurgeTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tasks) == 0 {
		return nil, nil
	}
	// Pop from the end (stack-like, efficient slice operation)
	task := c.tasks[len(c.tasks)-1]
	c.tasks = c.tasks[:len(c.tasks)-1]
	return &task, nil
}

func (c *LocalCoordinator) ReportResult(_ context.Context, _ int64, taskID string, result TaskResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[taskID] = result
	return nil
}

func (c *LocalCoordinator) GetProgress(_ context.Context, _ int64) (*PurgeProgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	failed := 0
	completed := 0
	for _, r := range c.results {
		switch r.Status {
		case TaskStatusFailed, TaskStatusTimeout:
			failed++
		default:
			completed++
		}
	}

	status := "executing"
	if len(c.tasks) == 0 && len(c.results) >= c.total {
		status = "done"
	}

	return &PurgeProgress{
		Epoch:      c.epoch,
		TotalTasks: c.total,
		Completed:  completed,
		Failed:     failed,
		Remaining:  len(c.tasks),
		Status:     status,
	}, nil
}

func (c *LocalCoordinator) GetActiveEpoch(_ context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tasks) > 0 || (c.total > 0 && len(c.results) < c.total) {
		return c.epoch, nil
	}
	return 0, nil
}

func (c *LocalCoordinator) CompleteEpoch(_ context.Context, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tasks = nil
	c.results = make(map[string]TaskResult)
	c.taskMeta = make(map[string]PurgeTask)
	c.total = 0
	c.epoch = 0
	return nil
}

// GetFailedTasks returns tasks that failed and are eligible for retry.
// Implements RetryableCoordinator.
func (c *LocalCoordinator) GetFailedTasks(_ context.Context, _ int64, maxRetries int) ([]PurgeTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var retryable []PurgeTask
	for taskID, result := range c.results {
		if result.Status == TaskStatusFailed || result.Status == TaskStatusTimeout {
			if task, exists := c.taskMeta[taskID]; exists && task.Retry < maxRetries {
				retryable = append(retryable, task)
			}
		}
	}
	return retryable, nil
}
