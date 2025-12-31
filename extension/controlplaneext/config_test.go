// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "default config is valid",
			config:  createDefaultConfig(),
			wantErr: false,
		},
		{
			name: "valid config with memory backends",
			config: &Config{
				ConfigManager: configmanager.Config{Type: "memory"},
				TaskManager:   taskmanager.Config{Type: "memory"},
				AgentRegistry: agentregistry.Config{Type: "memory"},
			},
			wantErr: false,
		},
		{
			name: "invalid config_manager type",
			config: &Config{
				ConfigManager: configmanager.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "config_manager.type must be 'memory', 'nacos', 'multi_agent_nacos', or 'on_demand'",
		},
		{
			name: "nacos config without storage extension",
			config: &Config{
				ConfigManager: configmanager.Config{Type: "nacos"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required",
		},
		{
			name: "invalid task_manager type",
			config: &Config{
				TaskManager: taskmanager.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "task_manager.type must be 'memory' or 'redis'",
		},
		{
			name: "redis task_manager without storage extension",
			config: &Config{
				TaskManager: taskmanager.Config{Type: "redis"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required",
		},
		{
			name: "invalid agent_registry type",
			config: &Config{
				AgentRegistry: agentregistry.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "agent_registry.type must be 'memory' or 'redis'",
		},
		{
			name: "redis agent_registry without storage extension",
			config: &Config{
				AgentRegistry: agentregistry.Config{Type: "redis"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required",
		},
		{
			name: "negative task_executor workers",
			config: &Config{
				TaskExecutor: TaskExecutorConfig{
					Workers: -1,
				},
			},
			wantErr: true,
			errMsg:  "task_executor.workers must be non-negative",
		},
		{
			name: "negative task_executor queue_size",
			config: &Config{
				TaskExecutor: TaskExecutorConfig{
					QueueSize: -1,
				},
			},
			wantErr: true,
			errMsg:  "task_executor.queue_size must be non-negative",
		},
		{
			name: "negative status_reporter buffer",
			config: &Config{
				StatusReporter: StatusReporterConfig{
					CompletedTasksBuffer: -1,
				},
			},
			wantErr: true,
			errMsg:  "status_reporter.completed_tasks_buffer must be non-negative",
		},
		{
			name: "valid config with storage extension",
			config: &Config{
				StorageExtension: "storage",
				ConfigManager:    configmanager.Config{Type: "nacos"},
				TaskManager:      taskmanager.Config{Type: "redis"},
				AgentRegistry:    agentregistry.Config{Type: "redis"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()

	require.NotNil(t, cfg)
	assert.Empty(t, cfg.StorageExtension)
	assert.Empty(t, cfg.AgentID)

	// TaskExecutor defaults
	assert.Equal(t, 4, cfg.TaskExecutor.Workers)
	assert.Equal(t, 100, cfg.TaskExecutor.QueueSize)
	assert.Equal(t, 30*time.Second, cfg.TaskExecutor.DefaultTimeout)

	// StatusReporter defaults
	assert.Equal(t, 50, cfg.StatusReporter.CompletedTasksBuffer)
	assert.Equal(t, 10*time.Second, cfg.StatusReporter.HealthCheckInterval)
}
