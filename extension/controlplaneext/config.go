// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"errors"
	"time"
)

// Config defines the configuration for the control plane extension.
type Config struct {
	// AgentID is the unique identifier for this agent instance.
	// If empty, a UUID will be generated.
	AgentID string `mapstructure:"agent_id"`

	// Store configuration for persisting state.
	Store StoreConfig `mapstructure:"store"`

	// TaskExecutor configuration.
	TaskExecutor TaskExecutorConfig `mapstructure:"task_executor"`

	// StatusReporter configuration.
	StatusReporter StatusReporterConfig `mapstructure:"status_reporter"`
}

// StoreConfig defines the storage backend configuration.
type StoreConfig struct {
	// Type of storage backend: "memory" or "file".
	Type string `mapstructure:"type"`

	// Path for file-based storage.
	Path string `mapstructure:"path"`
}

// TaskExecutorConfig defines task executor settings.
type TaskExecutorConfig struct {
	// Number of worker goroutines for task execution.
	Workers int `mapstructure:"workers"`

	// Maximum number of tasks in the queue.
	QueueSize int `mapstructure:"queue_size"`

	// Default timeout for task execution.
	DefaultTimeout time.Duration `mapstructure:"default_timeout"`
}

// StatusReporterConfig defines status reporter settings.
type StatusReporterConfig struct {
	// Number of completed tasks to buffer.
	CompletedTasksBuffer int `mapstructure:"completed_tasks_buffer"`

	// Interval for health check updates.
	HealthCheckInterval time.Duration `mapstructure:"health_check_interval"`
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg.Store.Type != "" && cfg.Store.Type != "memory" && cfg.Store.Type != "file" {
		return errors.New("store.type must be 'memory' or 'file'")
	}

	if cfg.Store.Type == "file" && cfg.Store.Path == "" {
		return errors.New("store.path is required when store.type is 'file'")
	}

	if cfg.TaskExecutor.Workers < 0 {
		return errors.New("task_executor.workers must be non-negative")
	}

	if cfg.TaskExecutor.QueueSize < 0 {
		return errors.New("task_executor.queue_size must be non-negative")
	}

	if cfg.StatusReporter.CompletedTasksBuffer < 0 {
		return errors.New("status_reporter.completed_tasks_buffer must be non-negative")
	}

	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		AgentID: "",
		Store: StoreConfig{
			Type: "memory",
			Path: "",
		},
		TaskExecutor: TaskExecutorConfig{
			Workers:        4,
			QueueSize:      100,
			DefaultTimeout: 30 * time.Second,
		},
		StatusReporter: StatusReporterConfig{
			CompletedTasksBuffer: 50,
			HealthCheckInterval:  10 * time.Second,
		},
	}
}
