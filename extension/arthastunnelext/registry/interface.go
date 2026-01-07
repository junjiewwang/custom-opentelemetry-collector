// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"time"
)

// AgentInfo represents the metadata of a connected agent.
type AgentInfo struct {
	// AgentID is the unique identifier of the agent.
	AgentID string `json:"agent_id"`

	// AppID is the application ID the agent belongs to.
	AppID string `json:"app_id"`

	// AppName is the application/service name.
	AppName string `json:"app_name"`

	// ArthasVersion is the Arthas version running on the agent.
	ArthasVersion string `json:"arthas_version"`

	// IP is the IP address of the agent.
	IP string `json:"ip"`

	// RemoteAddr is the full remote address (ip:port).
	RemoteAddr string `json:"remote_addr"`

	// NodeID is the collector replica ID that holds the WebSocket connection.
	NodeID string `json:"node_id"`

	// NodeAddr is the internal address of the collector replica for cross-node proxy.
	NodeAddr string `json:"node_addr"`

	// ConnectedAt is when the agent first connected (UnixMilli).
	// Note: Using milliseconds instead of nanoseconds to avoid JSON precision loss
	// when serializing through Redis Lua cjson (which converts large numbers to scientific notation).
	ConnectedAt int64 `json:"connected_at"`

	// LastPongAt is when the last pong was received (UnixMilli).
	// Note: Using milliseconds instead of nanoseconds to avoid JSON precision loss.
	LastPongAt int64 `json:"last_pong_at"`
}

// AgentRegistry defines the interface for agent registration and lookup.
// Implementations may store agents locally (memory) or distributed (Redis).
type AgentRegistry interface {
	// Register registers an agent. If the agent already exists, it will be replaced.
	Register(ctx context.Context, info *AgentInfo) error

	// Unregister removes an agent from the registry.
	Unregister(ctx context.Context, agentID string) error

	// UpdateLiveness updates the last pong time for an agent.
	// This is called frequently and implementations should optimize for this.
	UpdateLiveness(ctx context.Context, agentID string, lastPongAt time.Time) error

	// Get retrieves agent info by ID.
	// Returns nil if not found.
	Get(ctx context.Context, agentID string) (*AgentInfo, error)

	// List returns all healthy agents (not timed out).
	List(ctx context.Context) ([]*AgentInfo, error)

	// ListByAppID returns all healthy agents for a specific app ID.
	ListByAppID(ctx context.Context, appID string) ([]*AgentInfo, error)

	// IsLocal returns true if the agent is connected to this node.
	IsLocal(agentID string) bool

	// GetNodeAddr returns the node address where the agent is connected.
	// Returns empty string if not found.
	GetNodeAddr(ctx context.Context, agentID string) (string, error)

	// Close releases any resources held by the registry.
	Close() error
}

// LivenessChecker is used to determine if an agent is still alive.
type LivenessChecker interface {
	// IsTimeout returns true if the agent has timed out based on lastPongAt (UnixMilli).
	IsTimeout(lastPongAtMilli int64) bool

	// LivenessTimeout returns the timeout duration.
	LivenessTimeout() time.Duration
}
