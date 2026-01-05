// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
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
type Extension struct {
	config   *Config
	logger   *zap.Logger
	settings extension.Settings

	ctx    context.Context
	cancel context.CancelFunc

	startOnce sync.Once
	stopOnce  sync.Once

	compat *arthasURICompat
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

func (e *Extension) start(ctx context.Context, _ component.Host) error {
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.compat = newArthasURICompat(e.ctx, e.logger, e.config)

	e.logger.Info("Arthas tunnel extension started (arthas-uri compat mode)",
		zap.Duration("compat_connect_timeout", e.config.CompatConnectTimeout),
		zap.Bool("strict_ingress_method_allowlist", e.config.StrictIngressMethodAllowlist),
		zap.Int("max_pending_connections", e.config.MaxPendingConnections),
	)
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

// ===== ArthasTunnel API used by adminext =====

func (e *Extension) ListConnectedAgents() []*ConnectedAgent {
	if e.compat == nil {
		return nil
	}

	e.compat.mu.Lock()
	agents := make([]*ConnectedAgent, 0, len(e.compat.agents))
	for _, a := range e.compat.agents {
		if a == nil || a.conn == nil {
			continue
		}
		agents = append(agents, &ConnectedAgent{
			AgentID:      a.agentID,
			AppID:        a.appID,
			ServiceName:  a.appName,
			Hostname:     "",
			IP:           "",
			Version:      a.arthasVersion,
			ConnectedAt:  a.connectedAt,
			LastPingAt:   a.lastPingAt,
			ArthasStatus: nil,
		})
	}
	e.compat.mu.Unlock()
	return agents
}

func (e *Extension) IsAgentConnected(agentID string) bool {
	if e.compat == nil {
		return false
	}
	e.compat.mu.Lock()
	a := e.compat.agents[agentID]
	e.compat.mu.Unlock()
	return a != nil && a.conn != nil
}

func (e *Extension) GetAgentArthasStatus(agentID string) (*ArthasStatus, error) {
	if e.compat == nil {
		return nil, errors.New("arthas tunnel not started")
	}
	e.compat.mu.Lock()
	a := e.compat.agents[agentID]
	e.compat.mu.Unlock()
	if a == nil || a.conn == nil {
		return nil, errors.New("agent not connected")
	}
	// In compat mode without httpProxy/terminal protocol integration, status is unknown.
	return &ArthasStatus{
		State:          "unknown",
		ArthasVersion:  a.arthasVersion,
		ActiveSessions: 0,
		MaxSessions:    0,
		UptimeMs:       0,
	}, nil
}

// ===== Deprecated methods from previous JSON terminal protocol =====

func (e *Extension) OpenTerminal(agentID, userID string, cols, rows int) (string, error) {
	return "", errors.New("OpenTerminal is not supported in arthas-uri compat mode")
}

func (e *Extension) CloseTerminal(sessionID string) error {
	return errors.New("CloseTerminal is not supported in arthas-uri compat mode")
}

func (e *Extension) SendTerminalInput(sessionID, data string) error {
	return errors.New("SendTerminalInput is not supported in arthas-uri compat mode")
}

func (e *Extension) ResizeTerminal(sessionID string, cols, rows int) error {
	return errors.New("ResizeTerminal is not supported in arthas-uri compat mode")
}

