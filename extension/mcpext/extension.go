// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

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
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// tenantManagerProvider is an optional capability of the control plane. The
// concrete controlplaneext.Extension owns the single shared tenant manager.
type tenantManagerProvider interface {
	GetMultiTenantManager() *tenantmanager.MultiTenantManager
}

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension             = (*Extension)(nil)
	_ extensioncapabilities.Dependent = (*Extension)(nil)
)

// Extension implements the MCP extension that provides AI Agent interaction capabilities.
type Extension struct {
	config *Config
	logger *zap.Logger
	set    extension.Settings

	// Dependencies (discovered at Start time via host)
	controlPlane    controlplaneext.ControlPlane
	arthasTunnel    arthastunnelext.ArthasTunnel
	instrMgr        instrumentationmanager.InstrumentationManager
	tenantValidator tenantauth.Validator

	// MCP server
	mcpServer *mcpServerWrapper

	// HTTP server
	httpServer *http.Server
	listener   net.Listener

	// Lifecycle
	mu      sync.Mutex
	started bool
}

// newMCPExtension creates a new MCP extension.
func newMCPExtension(_ context.Context, set extension.Settings, config *Config) (*Extension, error) {
	return &Extension{
		config: config,
		logger: set.Logger,
		set:    set,
	}, nil
}

// Dependencies returns the extension dependencies.
// MCP Extension depends on ControlPlane and ArthasTunnel extensions. When the
// instrumentation tools are enabled it additionally depends on the extension
// owning the shared InstrumentationManager, so that manager is started before
// this extension and closed after it.
func (e *Extension) Dependencies() []component.ID {
	deps := []component.ID{
		component.MustNewID(e.config.ControlPlaneExtension),
		component.MustNewID(e.config.ArthasTunnelExtension),
	}
	if e.config.Instrumentation.Enabled && e.config.Instrumentation.Extension != "" {
		deps = append(deps, component.MustNewID(e.config.Instrumentation.Extension))
	}
	return deps
}

// Start starts the MCP extension.
func (e *Extension) Start(ctx context.Context, host component.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	e.logger.Info("Starting MCP extension",
		zap.String("endpoint", e.config.Endpoint),
		zap.String("auth_type", e.config.Auth.Type),
		zap.Int("max_concurrent_sessions", e.config.MaxConcurrentSessions),
	)

	// Discover dependencies
	if err := e.discoverDependencies(host); err != nil {
		return fmt.Errorf("failed to discover dependencies: %w", err)
	}

	// Create MCP server
	mcpSrv, err := newMCPServerWrapper(e)
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}
	e.mcpServer = mcpSrv

	// Start HTTP server
	if err := e.startHTTPServer(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	e.started = true
	e.logger.Info("MCP extension started successfully",
		zap.String("endpoint", e.listener.Addr().String()),
	)

	return nil
}

