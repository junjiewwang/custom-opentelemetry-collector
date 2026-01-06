// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/storageext"
)

var _ extension.Extension = (*Extension)(nil)
var _ ArthasTunnel = (*Extension)(nil)

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

	// If distributed mode is enabled, get storage extension
	if e.config.IsDistributedEnabled() {
		if err := e.initDistributed(host); err != nil {
			e.logger.Warn("Failed to initialize distributed mode, falling back to local mode",
				zap.Error(err),
			)
			// Continue with local mode
		}
	}

	// Create arthasURICompat with optional distributed manager
	e.compat = newArthasURICompat(e.ctx, e.logger, e.config, e.distributed)

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
func (e *Extension) HandleInternalProxy(w http.ResponseWriter, r *http.Request) {
	if e.distributed == nil || e.distributed.Proxy() == nil {
		http.Error(w, "distributed mode not enabled", http.StatusServiceUnavailable)
		return
	}
	e.distributed.Proxy().HandleInternalProxy(w, r)
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

// Errors
var errStorageExtensionNotFound = &extensionError{msg: "storage extension not found, distributed mode requires storageext"}

type extensionError struct {
	msg string
}

func (e *extensionError) Error() string {
	return e.msg
}
