// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pending

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
)

// PendingInfo represents a pending connection waiting for openTunnel.
type PendingInfo struct {
	// ClientConnID is the unique identifier for this pending connection.
	ClientConnID string `json:"client_conn_id"`

	// SessionID is the session ID returned to the browser.
	SessionID string `json:"session_id"`

	// AgentID is the target agent ID.
	AgentID string `json:"agent_id"`

	// NodeID is the collector replica that created this pending.
	NodeID string `json:"node_id"`

	// NodeAddr is the internal address of the collector replica.
	NodeAddr string `json:"node_addr"`

	// CreatedAt is when the pending was created.
	CreatedAt time.Time `json:"created_at"`
}

// PendingStore defines the interface for managing pending connections.
type PendingStore interface {
	// Create creates a new pending connection.
	// Returns error if the pending already exists.
	Create(ctx context.Context, info *PendingInfo) error

	// Get retrieves pending info by client connection ID.
	// Returns nil if not found.
	Get(ctx context.Context, clientConnID string) (*PendingInfo, error)

	// Delete removes a pending connection.
	Delete(ctx context.Context, clientConnID string) error

	// IsLocal returns true if the pending was created on this node.
	IsLocal(clientConnID string) bool

	// DeliverTunnel delivers a tunnel connection to a local pending.
	// Returns error if the pending is not local or already delivered.
	DeliverTunnel(clientConnID string, conn *websocket.Conn) error

	// WaitForTunnel waits for a tunnel connection to be delivered.
	// Returns the tunnel connection or error on timeout/cancellation.
	WaitForTunnel(ctx context.Context, clientConnID string, timeout time.Duration) (*websocket.Conn, error)

	// GetBrowserConn returns the browser connection for a local pending.
	GetBrowserConn(clientConnID string) *websocket.Conn

	// Close releases any resources held by the store.
	Close() error
}
