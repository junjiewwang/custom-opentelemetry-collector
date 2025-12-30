// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

// Config defines the configuration for the control plane extension.
type Config struct {
	// StorageExtension is the name of the storage extension to use.
	// If empty, in-memory storage will be used.
	StorageExtension string `mapstructure:"storage_extension"`

	// AgentID is the unique identifier for this agent instance.
	// If empty, a UUID will be generated.
	AgentID string `mapstructure:"agent_id"`

	// ConfigManager configuration for managing agent configuration.
	ConfigManager configmanager.Config `mapstructure:"config_manager"`

	// TaskManager configuration for managing tasks.
	TaskManager taskmanager.Config `mapstructure:"task_manager"`

	// AgentRegistry configuration for managing agent registration and status.
	AgentRegistry agentregistry.Config `mapstructure:"agent_registry"`

	// TokenManager configuration for token validation.
	TokenManager tokenmanager.Config `mapstructure:"token_manager"`

	// TaskExecutor configuration (for local task execution).
	TaskExecutor TaskExecutorConfig `mapstructure:"task_executor"`

	// StatusReporter configuration.
	StatusReporter StatusReporterConfig `mapstructure:"status_reporter"`
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
	// Validate ConfigManager
	validConfigTypes := map[string]bool{"": true, "memory": true, "nacos": true, "multi_agent_nacos": true, "on_demand": true}
	if !validConfigTypes[cfg.ConfigManager.Type] {
		return errors.New("config_manager.type must be 'memory', 'nacos', 'multi_agent_nacos', or 'on_demand'")
	}

	if (cfg.ConfigManager.Type == "nacos" || cfg.ConfigManager.Type == "multi_agent_nacos" || cfg.ConfigManager.Type == "on_demand") && cfg.StorageExtension == "" {
		return errors.New("storage_extension is required when config_manager.type is 'nacos', 'multi_agent_nacos', or 'on_demand'")
	}

	// Validate TaskManager
	if cfg.TaskManager.Type != "" && cfg.TaskManager.Type != "memory" && cfg.TaskManager.Type != "redis" {
		return errors.New("task_manager.type must be 'memory' or 'redis'")
	}

	if cfg.TaskManager.Type == "redis" {
		if cfg.StorageExtension == "" {
			return errors.New("storage_extension is required when task_manager.type is 'redis'")
		}
	}

	// Validate AgentRegistry
	if cfg.AgentRegistry.Type != "" && cfg.AgentRegistry.Type != "memory" && cfg.AgentRegistry.Type != "redis" {
		return errors.New("agent_registry.type must be 'memory' or 'redis'")
	}

	if cfg.AgentRegistry.Type == "redis" {
		if cfg.StorageExtension == "" {
			return errors.New("storage_extension is required when agent_registry.type is 'redis'")
		}
	}

	// Validate TokenManager
	if cfg.TokenManager.Type != "" && cfg.TokenManager.Type != "memory" && cfg.TokenManager.Type != "redis" {
		return errors.New("token_manager.type must be 'memory' or 'redis'")
	}

	if cfg.TokenManager.Type == "redis" {
		if cfg.StorageExtension == "" {
			return errors.New("storage_extension is required when token_manager.type is 'redis'")
		}
	}

	// Validate TaskExecutor
	if cfg.TaskExecutor.Workers < 0 {
		return errors.New("task_executor.workers must be non-negative")
	}

	if cfg.TaskExecutor.QueueSize < 0 {
		return errors.New("task_executor.queue_size must be non-negative")
	}

	// Validate StatusReporter
	if cfg.StatusReporter.CompletedTasksBuffer < 0 {
		return errors.New("status_reporter.completed_tasks_buffer must be non-negative")
	}

	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		StorageExtension: "",
		AgentID:          "",
		ConfigManager:    configmanager.DefaultConfig(),
		TaskManager:      taskmanager.DefaultConfig(),
		AgentRegistry:    agentregistry.DefaultConfig(),
		TokenManager:     tokenmanager.DefaultConfig(),
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
