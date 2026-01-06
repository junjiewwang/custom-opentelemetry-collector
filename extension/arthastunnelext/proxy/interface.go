// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"
)

// CrossNodeProxy defines the interface for cross-node WebSocket proxying.
type CrossNodeProxy interface {
	// ProxyConnectArthas proxies a connectArthas request to the target node.
	// This establishes: Browser <-> this node <-> target node <-> Agent
	ProxyConnectArthas(ctx context.Context, targetNodeAddr string, browserConn *websocket.Conn, agentID string) error

	// ProxyOpenTunnel proxies an openTunnel request to the target node.
	// This is called when openTunnel arrives at a node different from where pending was created.
	ProxyOpenTunnel(ctx context.Context, targetNodeAddr string, agentConn *websocket.Conn, clientConnID string) error

	// HandleInternalProxy handles incoming internal proxy requests from other nodes.
	// This should be mounted at the internal path prefix.
	HandleInternalProxy(w http.ResponseWriter, r *http.Request)

	// Close releases any resources.
	Close() error
}

// ProxyConfig contains configuration for the cross-node proxy.
type ProxyConfig struct {
	// InternalPathPrefix is the path prefix for internal endpoints.
	InternalPathPrefix string

	// InternalToken is the PSK for authenticating internal requests.
	InternalToken string

	// InternalTokenHeader is the header name for the internal token.
	InternalTokenHeader string

	// WriteTimeout is the write timeout for proxy connections.
	WriteTimeout int // seconds

	// MaxProxySessions limits concurrent proxy sessions.
	MaxProxySessions int
}
