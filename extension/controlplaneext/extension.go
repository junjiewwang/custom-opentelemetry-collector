// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// TokenValidationResult holds the result of token validation.
type TokenValidationResult struct {
	Valid   bool   `json:"valid"`
	AppID   string `json:"app_id,omitempty"`
	AppName string `json:"app_name,omitempty"`
	Token   string `json:"token,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ControlPlane defines the interface exposed by this extension to other components.
type ControlPlane interface {
	// Configuration management
	UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error
	GetCurrentConfig() *controlplanev1.AgentConfig

	// Task management
	SubmitTask(ctx context.Context, task *controlplanev1.Task) error
	SubmitTaskForAgent(ctx context.Context, agentID string, task *controlplanev1.Task) error
	GetTaskResult(taskID string) (*controlplanev1.TaskResult, bool)
	GetPendingTasks() []*controlplanev1.Task
	GetPendingTasksForAgent(ctx context.Context, agentID string) ([]*controlplanev1.Task, error)
	ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error
	CancelTask(ctx context.Context, taskID string) error
	IsTaskCancelled(ctx context.Context, taskID string) (bool, error)

	// Status management
	GetStatus() *controlplanev1.AgentStatus
	UpdateHealth(health *controlplanev1.HealthStatus)

	// Agent registry
	RegisterAgent(ctx context.Context, agent *agentregistry.AgentInfo) error
	HeartbeatAgent(ctx context.Context, agentID string, status *agentregistry.AgentStatus) error
	RegisterOrHeartbeatAgent(ctx context.Context, agent *agentregistry.AgentInfo) error
	UnregisterAgent(ctx context.Context, agentID string) error
	GetAgent(ctx context.Context, agentID string) (*agentregistry.AgentInfo, error)
	GetOnlineAgents(ctx context.Context) ([]*agentregistry.AgentInfo, error)
	GetAgentStats(ctx context.Context) (*agentregistry.AgentStats, error)

	// Chunk upload management
	UploadChunk(ctx context.Context, req *controlplanev1.UploadChunkRequest) (*controlplanev1.UploadChunkResponse, error)

	// Token validation
	ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error)
}

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension          = (*Extension)(nil)
	_ extensioncapabilities.Dependent = (*Extension)(nil)
	_ ControlPlane                 = (*Extension)(nil)
)

// Extension implements the control plane extension.
type Extension struct {
	config   *Config
	settings extension.Settings
	logger   *zap.Logger

	// Storage extension reference
	storage storageext.Storage

	// Core components
	configMgr      configmanager.ConfigManager
	taskMgr        taskmanager.TaskManager
	agentReg       agentregistry.AgentRegistry
	tokenMgr       tokenmanager.TokenManager
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
		zap.String("storage_extension", e.config.StorageExtension),
	)

	// Get storage extension if configured
	if e.config.StorageExtension != "" {
		storage, err := GetStorageExtension(host, e.config.StorageExtension, e.logger)
		if err != nil {
			return err
		}
		e.storage = storage
	}

	// Create component factory and initialize components
	factory := NewComponentFactory(e.logger, e.storage)

	var err error
	e.configMgr, err = factory.CreateConfigManager(e.config.ConfigManager)
	if err != nil {
		return fmt.Errorf("failed to create config manager: %w", err)
	}

	e.taskMgr, err = factory.CreateTaskManager(e.config.TaskManager)
	if err != nil {
		return fmt.Errorf("failed to create task manager: %w", err)
	}

	e.agentReg, err = factory.CreateAgentRegistry(e.config.AgentRegistry)
	if err != nil {
		return fmt.Errorf("failed to create agent registry: %w", err)
	}

	e.tokenMgr, err = factory.CreateTokenManager(e.config.TokenManager)
	if err != nil {
		return fmt.Errorf("failed to create token manager: %w", err)
	}

	// Initialize local components
	e.taskExecutor = newTaskExecutor(e.logger, e.config.TaskExecutor)
	e.statusReporter = newStatusReporter(e.logger, e.agentID, e.config.StatusReporter)
	e.chunkManager = newChunkManager(e.logger)

	// Start all components
	if err := e.configMgr.Start(ctx); err != nil {
		return err
	}

	if err := e.taskMgr.Start(ctx); err != nil {
		return err
	}

	if err := e.agentReg.Start(ctx); err != nil {
		return err
	}

	if err := e.tokenMgr.Start(ctx); err != nil {
		return err
	}

	if err := e.taskExecutor.Start(ctx); err != nil {
		return err
	}

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

	if err := e.agentReg.Close(); err != nil {
		e.logger.Warn("Error closing agent registry", zap.Error(err))
	}

	if err := e.tokenMgr.Close(); err != nil {
		e.logger.Warn("Error closing token manager", zap.Error(err))
	}

	if err := e.taskMgr.Close(); err != nil {
		e.logger.Warn("Error closing task manager", zap.Error(err))
	}

	if err := e.configMgr.Close(); err != nil {
		e.logger.Warn("Error closing config manager", zap.Error(err))
	}

	e.started = false
	return nil
}

// UpdateConfig implements ControlPlane.
func (e *Extension) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	if err := e.configMgr.UpdateConfig(ctx, config); err != nil {
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
	config, _ := e.configMgr.GetConfig(context.Background())
	return config
}

// SubmitTask implements ControlPlane.
func (e *Extension) SubmitTask(ctx context.Context, task *controlplanev1.Task) error {
	return e.taskMgr.SubmitTask(ctx, task)
}

// SubmitTaskForAgent implements ControlPlane.
func (e *Extension) SubmitTaskForAgent(ctx context.Context, agentID string, task *controlplanev1.Task) error {
	// 查询 Agent 信息以获取 AppID 和 ServiceName
	var agentMeta *taskmanager.AgentMeta
	if agent, err := e.agentReg.GetAgent(ctx, agentID); err == nil && agent != nil {
		agentMeta = &taskmanager.AgentMeta{
			AgentID:     agent.AgentID,
			AppID:       agent.AppID,
			ServiceName: agent.ServiceName,
		}
	} else {
		// Agent 不存在或查询失败，仅使用 AgentID
		agentMeta = &taskmanager.AgentMeta{
			AgentID: agentID,
		}
	}
	return e.taskMgr.SubmitTaskForAgent(ctx, agentMeta, task)
}

// GetTaskResult implements ControlPlane.
func (e *Extension) GetTaskResult(taskID string) (*controlplanev1.TaskResult, bool) {
	result, found, _ := e.taskMgr.GetTaskResult(context.Background(), taskID)
	return result, found
}

// GetPendingTasks implements ControlPlane.
func (e *Extension) GetPendingTasks() []*controlplanev1.Task {
	tasks, _ := e.taskMgr.GetGlobalPendingTasks(context.Background())
	return tasks
}

// GetPendingTasksForAgent implements ControlPlane.
func (e *Extension) GetPendingTasksForAgent(ctx context.Context, agentID string) ([]*controlplanev1.Task, error) {
	return e.taskMgr.GetPendingTasks(ctx, agentID)
}

// ReportTaskResult implements ControlPlane.
func (e *Extension) ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	return e.taskMgr.ReportTaskResult(ctx, result)
}

// CancelTask implements ControlPlane.
func (e *Extension) CancelTask(ctx context.Context, taskID string) error {
	return e.taskMgr.CancelTask(ctx, taskID)
}

// IsTaskCancelled implements ControlPlane.
func (e *Extension) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	return e.taskMgr.IsTaskCancelled(ctx, taskID)
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

// RegisterAgent implements ControlPlane.
func (e *Extension) RegisterAgent(ctx context.Context, agent *agentregistry.AgentInfo) error {
	return e.agentReg.Register(ctx, agent)
}

// HeartbeatAgent implements ControlPlane.
func (e *Extension) HeartbeatAgent(ctx context.Context, agentID string, status *agentregistry.AgentStatus) error {
	return e.agentReg.Heartbeat(ctx, agentID, status)
}

// RegisterOrHeartbeatAgent implements ControlPlane.
// This provides upsert semantics: registers the agent if not exists, or updates heartbeat if exists.
func (e *Extension) RegisterOrHeartbeatAgent(ctx context.Context, agent *agentregistry.AgentInfo) error {
	return e.agentReg.RegisterOrHeartbeat(ctx, agent)
}

// UnregisterAgent implements ControlPlane.
func (e *Extension) UnregisterAgent(ctx context.Context, agentID string) error {
	return e.agentReg.Unregister(ctx, agentID)
}

// GetAgent implements ControlPlane.
func (e *Extension) GetAgent(ctx context.Context, agentID string) (*agentregistry.AgentInfo, error) {
	return e.agentReg.GetAgent(ctx, agentID)
}

// GetOnlineAgents implements ControlPlane.
func (e *Extension) GetOnlineAgents(ctx context.Context) ([]*agentregistry.AgentInfo, error) {
	return e.agentReg.GetOnlineAgents(ctx)
}

// GetAgentStats implements ControlPlane.
func (e *Extension) GetAgentStats(ctx context.Context) (*agentregistry.AgentStats, error) {
	return e.agentReg.GetAgentStats(ctx)
}

// UploadChunk implements ControlPlane.
func (e *Extension) UploadChunk(ctx context.Context, req *controlplanev1.UploadChunkRequest) (*controlplanev1.UploadChunkResponse, error) {
	return e.chunkManager.HandleChunk(ctx, req)
}

// ValidateToken implements ControlPlane.
func (e *Extension) ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error) {
	if e.tokenMgr == nil {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "token manager not configured",
		}, nil
	}

	result, err := e.tokenMgr.ValidateToken(ctx, token)
	if err != nil {
		return nil, err
	}

	return &TokenValidationResult{
		Valid:   result.Valid,
		AppID:   result.AppID,
		AppName: result.AppName,
		Token:   token,
		Reason:  result.Reason,
	}, nil
}

// GetAgentID returns the agent's unique identifier.
func (e *Extension) GetAgentID() string {
	return e.agentID
}

// GetTaskManager returns the task manager for direct access.
func (e *Extension) GetTaskManager() taskmanager.TaskManager {
	return e.taskMgr
}

// GetAgentRegistry returns the agent registry for direct access.
func (e *Extension) GetAgentRegistry() agentregistry.AgentRegistry {
	return e.agentReg
}

// GetConfigManager returns the config manager for direct access.
func (e *Extension) GetConfigManager() configmanager.ConfigManager {
	return e.configMgr
}

// GetTokenManager returns the token manager for direct access.
func (e *Extension) GetTokenManager() tokenmanager.TokenManager {
	return e.tokenMgr
}

// GetStorage returns the storage extension for direct access.
func (e *Extension) GetStorage() storageext.Storage {
	return e.storage
}

// Dependencies implements extensioncapabilities.Dependent.
// This ensures the storage extension is started before this extension.
func (e *Extension) Dependencies() []component.ID {
	if e.config.StorageExtension == "" {
		return nil
	}
	// Return the storage extension as a dependency
	return []component.ID{component.MustNewID(e.config.StorageExtension)}
}
