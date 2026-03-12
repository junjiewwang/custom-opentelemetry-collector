// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/storageext"
)

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension             = (*Extension)(nil)
	_ extensioncapabilities.Dependent = (*Extension)(nil)
	_ ArthasTunnel                    = (*Extension)(nil)
)

// Extension implements an Arthas tunnel-server compatible extension.
//
// It provides two ingress endpoints (wired by other components):
// - agentgateway -> HandleAgentWebSocket (/v1/arthas/ws), authenticated via Authorization header
// - admin       -> HandleBrowserWebSocket (/api/v1/arthas/ws), authenticated via ws_token (token query)
//
// The ingress components only provide endpoints and perform auth. The core method
// routing and tunnel-server semantics are implemented here.
//
// In distributed mode, agents can connect to any replica, and the extension
// coordinates via Redis for cross-node visibility and proxying.
type Extension struct {
	config   *Config
	logger   *zap.Logger
	settings extension.Settings

	ctx    context.Context
	cancel context.CancelFunc

	startOnce sync.Once
	stopOnce  sync.Once

	compat      *arthasURICompat
	distributed *DistributedManager

	// Storage extension for Redis client (used in distributed mode)
	storage storageext.Storage
}

func newExtension(set extension.Settings, cfg *Config) (*Extension, error) {
	return &Extension{config: cfg, logger: set.Logger, settings: set}, nil
}

func (e *Extension) Start(ctx context.Context, host component.Host) error {
	var err error
	e.startOnce.Do(func() {
		err = e.start(ctx, host)
	})
	return err
}

func (e *Extension) start(ctx context.Context, host component.Host) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	// If distributed mode is enabled, initialize it (must succeed, no fallback)
	if e.config.IsDistributedEnabled() {
		if err := e.initDistributed(host); err != nil {
			// Do not fallback to local mode - fail fast with clear error message
			e.logger.Error("Failed to initialize distributed mode",
				zap.Error(err),
				zap.String("hint", "Check storage extension configuration and Redis connection"),
			)
			return fmt.Errorf("distributed mode initialization failed: %w", err)
		}
	}

	// Find controlplane task submitter (optional). Required by auto_detach.
	var taskSubmitter interface {
		SubmitTaskForAgent(ctx context.Context, agentID string, task *model.Task) error
	}
	for _, ext := range host.GetExtensions() {
		if ts, ok := ext.(interface {
			SubmitTaskForAgent(ctx context.Context, agentID string, task *model.Task) error
		}); ok {
			taskSubmitter = ts
			break
		}
	}
	if e.config.AutoDetach.Enabled && taskSubmitter == nil {
		e.logger.Warn("auto_detach enabled but no controlplane task submitter found; tasks will not be submitted")
	}

	// Create arthasURICompat with optional distributed manager
	e.compat = newArthasURICompat(e.ctx, e.logger, e.config, e.distributed, taskSubmitter)

	if e.distributed != nil {
		e.logger.Info("Arthas tunnel extension started (distributed mode)",
			zap.String("node_id", e.distributed.NodeID()),
			zap.String("node_addr", e.distributed.NodeAddr()),
			zap.Duration("compat_connect_timeout", e.config.CompatConnectTimeout),
			zap.Bool("strict_ingress_method_allowlist", e.config.StrictIngressMethodAllowlist),
			zap.Int("max_pending_connections", e.config.MaxPendingConnections),
		)
	} else {
		e.logger.Info("Arthas tunnel extension started (local mode)",
			zap.Duration("compat_connect_timeout", e.config.CompatConnectTimeout),
			zap.Bool("strict_ingress_method_allowlist", e.config.StrictIngressMethodAllowlist),
			zap.Int("max_pending_connections", e.config.MaxPendingConnections),
		)
	}
	return nil
}

func (e *Extension) initDistributed(host component.Host) error {
	// Find storage extension
	for _, ext := range host.GetExtensions() {
		if storage, ok := ext.(storageext.Storage); ok {
			e.storage = storage
			break
		}
	}

	if e.storage == nil {
		return errStorageExtensionNotFound
	}

	// Get Redis client
	redisClient, err := e.storage.GetRedis(e.config.Distributed.RedisName)
	if err != nil {
		return err
	}

	// Determine listener port (use a default if not available)
	// In practice, this would come from the admin extension's HTTP server config
	listenerPort := 8088 // Default admin HTTP port

	// Create distributed manager
	e.distributed = NewDistributedManager(e.ctx, e.logger, e.config, redisClient, listenerPort)
	e.distributed.Start()

	return nil
}

func (e *Extension) Shutdown(ctx context.Context) error {
	var err error
	e.stopOnce.Do(func() {
		err = e.shutdown(ctx)
	})
	return err
}

func (e *Extension) shutdown(ctx context.Context) error {
	if e.cancel != nil {
		e.cancel()
	}
	if e.compat != nil {
		e.compat.shutdown(ctx)
	}
	if e.distributed != nil {
		e.distributed.Shutdown(ctx)
	}
	e.logger.Info("Arthas tunnel extension stopped")
	return nil
}

// HandleAgentWebSocket is called by agentgatewayreceiver.
func (e *Extension) HandleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	if e.compat == nil {
		http.Error(w, "arthas tunnel not started", http.StatusServiceUnavailable)
		return
	}
	e.compat.handleWS(ingressAgentGateway, w, r)
}

