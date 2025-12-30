// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext"
)

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension             = (*Extension)(nil)
	_ extensioncapabilities.Dependent = (*Extension)(nil)
)

// Extension implements the admin extension.
type Extension struct {
	config   *Config
	settings extension.Settings
	logger   *zap.Logger

	// Storage extension reference
	storage storageext.Storage

	// Core components
	configMgr configmanager.ConfigManager
	taskMgr   taskmanager.TaskManager
	agentReg  agentregistry.AgentRegistry
	tokenMgr  tokenmanager.TokenManager

	// On-demand config manager (if enabled)
	onDemandConfigMgr configmanager.OnDemandConfigManager

	// HTTP server
	server   *http.Server
	listener net.Listener

	// Lifecycle
	mu      sync.RWMutex
	started bool
}

// newAdminExtension creates a new admin extension.
func newAdminExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*Extension, error) {
	return &Extension{
		config:   config,
		settings: set,
		logger:   set.Logger,
	}, nil
}

// Start implements component.Component.
func (e *Extension) Start(ctx context.Context, host component.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	e.logger.Info("Starting admin extension",
		zap.String("endpoint", e.config.HTTP.Endpoint),
	)

	// Get storage extension if configured
	if e.config.StorageExtension != "" {
		if err := e.initStorage(host); err != nil {
			return err
		}
	}

	// Initialize components
	var err error
	e.configMgr, err = e.createConfigManager()
	if err != nil {
		return fmt.Errorf("failed to create config manager: %w", err)
	}

	e.taskMgr, err = e.createTaskManager()
	if err != nil {
		return fmt.Errorf("failed to create task manager: %w", err)
	}

	e.agentReg, err = e.createAgentRegistry()
	if err != nil {
		return fmt.Errorf("failed to create agent registry: %w", err)
	}

	e.tokenMgr, err = e.createTokenManager()
	if err != nil {
		return fmt.Errorf("failed to create token manager: %w", err)
	}

	// Start components
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

	// Start HTTP server
	if err := e.startHTTPServer(); err != nil {
		return err
	}

	e.started = true
	e.logger.Info("Admin extension started")
	return nil
}

