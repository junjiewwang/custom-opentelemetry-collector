// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var (
	// ErrMaxProxySessionsReached is returned when the maximum number of proxy sessions is reached.
	ErrMaxProxySessionsReached = errors.New("max proxy sessions reached")
	// ErrInvalidInternalToken is returned when the internal token is invalid.
	ErrInvalidInternalToken = errors.New("invalid internal token")
)

// WSProxy implements CrossNodeProxy using WebSocket connections.
type WSProxy struct {
	logger *zap.Logger
	config *ProxyConfig

	upgrader websocket.Upgrader
	dialer   *websocket.Dialer

	// Session tracking
	activeSessions int64

	// Handler for delivering tunnels to local pendings
	tunnelDeliverer TunnelDeliverer
}

// TunnelDeliverer is called when a proxied openTunnel needs to deliver to local pending.
type TunnelDeliverer interface {
	DeliverTunnel(clientConnID string, conn *websocket.Conn) error
}

// NewWSProxy creates a new WebSocket-based cross-node proxy.
func NewWSProxy(logger *zap.Logger, config *ProxyConfig, deliverer TunnelDeliverer) *WSProxy {
	return &WSProxy{
		logger: logger,
		config: config,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				// Internal endpoints should validate token, not origin
				return true
			},
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
		},
		tunnelDeliverer: deliverer,
	}
}

// ProxyConnectArthas proxies a connectArthas request to the target node.
func (p *WSProxy) ProxyConnectArthas(ctx context.Context, targetNodeAddr string, browserConn *websocket.Conn, agentID string) error {
	// Check session limit
	if p.config.MaxProxySessions > 0 {
		current := atomic.LoadInt64(&p.activeSessions)
		if current >= int64(p.config.MaxProxySessions) {
			return ErrMaxProxySessionsReached
		}
	}
	atomic.AddInt64(&p.activeSessions, 1)
	defer atomic.AddInt64(&p.activeSessions, -1)

	// Build target URL
	targetURL := p.BuildInternalURL(targetNodeAddr, "connect", map[string]string{
		"id": agentID,
	})

	p.logger.Info("Proxying connectArthas to remote node",
		zap.String("target_node", targetNodeAddr),
		zap.String("agent_id", agentID),
		zap.String("target_url", targetURL),
	)

	// Connect to target node
	header := http.Header{}
	header.Set(p.config.InternalTokenHeader, p.config.InternalToken)

	targetConn, resp, err := p.dialer.DialContext(ctx, targetURL, header)
	if err != nil {
		if resp != nil {
			p.logger.Error("Failed to connect to target node",
				zap.String("target_node", targetNodeAddr),
				zap.Int("status_code", resp.StatusCode),
				zap.Error(err),
			)
		}
		return fmt.Errorf("dial target node: %w", err)
	}
	defer targetConn.Close()

	p.logger.Info("Connected to target node, starting relay",
		zap.String("target_node", targetNodeAddr),
		zap.String("agent_id", agentID),
	)

	// Relay between browser and target node
	p.relayWebSocketPair(ctx, browserConn, targetConn)
	return nil
}

// ProxyOpenTunnel proxies an openTunnel request to the target node.
func (p *WSProxy) ProxyOpenTunnel(ctx context.Context, targetNodeAddr string, agentConn *websocket.Conn, clientConnID string) error {
	// Check session limit
	if p.config.MaxProxySessions > 0 {
		current := atomic.LoadInt64(&p.activeSessions)
		if current >= int64(p.config.MaxProxySessions) {
			return ErrMaxProxySessionsReached
		}
	}
	atomic.AddInt64(&p.activeSessions, 1)
	defer atomic.AddInt64(&p.activeSessions, -1)

	// Build target URL
	targetURL := p.BuildInternalURL(targetNodeAddr, "opentunnel", map[string]string{
		"clientConnectionId": clientConnID,
	})

	p.logger.Info("Proxying openTunnel to remote node",
		zap.String("target_node", targetNodeAddr),
		zap.String("client_conn_id", clientConnID),
		zap.String("target_url", targetURL),
	)

	// Connect to target node
	header := http.Header{}
	header.Set(p.config.InternalTokenHeader, p.config.InternalToken)

	targetConn, resp, err := p.dialer.DialContext(ctx, targetURL, header)
	if err != nil {
		if resp != nil {
			p.logger.Error("Failed to connect to target node for openTunnel",
				zap.String("target_node", targetNodeAddr),
				zap.Int("status_code", resp.StatusCode),
				zap.Error(err),
			)
		}
		return fmt.Errorf("dial target node: %w", err)
	}
	defer targetConn.Close()

	p.logger.Info("Connected to target node for openTunnel, starting relay",
		zap.String("target_node", targetNodeAddr),
		zap.String("client_conn_id", clientConnID),
	)

	// Relay between agent and target node
	p.relayWebSocketPair(ctx, agentConn, targetConn)
	return nil
}

