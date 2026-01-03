// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
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

	// Storage extension reference (only used when not reusing controlplane)
	storage storageext.Storage

	// ControlPlane extension reference (when reusing components)
	controlPlane controlplaneext.ControlPlane

	// Arthas tunnel extension reference
	arthasTunnel arthastunnelext.ArthasTunnel

	// Core components (either created locally or reused from controlplane)
	configMgr configmanager.ConfigManager
	taskMgr   taskmanager.TaskManager
	agentReg  agentregistry.AgentRegistry
	tokenMgr  tokenmanager.TokenManager

	// On-demand config manager (if enabled)
	onDemandConfigMgr configmanager.OnDemandConfigManager

	// WebSocket token manager for secure WS authentication
	wsTokenMgr *wsTokenManager

	// Flag to track if we own the components (need to close them on shutdown)
	ownsComponents bool

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

	// Check if we should reuse components from controlplane extension
	if e.config.ControlPlaneExtension != "" {
		if err := e.initFromControlPlane(host); err != nil {
			return err
		}
	} else {
		// Create our own components
		if err := e.initOwnComponents(ctx, host); err != nil {
			return err
		}
	}

	// Initialize Arthas tunnel extension if configured
	if e.config.ArthasTunnelExtension != "" {
		if err := e.initArthasTunnel(host); err != nil {
			e.logger.Warn("Failed to initialize Arthas tunnel extension", zap.Error(err))
			// Don't fail startup, just log warning
		}
	}

	// Initialize WebSocket token manager (30 second TTL for tokens)
	e.wsTokenMgr = newWSTokenManager(30 * time.Second)

	// Start HTTP server
	if err := e.startHTTPServer(); err != nil {
		return err
	}

	e.started = true
	e.logger.Info("Admin extension started")
	return nil
}

// initFromControlPlane initializes by reusing components from the controlplane extension.
func (e *Extension) initFromControlPlane(host component.Host) error {
	// Find controlplane extension by type name
	controlPlaneType := component.MustNewType(e.config.ControlPlaneExtension)
	var found bool

	for id, ext := range host.GetExtensions() {
		if id.Type() == controlPlaneType {
			if cp, ok := ext.(controlplaneext.ControlPlane); ok {
				e.controlPlane = cp
				found = true
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("controlplane extension %q not found or does not implement ControlPlane interface", e.config.ControlPlaneExtension)
	}

	// Get the underlying extension to access component getters
	cpExt, ok := e.controlPlane.(*controlplaneext.Extension)
	if !ok {
		return fmt.Errorf("controlplane extension does not expose component getters")
	}

	// Reuse components from controlplane
	e.configMgr = cpExt.GetConfigManager()
	e.taskMgr = cpExt.GetTaskManager()
	e.agentReg = cpExt.GetAgentRegistry()
	e.tokenMgr = cpExt.GetTokenManager()
	e.ownsComponents = false // Don't close these on shutdown

	e.logger.Info("Reusing components from controlplane extension",
		zap.String("controlplane", e.config.ControlPlaneExtension),
	)

	return nil
}

// initArthasTunnel initializes the Arthas tunnel extension reference.
func (e *Extension) initArthasTunnel(host component.Host) error {
	arthasTunnelType := component.MustNewType(e.config.ArthasTunnelExtension)

	for id, ext := range host.GetExtensions() {
		if id.Type() == arthasTunnelType {
			if tunnel, ok := ext.(arthastunnelext.ArthasTunnel); ok {
				e.arthasTunnel = tunnel
				e.logger.Info("Arthas tunnel extension initialized",
					zap.String("extension", e.config.ArthasTunnelExtension),
				)
				return nil
			}
		}
	}

	return fmt.Errorf("arthas tunnel extension %q not found or does not implement ArthasTunnel interface", e.config.ArthasTunnelExtension)
}

// initOwnComponents creates and starts our own component instances.
func (e *Extension) initOwnComponents(ctx context.Context, host component.Host) error {
	// Get storage extension if configured using shared function
	if e.config.StorageExtension != "" {
		storage, err := controlplaneext.GetStorageExtension(host, e.config.StorageExtension, e.logger)
		if err != nil {
			return err
		}
		e.storage = storage
	}

	// Create component factory and initialize components
	factory := controlplaneext.NewComponentFactory(e.logger, e.storage)

	var err error
	e.configMgr, e.onDemandConfigMgr, err = factory.CreateConfigManagerWithOnDemand(e.config.ConfigManager)
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

	e.ownsComponents = true // We own these, close them on shutdown
	return nil
}

// startHTTPServer starts the HTTP server.
func (e *Extension) startHTTPServer() error {
	listener, err := net.Listen("tcp", e.config.HTTP.Endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", e.config.HTTP.Endpoint, err)
	}
	e.listener = listener

	// Create router with all routes and middleware
	handler := e.newRouter()

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

	// Only close components if we own them (not reused from controlplane)
	if e.ownsComponents {
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

// GetArthasTunnel returns the Arthas tunnel extension if available.
func (e *Extension) GetArthasTunnel() arthastunnelext.ArthasTunnel {
	return e.arthasTunnel
}

// Dependencies implements extensioncapabilities.Dependent.
// This ensures the storage extension and controlplane extension are started before this extension.
func (e *Extension) Dependencies() []component.ID {
	var deps []component.ID

	// If using controlplane extension, depend on it
	if e.config.ControlPlaneExtension != "" {
		deps = append(deps, component.MustNewID(e.config.ControlPlaneExtension))
	} else if e.config.StorageExtension != "" {
		// Otherwise depend on storage extension if configured
		deps = append(deps, component.MustNewID(e.config.StorageExtension))
	}

	// If using arthas tunnel extension, depend on it
	if e.config.ArthasTunnelExtension != "" {
		deps = append(deps, component.MustNewID(e.config.ArthasTunnelExtension))
	}

	return deps
}
