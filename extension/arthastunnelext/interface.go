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

	// GetAgentArthasStatus returns the Arthas status for an agent.
	GetAgentArthasStatus(agentID string) (*ArthasStatus, error)

	// ListConnectedAgents returns all agents with active tunnel connections.
	ListConnectedAgents() []*ConnectedAgent

	// IsAgentConnected checks if an agent has an active tunnel connection.
	IsAgentConnected(agentID string) bool

	// OpenTerminal opens a terminal session to an agent.
	// Returns the session ID and error.
	OpenTerminal(agentID, userID string, cols, rows int) (string, error)

	// CloseTerminal closes a terminal session.
	CloseTerminal(sessionID string) error

	// SendTerminalInput sends input to a terminal session.
	SendTerminalInput(sessionID, data string) error

	// ResizeTerminal resizes a terminal session.
	ResizeTerminal(sessionID string, cols, rows int) error
}

// ArthasStatus represents the Arthas status of an agent.
type ArthasStatus struct {
	// State is the current state: "stopped", "starting", "running", "stopping"
	State string `json:"state"`

	// ArthasVersion is the version of Arthas running on the agent.
	ArthasVersion string `json:"arthas_version,omitempty"`

	// ActiveSessions is the number of active terminal sessions.
	ActiveSessions int `json:"active_sessions"`

	// MaxSessions is the maximum number of sessions allowed.
	MaxSessions int `json:"max_sessions"`

	// UptimeMs is the uptime of Arthas in milliseconds.
	UptimeMs int64 `json:"uptime_ms"`
}

// ConnectedAgent represents an agent with an active tunnel connection.
type ConnectedAgent struct {
	// AgentID is the unique identifier of the agent.
	AgentID string `json:"agent_id"`

	// AppID is the application ID the agent belongs to.
	AppID string `json:"app_id"`

	// ServiceName is the service name of the agent.
	ServiceName string `json:"service_name,omitempty"`

	// Hostname is the hostname of the agent.
	Hostname string `json:"hostname,omitempty"`

	// IP is the IP address of the agent.
	IP string `json:"ip,omitempty"`

	// Version is the SDK version of the agent.
	Version string `json:"version,omitempty"`

	// ConnectedAt is when the agent connected.
	ConnectedAt time.Time `json:"connected_at"`

	// LastPingAt is when the last ping was received.
	LastPingAt time.Time `json:"last_ping_at"`

	// ArthasStatus is the current Arthas status.
	ArthasStatus *ArthasStatus `json:"arthas_status,omitempty"`
}

// TerminalSession represents an active terminal session.
type TerminalSession struct {
	// SessionID is the unique identifier of the session.
	SessionID string `json:"session_id"`

	// AgentID is the agent this session is connected to.
	AgentID string `json:"agent_id"`

	// UserID is the user who opened this session.
	UserID string `json:"user_id,omitempty"`

	// Cols is the terminal width.
	Cols int `json:"cols"`

	// Rows is the terminal height.
	Rows int `json:"rows"`

	// CreatedAt is when the session was created.
	CreatedAt time.Time `json:"created_at"`

	// LastActiveAt is when the session was last active.
	LastActiveAt time.Time `json:"last_active_at"`
}
