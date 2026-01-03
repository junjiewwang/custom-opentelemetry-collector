// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// TaskManager defines the interface for task management.
type TaskManager interface {
	// SubmitTask submits a task to the queue.
	SubmitTask(ctx context.Context, task *controlplanev1.Task) error

	// SubmitTaskForAgent submits a task for a specific agent.
	SubmitTaskForAgent(ctx context.Context, agentMeta *AgentMeta, task *controlplanev1.Task) error

	// FetchTask fetches the next task for an agent (blocking with timeout).
	FetchTask(ctx context.Context, agentID string, timeout time.Duration) (*controlplanev1.Task, error)

	// GetPendingTasks returns all pending tasks for an agent.
	GetPendingTasks(ctx context.Context, agentID string) ([]*controlplanev1.Task, error)

	// GetGlobalPendingTasks returns all pending tasks in the global queue.
	GetGlobalPendingTasks(ctx context.Context) ([]*controlplanev1.Task, error)

	// GetAllTasks returns all tasks (from detail storage, including all statuses).
	GetAllTasks(ctx context.Context) ([]*TaskInfo, error)

	// CancelTask cancels a task by ID.
	CancelTask(ctx context.Context, taskID string) error

	// IsTaskCancelled checks if a task has been cancelled.
	IsTaskCancelled(ctx context.Context, taskID string) (bool, error)

	// ReportTaskResult reports the result of a task execution.
	ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error

	// GetTaskResult retrieves the result of a task.
	GetTaskResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error)

	// GetTaskStatus retrieves the status of a task.
	GetTaskStatus(ctx context.Context, taskID string) (*TaskInfo, error)

	// SetTaskRunning marks a task as running by an agent.
	SetTaskRunning(ctx context.Context, taskID string, agentID string) error

	// Start initializes the task manager.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// TaskInfo contains detailed task information.
type TaskInfo struct {
	Task            *controlplanev1.Task       `json:"task"`
	Status          controlplanev1.TaskStatus  `json:"status"`
	AgentID         string                     `json:"agent_id,omitempty"`
	AppID           string                     `json:"app_id,omitempty"`
	ServiceName     string                     `json:"service_name,omitempty"`
	CreatedAtMillis int64                      `json:"created_at_millis"`
	StartedAtMillis int64                      `json:"started_at_millis,omitempty"`
	Result          *controlplanev1.TaskResult `json:"result,omitempty"`
}

// Config holds the configuration for TaskManager.
type Config struct {
	// Type specifies the backend type: "memory" or "redis"
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from storage extension
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for all Redis keys
	KeyPrefix string `mapstructure:"key_prefix"`

	// ResultTTL is the TTL for task results
	ResultTTL time.Duration `mapstructure:"result_ttl"`

	// Workers is the number of worker goroutines for local task execution.
	Workers int `mapstructure:"workers"`

	// QueueSize is the maximum number of tasks in the local queue.
	QueueSize int `mapstructure:"queue_size"`

	// DefaultTimeout is the default task execution timeout.
	DefaultTimeout time.Duration `mapstructure:"default_timeout"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:           "memory",
		RedisName:      "default",
		KeyPrefix:      "otel:tasks",
		ResultTTL:      24 * time.Hour,
		Workers:        4,
		QueueSize:      100,
		DefaultTimeout: 30 * time.Second,
	}
}
