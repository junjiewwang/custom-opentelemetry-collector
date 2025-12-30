// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ControlPlane defines the interface exposed by this extension to other components.
type ControlPlane interface {
	// Configuration management
	UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error
	GetCurrentConfig() *controlplanev1.AgentConfig

	// Task management
	SubmitTask(ctx context.Context, task *controlplanev1.Task) error
	GetTaskResult(taskID string) (*controlplanev1.TaskResult, bool)
	GetPendingTasks() []*controlplanev1.Task

	// Status management
	GetStatus() *controlplanev1.AgentStatus
	UpdateHealth(health *controlplanev1.HealthStatus)

	// Chunk upload management
	UploadChunk(ctx context.Context, req *controlplanev1.UploadChunkRequest) (*controlplanev1.UploadChunkResponse, error)
}

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension = (*Extension)(nil)
	_ ControlPlane        = (*Extension)(nil)
)

// Extension implements the control plane extension.
type Extension struct {
	config   *Config
	settings extension.Settings
	logger   *zap.Logger

	// Core components
	configManager  *ConfigManager
	taskExecutor   *TaskExecutor
	statusReporter *StatusReporter
	chunkManager   *ChunkManager

	// Agent identity
	agentID string

	// Lifecycle
	mu       sync.RWMutex
	started  bool
	stopChan chan struct{}
}

// newControlPlaneExtension creates a new control plane extension.
func newControlPlaneExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*Extension, error) {
	agentID := config.AgentID
	if agentID == "" {
		agentID = uuid.New().String()
	}

	ext := &Extension{
		config:   config,
		settings: set,
		logger:   set.Logger,
		agentID:  agentID,
		stopChan: make(chan struct{}),
	}

	// Initialize components
	ext.configManager = newConfigManager(ext.logger)
	ext.taskExecutor = newTaskExecutor(ext.logger, config.TaskExecutor)
	ext.statusReporter = newStatusReporter(ext.logger, agentID, config.StatusReporter)
	ext.chunkManager = newChunkManager(ext.logger)

	return ext, nil
}

// Start implements component.Component.
func (e *Extension) Start(ctx context.Context, host component.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	e.logger.Info("Starting control plane extension",
		zap.String("agent_id", e.agentID),
	)

	// Start task executor
	if err := e.taskExecutor.Start(ctx); err != nil {
		return err
	}

	// Start status reporter
	if err := e.statusReporter.Start(ctx); err != nil {
		return err
	}

	e.started = true
	return nil
}

// Shutdown implements component.Component.
func (e *Extension) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return nil
	}

	e.logger.Info("Shutting down control plane extension")

	close(e.stopChan)

	// Shutdown components in reverse order
	if err := e.statusReporter.Shutdown(ctx); err != nil {
		e.logger.Warn("Error shutting down status reporter", zap.Error(err))
	}

	if err := e.taskExecutor.Shutdown(ctx); err != nil {
		e.logger.Warn("Error shutting down task executor", zap.Error(err))
	}

	e.started = false
	return nil
}

// UpdateConfig implements ControlPlane.
func (e *Extension) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	if err := e.configManager.UpdateConfig(ctx, config); err != nil {
		return err
	}

	// Update status reporter with new config version
	e.statusReporter.SetConfigVersion(config.ConfigVersion)

	e.logger.Info("Configuration updated",
		zap.String("version", config.ConfigVersion),
	)
	return nil
}

// GetCurrentConfig implements ControlPlane.
func (e *Extension) GetCurrentConfig() *controlplanev1.AgentConfig {
	return e.configManager.GetCurrentConfig()
}

// SubmitTask implements ControlPlane.
func (e *Extension) SubmitTask(ctx context.Context, task *controlplanev1.Task) error {
	return e.taskExecutor.Submit(ctx, task)
}

// GetTaskResult implements ControlPlane.
func (e *Extension) GetTaskResult(taskID string) (*controlplanev1.TaskResult, bool) {
	return e.taskExecutor.GetResult(taskID)
}

// GetPendingTasks implements ControlPlane.
func (e *Extension) GetPendingTasks() []*controlplanev1.Task {
	return e.taskExecutor.GetPendingTasks()
}

// GetStatus implements ControlPlane.
func (e *Extension) GetStatus() *controlplanev1.AgentStatus {
	status := e.statusReporter.GetStatus()

	// Add completed tasks from task executor
	status.CompletedTasks = e.taskExecutor.DrainCompletedResults()

	return status
}

// UpdateHealth implements ControlPlane.
func (e *Extension) UpdateHealth(health *controlplanev1.HealthStatus) {
	e.statusReporter.UpdateHealth(health)
}

// UploadChunk implements ControlPlane.
func (e *Extension) UploadChunk(ctx context.Context, req *controlplanev1.UploadChunkRequest) (*controlplanev1.UploadChunkResponse, error) {
	return e.chunkManager.HandleChunk(ctx, req)
}

// GetAgentID returns the agent's unique identifier.
func (e *Extension) GetAgentID() string {
	return e.agentID
}