// Shutdown shuts down the MCP extension.
func (e *Extension) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return nil
	}

	e.logger.Info("Shutting down MCP extension")

	var errs []error

	// Shutdown HTTP server
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if e.httpServer != nil {
		if err := e.httpServer.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("HTTP server shutdown error: %w", err))
		}
	}

	e.started = false
	e.logger.Info("MCP extension shut down successfully")

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// discoverDependencies discovers the required extensions from host.
func (e *Extension) discoverDependencies(host component.Host) error {
	// Discover ControlPlane extension
	cpID := component.MustNewID(e.config.ControlPlaneExtension)
	cpExt, err := findExtension[controlplaneext.ControlPlane](host, cpID)
	if err != nil {
		return fmt.Errorf("controlplane extension '%s': %w", e.config.ControlPlaneExtension, err)
	}
	e.controlPlane = cpExt
	e.logger.Info("Discovered ControlPlane extension", zap.String("id", cpID.String()))

	// Discover ArthasTunnel extension
	atID := component.MustNewID(e.config.ArthasTunnelExtension)
	atExt, err := findExtension[arthastunnelext.ArthasTunnel](host, atID)
	if err != nil {
		return fmt.Errorf("arthas_tunnel extension '%s': %w", e.config.ArthasTunnelExtension, err)
	}
	e.arthasTunnel = atExt
	e.logger.Info("Discovered ArthasTunnel extension", zap.String("id", atID.String()))

	// Resolve the shared tenant key validator. The control plane owns the single
	// MultiTenantManager, so admin and MCP validate exactly the same keys.
	if e.config.Auth.DynamicKeys && e.config.Auth.Type == "api_key" {
		provider, ok := e.controlPlane.(tenantManagerProvider)
		if !ok {
			return fmt.Errorf("dynamic_keys requires the controlplane extension to expose a tenant manager")
		}
		tenantMgr := provider.GetMultiTenantManager()
		if tenantMgr == nil || tenantMgr.Keys == nil {
			return fmt.Errorf("dynamic_keys requires a started tenant manager")
		}
		keys := tenantMgr.Keys
		e.tenantValidator = tenantauth.ValidatorFunc(func(ctx context.Context, key string) (tenantauth.Identity, bool) {
			validation, err := keys.ValidateAPIKey(ctx, key)
			if err != nil || !validation.Valid {
				return tenantauth.Identity{}, false
			}
			return tenantauth.Identity{
				TenantID: validation.TenantID,
				KeyType:  validation.KeyType,
				KeyID:    validation.KeyID,
				Scopes:   validation.Scopes,
			}, true
		})
		e.logger.Info("Discovered tenant API key validator")
	}

	// Discover the shared instrumentation manager (optional capability).
	if !e.config.Instrumentation.Enabled {
		return nil
	}
	imID := component.MustNewID(e.config.Instrumentation.Extension)
	provider, err := findExtension[instrumentationmanager.Provider](host, imID)
	if err != nil {
		return fmt.Errorf("instrumentation provider extension '%s': %w", e.config.Instrumentation.Extension, err)
	}
	mgr := provider.InstrumentationManager()
	if mgr == nil {
		return fmt.Errorf("instrumentation provider extension '%s' has no manager", e.config.Instrumentation.Extension)
	}
	e.instrMgr = mgr
	e.logger.Info("Discovered InstrumentationManager", zap.String("id", imID.String()))

	return nil
}

// findExtension finds an extension by ID and asserts its type.
func findExtension[T any](host component.Host, id component.ID) (T, error) {
	var zero T
	exts := host.GetExtensions()
	ext, ok := exts[id]
	if !ok {
		return zero, fmt.Errorf("extension not found: %s", id.String())
	}
	typed, ok := ext.(T)
	if !ok {
		return zero, fmt.Errorf("extension %s does not implement required interface", id.String())
	}
	return typed, nil
}

// startHTTPServer starts the HTTP server for MCP Streamable HTTP transport.
func (e *Extension) startHTTPServer() error {
	mux := http.NewServeMux()

	// MCP endpoint - handled by mcp-go's StreamableHTTPServer
	// StreamableHTTPServer implements http.Handler and handles /mcp path internally.
	mcpHandler := e.mcpServer.Handler()

	// Static super keys keep full privileges; tk_/ok_ keys are resolved by the
	// shared tenant manager. Both admin and MCP use tenantauth.New.
	var handler http.Handler = mcpHandler
	if e.config.Auth.Type == "api_key" {
		handler = tenantauth.New(
			e.config.Auth.APIKeys,
			e.tenantValidator,
			e.logger,
			tenantauth.WithBearerPriority(),
			tenantauth.WithMCPErrorResponses(),
		)(mcpHandler)
	}

	// Mount the MCP handler at root, it internally routes /mcp
	mux.Handle("/", handler)

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	listener, err := net.Listen("tcp", e.config.Endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", e.config.Endpoint, err)
	}
	e.listener = listener

	e.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if err := e.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			e.logger.Error("MCP HTTP server error", zap.Error(err))
		}
	}()

	return nil
}