// HandleInternalOpenTunnel handles proxied openTunnel requests from other nodes.
// Token validation is done by the caller (Extension.HandleInternalProxy).
func (p *WSProxy) HandleInternalOpenTunnel(w http.ResponseWriter, r *http.Request) {
	clientConnID := r.URL.Query().Get("clientConnectionId")
	if clientConnID == "" {
		http.Error(w, "Missing clientConnectionId", http.StatusBadRequest)
		return
	}

	conn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.logger.Error("Failed to upgrade internal openTunnel WebSocket",
			zap.Error(err),
		)
		return
	}

	p.logger.Info("Internal openTunnel request received",
		zap.String("client_conn_id", clientConnID),
		zap.String("remote_addr", r.RemoteAddr),
	)

	// Deliver the tunnel to the local pending
	if p.tunnelDeliverer != nil {
		if err := p.tunnelDeliverer.DeliverTunnel(clientConnID, conn); err != nil {
			p.logger.Error("Failed to deliver tunnel",
				zap.String("client_conn_id", clientConnID),
				zap.Error(err),
			)
			_ = conn.Close()
			return
		}
	}

	// Connection ownership transferred to the pending handler
}

// Config returns the proxy configuration.
func (p *WSProxy) Config() *ProxyConfig {
	return p.config
}

// BuildInternalURL builds the internal proxy URL for cross-node communication.
func (p *WSProxy) BuildInternalURL(nodeAddr, action string, params map[string]string) string {
	// Ensure nodeAddr has scheme
	if !strings.HasPrefix(nodeAddr, "ws://") && !strings.HasPrefix(nodeAddr, "wss://") {
		nodeAddr = "ws://" + nodeAddr
	}

	u, _ := url.Parse(nodeAddr)
	u.Path = p.config.InternalPathPrefix + "/proxy/" + action

	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// relayWebSocketPair relays messages between two WebSocket connections.
func (p *WSProxy) relayWebSocketPair(ctx context.Context, conn1, conn2 *websocket.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	// conn1 -> conn2
	go func() {
		defer wg.Done()
		p.relayMessages(ctx, conn1, conn2, "conn1->conn2")
	}()

	// conn2 -> conn1
	go func() {
		defer wg.Done()
		p.relayMessages(ctx, conn2, conn1, "conn2->conn1")
	}()

	wg.Wait()
}

// relayMessages relays messages from src to dst.
func (p *WSProxy) relayMessages(ctx context.Context, src, dst *websocket.Conn, direction string) {
	writeTimeout := time.Duration(p.config.WriteTimeout) * time.Second
	if writeTimeout == 0 {
		writeTimeout = 10 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, data, err := src.ReadMessage()
		if err != nil {
			if !isNormalClose(err) {
				p.logger.Debug("Relay read error",
					zap.String("direction", direction),
					zap.Error(err),
				)
			}
			// Close the other connection
			_ = dst.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			return
		}

		_ = dst.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := dst.WriteMessage(msgType, data); err != nil {
			if !isNormalClose(err) {
				p.logger.Debug("Relay write error",
					zap.String("direction", direction),
					zap.Error(err),
				)
			}
			return
		}
	}
}

// isNormalClose checks if the error is a normal WebSocket close.
func isNormalClose(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return true
	}
	return false
}

// Close releases resources.
func (p *WSProxy) Close() error {
	return nil
}

// ActiveSessions returns the number of active proxy sessions.
func (p *WSProxy) ActiveSessions() int64 {
	return atomic.LoadInt64(&p.activeSessions)
}