// initStorage initializes the storage extension reference.
func (e *Extension) initStorage(host component.Host) error {
	// Find storage extension by type name
	storageType := component.MustNewType(e.config.StorageExtension)
	var storage storageext.Storage
	var found bool

	for id, ext := range host.GetExtensions() {
		if id.Type() == storageType {
			if s, ok := ext.(storageext.Storage); ok {
				storage = s
				found = true
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("storage extension %q not found or does not implement Storage interface", e.config.StorageExtension)
	}

	e.storage = storage
	e.logger.Info("Using storage extension", zap.String("name", e.config.StorageExtension))
	return nil
}

// createConfigManager creates the appropriate ConfigManager based on config.
func (e *Extension) createConfigManager() (configmanager.ConfigManager, error) {
	cfg := e.config.ConfigManager

	switch cfg.Type {
	case "nacos":
		if e.storage == nil {
			return nil, fmt.Errorf("storage extension required for nacos config manager")
		}
		nacosName := cfg.NacosName
		if nacosName == "" {
			nacosName = "default"
		}
		client, err := e.storage.GetNacosConfigClient(nacosName)
		if err != nil {
			return nil, fmt.Errorf("failed to get nacos client %q: %w", nacosName, err)
		}
		return configmanager.NewNacosConfigManager(e.logger, cfg, client)

	case "on_demand":
		if e.storage == nil {
			return nil, fmt.Errorf("storage extension required for on-demand config manager")
		}
		nacosName := cfg.NacosName
		if nacosName == "" {
			nacosName = "default"
		}
		client, err := e.storage.GetNacosConfigClient(nacosName)
		if err != nil {
			return nil, fmt.Errorf("failed to get nacos client %q: %w", nacosName, err)
		}
		onDemandMgr, err := configmanager.NewOnDemandConfigManager(e.logger, cfg, client)
		if err != nil {
			return nil, err
		}
		e.onDemandConfigMgr = onDemandMgr
		return onDemandMgr, nil

	default:
		return configmanager.NewMemoryConfigManager(e.logger), nil
	}
}

// createTokenManager creates the appropriate TokenManager based on config.
func (e *Extension) createTokenManager() (tokenmanager.TokenManager, error) {
	cfg := e.config.TokenManager

	switch cfg.Type {
	case "redis":
		if e.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis token manager")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := e.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return tokenmanager.NewRedisTokenManager(e.logger, cfg, client)

	default:
		return tokenmanager.NewMemoryTokenManager(e.logger, cfg), nil
	}
}

// createTaskManager creates the appropriate TaskManager based on config.
func (e *Extension) createTaskManager() (taskmanager.TaskManager, error) {
	cfg := e.config.TaskManager

	switch cfg.Type {
	case "redis":
		if e.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis task manager")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := e.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return taskmanager.NewRedisTaskManager(e.logger, cfg, client)
	default:
		return taskmanager.NewMemoryTaskManager(e.logger, cfg), nil
	}
}

// createAgentRegistry creates the appropriate AgentRegistry based on config.
func (e *Extension) createAgentRegistry() (agentregistry.AgentRegistry, error) {
	cfg := e.config.AgentRegistry

	switch cfg.Type {
	case "redis":
		if e.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis agent registry")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := e.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return agentregistry.NewRedisAgentRegistry(e.logger, cfg, client)
	default:
		return agentregistry.NewMemoryAgentRegistry(e.logger, cfg), nil
	}
}

// startHTTPServer starts the HTTP server.
func (e *Extension) startHTTPServer() error {
	listener, err := net.Listen("tcp", e.config.HTTP.Endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", e.config.HTTP.Endpoint, err)
	}
	e.listener = listener

	// Create router
	mux := http.NewServeMux()
	e.registerRoutes(mux)

	// Apply middleware
	var handler http.Handler = mux
	handler = e.loggingMiddleware(handler)
	if e.config.CORS.Enabled {
		handler = e.corsMiddleware(handler)
	}
	if e.config.Auth.Enabled {
		handler = e.authMiddleware(handler)
	}

	e.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  e.config.HTTP.ReadTimeout,
		WriteTimeout: e.config.HTTP.WriteTimeout,
		IdleTimeout:  e.config.HTTP.IdleTimeout,
	}

	go func() {
		e.logger.Info("HTTP server listening", zap.String("addr", listener.Addr().String()))
		if err := e.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			e.logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	return nil
}

// registerRoutes registers all HTTP routes.
func (e *Extension) registerRoutes(mux *http.ServeMux) {
	// App/Token management routes
	mux.HandleFunc("/api/v1/apps", e.handleApps)
	mux.HandleFunc("/api/v1/apps/", e.handleAppByID)

	// Config routes
	mux.HandleFunc("/api/v1/configs", e.handleConfigs)
	mux.HandleFunc("/api/v1/configs/", e.handleConfigByID)

	// Task routes
	mux.HandleFunc("/api/v1/tasks", e.handleTasks)
	mux.HandleFunc("/api/v1/tasks/", e.handleTaskByID)
	mux.HandleFunc("/api/v1/tasks/batch", e.handleTaskBatch)

	// Agent routes
	mux.HandleFunc("/api/v1/agents", e.handleAgents)
	mux.HandleFunc("/api/v1/agents/stats", e.handleAgentStats)
	mux.HandleFunc("/api/v1/agents/", e.handleAgentByID)

	// Dashboard routes
	mux.HandleFunc("/api/v1/dashboard/overview", e.handleDashboardOverview)

	// Health check
	mux.HandleFunc("/health", e.handleHealth)
}

// Shutdown implements component.Component.
func (e *Extension) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return nil
	}

	e.logger.Info("Shutting down admin extension")

	// Shutdown HTTP server
	if e.server != nil {
		if err := e.server.Shutdown(ctx); err != nil {
			e.logger.Warn("Error shutting down HTTP server", zap.Error(err))
		}
	}

	// Close components
	if e.agentReg != nil {
		if err := e.agentReg.Close(); err != nil {
			e.logger.Warn("Error closing agent registry", zap.Error(err))
		}
	}

	if e.taskMgr != nil {
		if err := e.taskMgr.Close(); err != nil {
			e.logger.Warn("Error closing task manager", zap.Error(err))
		}
	}

	if e.configMgr != nil {
		if err := e.configMgr.Close(); err != nil {
			e.logger.Warn("Error closing config manager", zap.Error(err))
		}
	}

	if e.tokenMgr != nil {
		if err := e.tokenMgr.Close(); err != nil {
			e.logger.Warn("Error closing token manager", zap.Error(err))
		}
	}

	e.started = false
	return nil
}

// GetConfigManager returns the config manager.
func (e *Extension) GetConfigManager() configmanager.ConfigManager {
	return e.configMgr
}

// GetTaskManager returns the task manager.
func (e *Extension) GetTaskManager() taskmanager.TaskManager {
	return e.taskMgr
}

// GetAgentRegistry returns the agent registry.
func (e *Extension) GetAgentRegistry() agentregistry.AgentRegistry {
	return e.agentReg
}

// GetTokenManager returns the token manager.
func (e *Extension) GetTokenManager() tokenmanager.TokenManager {
	return e.tokenMgr
}

// GetOnDemandConfigManager returns the on-demand config manager if available.
func (e *Extension) GetOnDemandConfigManager() configmanager.OnDemandConfigManager {
	return e.onDemandConfigMgr
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