// HandleBrowserWebSocket is called by adminext.
func (e *Extension) HandleBrowserWebSocket(w http.ResponseWriter, r *http.Request) {
	if e.compat == nil {
		http.Error(w, "arthas tunnel not started", http.StatusServiceUnavailable)
		return
	}
	e.compat.handleWS(ingressAdmin, w, r)
}

// HandleInternalProxy handles internal cross-node proxy requests.
// This should be mounted at the internal path prefix by adminext.
//
// Routes:
//   - /proxy/connect?id=<agentID>: Proxied connectArthas, runs full handleConnectArthas flow
//   - /proxy/opentunnel?clientConnectionId=<id>: Proxied openTunnel, delivers to local pending
func (e *Extension) HandleInternalProxy(w http.ResponseWriter, r *http.Request) {
	if e.distributed == nil {
		http.Error(w, "distributed mode not enabled", http.StatusServiceUnavailable)
		return
	}

	// Validate internal token
	token := r.Header.Get(e.config.Distributed.InternalAuth.HeaderName)
	if token != e.config.Distributed.InternalAuth.Token {
		e.logger.Warn("Invalid internal token",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("path", r.URL.Path),
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse path to determine action
	// Path format: /internal/v1/arthas/proxy/connect or /internal/v1/arthas/proxy/opentunnel
	path := strings.TrimPrefix(r.URL.Path, e.config.Distributed.InternalPathPrefix)
	path = strings.TrimPrefix(path, "/proxy/")

	switch {
	case strings.HasPrefix(path, "connect"):
		// Proxied connectArthas: run full handleConnectArthas flow on this node
		// The internal WS acts as the "browser" connection
		e.handleInternalConnect(w, r)

	case strings.HasPrefix(path, "opentunnel"):
		// Proxied openTunnel: deliver tunnel to local pending
		// This still uses WSProxy because it needs TunnelDeliverer
		if e.distributed.Proxy() == nil {
			http.Error(w, "proxy not available", http.StatusServiceUnavailable)
			return
		}
		e.distributed.Proxy().HandleInternalOpenTunnel(w, r)

	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

// handleInternalConnect handles proxied connectArthas requests from other nodes.
// It treats the internal WebSocket as a browser connection and runs the full
// handleConnectArthas flow (pending registration, startTunnel, openTunnel wait, relay).
//
// This uses a dedicated internal entry point (HandleInternalConnectArthas) instead
// of the generic handleWS dispatcher, ensuring internal protocol is decoupled from
// external method-based dispatch.
func (e *Extension) handleInternalConnect(w http.ResponseWriter, r *http.Request) {
	if e.compat == nil {
		http.Error(w, "arthas tunnel not started", http.StatusServiceUnavailable)
		return
	}

	agentID := r.URL.Query().Get("id")
	if agentID == "" {
		http.Error(w, "Missing agent ID", http.StatusBadRequest)
		return
	}

	e.logger.Info("Internal connect request received",
		zap.String("agent_id", agentID),
		zap.String("remote_addr", r.RemoteAddr),
	)

	// Use dedicated internal entry point - no method dispatch needed
	e.compat.HandleInternalConnectArthas(w, r, agentID)
}

// ===== ArthasTunnel API used by adminext =====

func (e *Extension) ListConnectedAgents() []*ConnectedAgent {
	if e.compat == nil {
		return nil
	}
	return e.compat.ListAgents()
}

func (e *Extension) IsAgentConnected(agentID string) bool {
	if e.compat == nil {
		return false
	}
	return e.compat.IsAgentOnline(agentID)
}

// IsDistributedMode returns true if distributed mode is enabled and active.
func (e *Extension) IsDistributedMode() bool {
	return e.distributed != nil
}

// GetInternalPathPrefix returns the internal path prefix for cross-node proxy.
func (e *Extension) GetInternalPathPrefix() string {
	return e.config.Distributed.InternalPathPrefix
}

// ConnectToAgent establishes a programmatic Arthas session to a target agent.
// This is the internal API used by mcpext to execute Arthas commands.
func (e *Extension) ConnectToAgent(ctx context.Context, agentID string) (*ArthasSession, error) {
	if e.compat == nil {
		return nil, fmt.Errorf("arthas tunnel not started")
	}
	return e.compat.connectToAgentProgrammatic(ctx, agentID, e.logger)
}

// Dependencies implements extensioncapabilities.Dependent.
// This ensures the storage extension is started before this extension
// when distributed mode is enabled.
func (e *Extension) Dependencies() []component.ID {
	// Only declare dependency when distributed mode is enabled
	if !e.config.IsDistributedEnabled() {
		return nil
	}

	// Use the configured storage extension name
	storageExtName := e.config.Distributed.StorageExtension
	if storageExtName == "" {
		storageExtName = "storage" // Default name
	}

	return []component.ID{component.MustNewID(storageExtName)}
}

// Errors
var errStorageExtensionNotFound = &extensionError{msg: "storage extension not found, distributed mode requires storageext"}

type extensionError struct {
	msg string
}

func (e *extensionError) Error() string {
	return e.msg
}
