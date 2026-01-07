// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"net/http"
	"time"
)

// ArthasTunnel defines the interface for Arthas tunnel service.
// This interface is used by agentgatewayreceiver and adminext to interact with the tunnel.
type ArthasTunnel interface {
	// HandleAgentWebSocket handles WebSocket connections from agents.
	// This is called by agentgatewayreceiver when an agent connects to /v1/arthas/ws.
	HandleAgentWebSocket(w http.ResponseWriter, r *http.Request)

	// HandleBrowserWebSocket handles WebSocket connections from browsers.
	// This is called by adminext when a browser connects to /api/v1/arthas/ws.
	HandleBrowserWebSocket(w http.ResponseWriter, r *http.Request)

	// HandleInternalProxy handles internal cross-node proxy requests.
	// This is called by adminext to handle requests at the internal path prefix.
	// Internal authentication (token validation) is handled within this method.
	HandleInternalProxy(w http.ResponseWriter, r *http.Request)

	// ListConnectedAgents returns all registered agents that are healthy.
	ListConnectedAgents() []*ConnectedAgent

	// IsAgentConnected checks if an agent is registered AND healthy (lastPingAt not timeout).
	IsAgentConnected(agentID string) bool

	// IsDistributedMode returns true if distributed mode is enabled and active.
	IsDistributedMode() bool

	// GetInternalPathPrefix returns the internal path prefix for cross-node proxy.
	// This is used by adminext to mount the internal proxy handler.
	GetInternalPathPrefix() string
}

// ConnectedAgent represents an agent with an active tunnel connection.
type ConnectedAgent struct {
	// AgentID is the unique identifier of the agent.
	AgentID string `json:"agent_id"`

	// AppID is the application ID the agent belongs to.
	AppID string `json:"app_id"`

	// ServiceName is the service name of the agent.
	ServiceName string `json:"service_name,omitempty"`

	// IP is the IP address of the agent.
	IP string `json:"ip,omitempty"`

	// Version is the Arthas version of the agent.
	Version string `json:"version,omitempty"`

	// ConnectedAt is when the agent connected.
	ConnectedAt time.Time `json:"connected_at"`

	// LastPingAt is when the last ping was received.
	LastPingAt time.Time `json:"last_ping_at"`
}
