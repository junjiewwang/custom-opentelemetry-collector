// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/notification"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

// ── ValidateComponentConfigs ──────────────────────────────────────────

func TestValidateComponentConfigs_ValidDefaults(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{})
	assert.NoError(t, err)
}

func TestValidateComponentConfigs_InvalidConfigManagerType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ConfigManager: configmanager.Config{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config_manager.type")
}

func TestValidateComponentConfigs_ConfigManagerNeedsStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ConfigManager: configmanager.Config{Type: "nacos"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage_extension")
}

func TestValidateComponentConfigs_OnDemandNeedsStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ConfigManager: configmanager.Config{Type: "on_demand"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage_extension")
}

func TestValidateComponentConfigs_ConfigManagerWithStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		StorageExtension: "storage",
		ConfigManager:    configmanager.Config{Type: "nacos"},
	})
	assert.NoError(t, err)
}

func TestValidateComponentConfigs_InvalidTaskManagerType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		TaskManager: taskmanager.Config{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task_manager.type")
}

func TestValidateComponentConfigs_TaskManagerRedisNeedsStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		TaskManager: taskmanager.Config{Type: "redis"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage_extension")
}

func TestValidateComponentConfigs_TaskManagerEngineNeedsStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		TaskManager: taskmanager.Config{Type: "engine"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage_extension")
}

func TestValidateComponentConfigs_InvalidAgentRegistryType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		AgentRegistry: agentregistry.Config{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent_registry.type")
}

func TestValidateComponentConfigs_AgentRegistryRedisNeedsStorage(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		AgentRegistry: agentregistry.Config{Type: "redis"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage_extension")
}

func TestValidateComponentConfigs_InvalidTokenManagerType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		TokenManager: appmanager.Config{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token_manager.type")
}

func TestValidateComponentConfigs_InvalidServiceManagerType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ServiceManager: servicemanager.Config{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "service_manager.type")
}

func TestValidateComponentConfigs_InvalidChunkManagerType(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ChunkManager: ChunkManagerConfig{Type: "invalid"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "chunk_manager.type")
}

func TestValidateComponentConfigs_ArtifactNotificationMissingURLs(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ArtifactNotification: notification.Config{Enabled: true},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "analysis_service_url")
}

func TestValidateComponentConfigs_ArtifactNotificationMissingCallback(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ArtifactNotification: notification.Config{
			Enabled:            true,
			AnalysisServiceURL: "http://example.com",
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "callback_url")
}

func TestValidateComponentConfigs_ArtifactNotificationValid(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ArtifactNotification: notification.Config{
			Enabled:            true,
			AnalysisServiceURL: "http://example.com",
			CallbackURL:        "http://callback.example.com",
		},
	})
	assert.NoError(t, err)
}

func TestValidateComponentConfigs_ArtifactNotificationDisabled(t *testing.T) {
	err := ValidateComponentConfigs(ComponentConfigs{
		ArtifactNotification: notification.Config{Enabled: false},
	})
	assert.NoError(t, err)
}

// ── Config Defaults & Validate ─────────────────────────────────────────

func TestConfig_Validate_ValidDefault(t *testing.T) {
	cfg := createDefaultConfig()
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_InvalidTaskExecutorWorkers(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.TaskExecutor.Workers = -1
	assert.Error(t, cfg.Validate())
	assert.Contains(t, cfg.Validate().Error(), "task_executor.workers")
}

func TestConfig_Validate_InvalidTaskExecutorQueue(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.TaskExecutor.QueueSize = -1
	assert.Error(t, cfg.Validate())
	assert.Contains(t, cfg.Validate().Error(), "task_executor.queue_size")
}

func TestConfig_Validate_InvalidStatusReporterBuffer(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.StatusReporter.CompletedTasksBuffer = -1
	assert.Error(t, cfg.Validate())
	assert.Contains(t, cfg.Validate().Error(), "status_reporter.completed_tasks_buffer")
}

func TestConfig_Validate_DelegatesToComponentValidation(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.ConfigManager.Type = "invalid"
	assert.Error(t, cfg.Validate())
	assert.Contains(t, cfg.Validate().Error(), "config_manager.type")
}
