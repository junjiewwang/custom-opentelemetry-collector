// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/pending"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/registry"
)

type wsIngress string

const (
	ingressAgentGateway wsIngress = "agentgateway"
	ingressAdmin        wsIngress = "admin"
)

type arthasURIMethod string

const (
	methodAgentRegister arthasURIMethod = "agentRegister"
	methodConnectArthas arthasURIMethod = "connectArthas"
	methodOpenTunnel    arthasURIMethod = "openTunnel"

	// server -> agent
	methodStartTunnel arthasURIMethod = "startTunnel"
)

type arthasURICompat struct {
	logger *zap.Logger
	cfg    *Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	upgrader websocket.Upgrader

	// Local state (always used)
	mu      sync.Mutex
	agents  map[string]*compatAgent
	pending map[string]*compatPendingConn

	// Distributed manager (optional, nil in local mode)
	distributed *DistributedManager
}

type compatAgent struct {
	agentID       string
	appName       string
	arthasVersion string
	appID         string

	remoteAddr string

	conn      *websocket.Conn
	writeMu   sync.Mutex // protects concurrent writes to conn (ping vs startTunnel)
	closeOnce sync.Once

	connectedAt int64 // unix nano, set once at registration
	lastPongAt  int64 // unix nano, updated atomically on pong
}

// safeWriteMessage writes a message to the agent connection with mutex protection.
func (a *compatAgent) safeWriteMessage(messageType int, data []byte) error {
	if a == nil || a.conn == nil {
		return errors.New("agent or connection is nil")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteMessage(messageType, data)
}

// safeWriteControl writes a control message to the agent connection with mutex protection.
func (a *compatAgent) safeWriteControl(messageType int, data []byte, deadline time.Time) error {
	if a == nil || a.conn == nil {
		return errors.New("agent or connection is nil")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteControl(messageType, data, deadline)
}

type compatPendingConn struct {
	sessionID          string // returned to browser immediately
	clientConnectionID string
	agentID            string

	browserConn *websocket.Conn

	createdAt time.Time
	tunnelCh  chan *websocket.Conn
	closeOnce sync.Once
}

// ANSI color codes for terminal output.
const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiCyan   = "\033[36m"
)

// Browser session status constants.
const (
	statusConnecting    = "connecting"
	statusWaitingTunnel = "waiting_tunnel"
	statusReady         = "ready"
	statusTimeout       = "timeout"
	statusError         = "error"
	statusClosed        = "closed"
)

// formatTerminalStatus formats a status message with ANSI colors for terminal display.
func formatTerminalStatus(status, message string) string {
	var color string
	var icon string
	switch status {
	case statusConnecting, statusWaitingTunnel:
		color = ansiCyan
		icon = "[*]"
	case statusReady:
		color = ansiGreen
		icon = "[+]"
	case statusTimeout:
		color = ansiYellow
		icon = "[!]"
	case statusError:
		color = ansiRed
		icon = "[-]"
	case statusClosed:
		color = ansiYellow
		icon = "[!]"
	default:
		color = ansiReset
		icon = "[.]"
	}
	return color + icon + " " + message + ansiReset + "\r\n"
}

// sendBrowserStatus sends a status message to the browser connection as raw terminal text.
func sendBrowserStatus(conn *websocket.Conn, sessionID, status, message string) error {
	_ = sessionID // sessionID reserved for future use
	if conn == nil {
		return errors.New("browser connection is nil")
	}
	return conn.WriteMessage(websocket.TextMessage, []byte(formatTerminalStatus(status, message)))
}

func newArthasURICompat(ctx context.Context, logger *zap.Logger, cfg *Config, distributed *DistributedManager) *arthasURICompat {
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	cctx, cancel := context.WithCancel(baseCtx)

	return &arthasURICompat{
		logger: logger,
		cfg:    cfg,
		ctx:    cctx,
		cancel: cancel,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		agents:      make(map[string]*compatAgent),
		pending:     make(map[string]*compatPendingConn),
		distributed: distributed,
	}
}

func (s *arthasURICompat) shutdown(ctx context.Context) {
	if s.cancel != nil {
		s.cancel()
	}

	// Best-effort close all connections.
	s.mu.Lock()
	agents := make([]*compatAgent, 0, len(s.agents))
	for _, a := range s.agents {
		agents = append(agents, a)
	}
	pendings := make([]*compatPendingConn, 0, len(s.pending))
	for _, p := range s.pending {
		pendings = append(pendings, p)
	}
	s.mu.Unlock()

	for _, a := range agents {
		a.close()
	}
	for _, p := range pendings {
		p.close("server_shutdown")
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
		return
	}
}

func (a *compatAgent) close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		if a.conn != nil {
			_ = a.conn.Close()
		}
	})
}

func (p *compatPendingConn) close(reason string) {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.browserConn != nil {
			_ = writeClose(p.browserConn, 2000, reason)
			_ = p.browserConn.Close()
		}
		// tunnel conn is owned by the openTunnel handler/relay once delivered.
	})
}

func (s *arthasURICompat) handleWS(ing wsIngress, w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		remoteAddr = xff
	}

	s.logger.Debug("WebSocket connection attempt",
		zap.String("ingress", string(ing)),
		zap.String("remote_addr", remoteAddr),
		zap.String("request_uri", r.RequestURI),
		zap.String("user_agent", r.UserAgent()),
	)

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("Failed to upgrade WebSocket",
			zap.String("ingress", string(ing)),
			zap.String("remote_addr", remoteAddr),
			zap.String("request_uri", r.RequestURI),
			zap.Error(err),
		)
		return
	}

	method := strings.TrimSpace(r.URL.Query().Get("method"))
	if method == "" {
		s.logger.Warn("WebSocket missing method parameter",
			zap.String("ingress", string(ing)),
			zap.String("remote_addr", remoteAddr),
			zap.String("request_uri", r.RequestURI),
		)
		_ = writeClose(conn, 2000, "missing method")
		_ = conn.Close()
		return
	}

	m := normalizeMethod(method)

	// Enforce ingress allowlist by default.
	if s.cfg.StrictIngressMethodAllowlist {
		if !isMethodAllowed(ing, m) {
			s.logger.Warn("WebSocket method not allowed",
				zap.String("ingress", string(ing)),
				zap.String("method", string(m)),
				zap.String("remote_addr", remoteAddr),
			)
			_ = writeClose(conn, 2000, "method not allowed")
			_ = conn.Close()
			return
		}
	}

	s.logger.Info("WebSocket connection established",
		zap.String("ingress", string(ing)),
		zap.String("method", string(m)),
		zap.String("remote_addr", remoteAddr),
		zap.String("query", r.URL.RawQuery),
	)

	ctx := &compatConnContext{
		ingress:    ing,
		conn:       conn,
		request:    r,
		query:      r.URL.Query(),
		remoteAddr: remoteAddr,
		logger: s.logger.With(
			zap.String("ingress", string(ing)),
			zap.String("method", string(m)),
			zap.String("remote_addr", remoteAddr),
		),
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.dispatch(ctx, m)
	}()
}

type compatConnContext struct {
	ingress    wsIngress
	conn       *websocket.Conn
	request    *http.Request
	query      url.Values
	remoteAddr string
	logger     *zap.Logger
}

func normalizeMethod(method string) arthasURIMethod {
	m := strings.TrimSpace(method)
	m = strings.TrimPrefix(m, "/")
	m = strings.TrimSpace(m)
	// accept case-insensitive inputs
	lm := strings.ToLower(m)
	switch lm {
	case strings.ToLower(string(methodAgentRegister)):
		return methodAgentRegister
	case strings.ToLower(string(methodConnectArthas)):
		return methodConnectArthas
	case strings.ToLower(string(methodOpenTunnel)):
		return methodOpenTunnel
	default:
		// preserve original for error message
		return arthasURIMethod(m)
	}
}

func isMethodAllowed(ing wsIngress, m arthasURIMethod) bool {
	switch ing {
	case ingressAgentGateway:
		return m == methodAgentRegister || m == methodOpenTunnel
	case ingressAdmin:
		return m == methodConnectArthas
	default:
		return false
	}
}

func (s *arthasURICompat) dispatch(ctx *compatConnContext, m arthasURIMethod) {
	switch m {
	case methodAgentRegister:
		s.handleAgentRegister(ctx)
	case methodConnectArthas:
		s.handleConnectArthas(ctx)
	case methodOpenTunnel:
		s.handleOpenTunnel(ctx)
	default:
		ctx.logger.Warn("Unsupported method")
		_ = writeClose(ctx.conn, 2000, "unsupported method")
		_ = ctx.conn.Close()
	}
}

func (s *arthasURICompat) handleAgentRegister(ctx *compatConnContext) {
	agentID, appName, arthasVersion := s.resolveAgentRegisterParams(ctx.query)
	appID := ctx.request.Header.Get("X-App-ID")

	remoteAddr := ctx.remoteAddr
	if ra := ctx.request.Header.Get("X-Forwarded-For"); ra != "" {
		remoteAddr = ra
	}

	now := time.Now().UnixNano()
	a := &compatAgent{
		agentID:       agentID,
		appName:       appName,
		arthasVersion: arthasVersion,
		appID:         appID,
		remoteAddr:    remoteAddr,
		conn:          ctx.conn,
		connectedAt:   now,
		lastPongAt:    now,
	}

	// Replace old agent connection if exists.
	s.mu.Lock()
	if old, ok := s.agents[agentID]; ok {
		ctx.logger.Info("Replacing existing agentRegister connection",
			zap.String("agent_id", agentID),
		)
		old.close()
	}
	s.agents[agentID] = a
	s.mu.Unlock()

	// Register in distributed registry if enabled
	if s.distributed != nil {
		ip := remoteAddr
		if idx := strings.LastIndex(remoteAddr, ":"); idx > 0 {
			ip = remoteAddr[:idx]
		}
		agentInfo := &registry.AgentInfo{
			AgentID:       agentID,
			AppID:         appID,
			AppName:       appName,
			ArthasVersion: arthasVersion,
			IP:            ip,
			RemoteAddr:    remoteAddr,
			NodeID:        s.distributed.NodeID(),
			NodeAddr:      s.distributed.NodeAddr(),
			ConnectedAt:   time.Unix(0, now),
			LastPongAt:    time.Unix(0, now),
		}
		if err := s.distributed.Registry().Register(s.ctx, agentInfo); err != nil {
			ctx.logger.Warn("Failed to register agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}

	ctx.logger.Info("Agent registered",
		zap.String("agent_id", agentID),
		zap.String("app_name", appName),
		zap.String("arthas_version", arthasVersion),
		zap.String("app_id", appID),
		zap.String("remote_addr", remoteAddr),
	)

	// Send response frame: response:/?method=agentRegister&id=xxx
	vals := url.Values{}
	vals.Set("method", string(methodAgentRegister))
	vals.Set("id", agentID)
	resp := buildResponseFrame(vals)
	_ = ctx.conn.WriteMessage(websocket.TextMessage, []byte(resp))

	// Keep the control connection alive and detect disconnect.
	s.runAgentControlLoops(ctx, a)

	// Cleanup on disconnect.
	s.mu.Lock()
	if cur, ok := s.agents[agentID]; ok && cur == a {
		delete(s.agents, agentID)
		ctx.logger.Info("Agent disconnected and removed from registry",
			zap.String("agent_id", agentID),
			zap.Duration("connection_duration", time.Since(time.Unix(0, a.connectedAt))),
		)
	}
	s.mu.Unlock()

	// Unregister from distributed registry
	if s.distributed != nil {
		if err := s.distributed.Registry().Unregister(s.ctx, agentID); err != nil {
			ctx.logger.Warn("Failed to unregister agent from distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}
}

func (s *arthasURICompat) resolveAgentRegisterParams(q url.Values) (agentID string, appName string, arthasVersion string) {
	appName = q.Get("appName")
	arthasVersion = q.Get("arthasVersion")

	id := q.Get("id")
	if id != "" {
		return id, appName, arthasVersion
	}

	randID := randomUpperAlphaNum(20)
	if appName != "" {
		return appName + "_" + randID, appName, arthasVersion
	}
	return randID, appName, arthasVersion
}

func (s *arthasURICompat) runAgentControlLoops(ctx *compatConnContext, a *compatAgent) {
	// Read loop to process control frames (and to detect disconnect).
	// We don't implement httpProxy in this phase; frames are ignored.
	readDone := make(chan struct{})

	// Use livenessTimeout for ReadDeadline (unified with ListAgents filter).
	livenessTimeout := s.livenessTimeout()

	// Configure pong handler to track liveness (atomic update).
	_ = ctx.conn.SetReadDeadline(time.Now().Add(livenessTimeout))
	ctx.conn.SetPongHandler(func(string) error {
		pongTime := time.Now()
		atomic.StoreInt64(&a.lastPongAt, pongTime.UnixNano())
		_ = ctx.conn.SetReadDeadline(pongTime.Add(livenessTimeout))

		// Record pong for batched Redis update in distributed mode
		if s.distributed != nil {
			s.distributed.RecordPong(a.agentID, pongTime)
		}
		return nil
	})

	// Send first ping immediately to reduce initial window without pong update.
	_ = a.safeWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(readDone)
		for {
			_, _, err := ctx.conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Ping loop.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.pingInterval())
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-readDone:
				return
			case <-t.C:
				// Use safeWriteControl to protect against concurrent writes.
				_ = a.safeWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			}
		}
	}()

	<-readDone
}

func (s *arthasURICompat) handleConnectArthas(ctx *compatConnContext) {
	agentID := ctx.query.Get("id")

	ctx.logger.Info("[connectArthas] Received browser connect request",
		zap.String("requested_agent_id", agentID),
		zap.String("full_query", ctx.request.URL.RawQuery),
	)

	if agentID == "" {
		ctx.logger.Warn("[connectArthas] Missing agent id in request")
		_ = sendBrowserStatus(ctx.conn, "", statusError, "Missing agent ID parameter")
		_ = ctx.conn.Close()
		return
	}

	// Generate sessionID immediately for browser.
	sessionID := randomUpperAlphaNum(20)

	// Step 1: Check if agent is online (local first, then distributed registry)
	var agent *compatAgent
	var agentNodeAddr string
	var isLocalAgent bool

	s.mu.Lock()
	agent = s.agents[agentID]
	registeredAgentIDs := make([]string, 0, len(s.agents))
	for id := range s.agents {
		registeredAgentIDs = append(registeredAgentIDs, id)
	}
	s.mu.Unlock()

	isLocalAgent = agent != nil && agent.conn != nil

	// In distributed mode, check if agent is on another node
	if !isLocalAgent && s.distributed != nil {
		var err error
		agentNodeAddr, err = s.distributed.GetAgentNodeAddr(s.ctx, agentID)
		if err != nil {
			ctx.logger.Warn("[connectArthas] Failed to lookup agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}

	ctx.logger.Info("[connectArthas] Looking up agent",
		zap.String("requested_agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.Strings("local_agents", registeredAgentIDs),
		zap.Bool("local_agent_found", isLocalAgent),
		zap.String("remote_node_addr", agentNodeAddr),
	)

	// If agent not found anywhere, return error
	if !isLocalAgent && agentNodeAddr == "" {
		ctx.logger.Warn("[connectArthas] Agent not online",
			zap.String("requested_agent_id", agentID),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError,
			"Agent is offline. Please ensure the target application has started Arthas and connected to the server.")
		_ = ctx.conn.Close()
		return
	}

	// If agent is on another node, proxy the request
	if !isLocalAgent && agentNodeAddr != "" {
		ctx.logger.Info("[connectArthas] Agent is on another node, proxying request",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("target_node", agentNodeAddr),
		)
		s.proxyConnectArthas(ctx, agentID, sessionID, agentNodeAddr)
		return
	}

	// Agent is local, proceed with normal flow
	ctx.logger.Info("[connectArthas] Agent found locally",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("agent_app_name", agent.appName),
		zap.String("agent_arthas_version", agent.arthasVersion),
		zap.String("agent_remote_addr", agent.remoteAddr),
		zap.Time("agent_connected_at", time.Unix(0, agent.connectedAt)),
	)

	// Step 2: Send initial status to browser immediately.
	if err := sendBrowserStatus(ctx.conn, sessionID, statusConnecting, "Connecting to agent..."); err != nil {
		ctx.logger.Error("[connectArthas] Failed to send initial status to browser",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		_ = ctx.conn.Close()
		return
	}

	clientConnID := randomUpperAlphaNum(20)
	localPending := &compatPendingConn{
		sessionID:          sessionID,
		clientConnectionID: clientConnID,
		agentID:            agentID,
		browserConn:        ctx.conn,
		createdAt:          time.Now(),
		tunnelCh:           make(chan *websocket.Conn, 1),
	}

	// Step 3: Register pending before sending startTunnel.
	s.mu.Lock()
	if s.cfg.MaxPendingConnections > 0 && len(s.pending) >= s.cfg.MaxPendingConnections {
		s.mu.Unlock()
		ctx.logger.Warn("[connectArthas] Server busy, too many pending connections",
			zap.Int("pending_count", len(s.pending)),
			zap.Int("max_pending", s.cfg.MaxPendingConnections),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Server busy, please try again later")
		_ = ctx.conn.Close()
		return
	}
	s.pending[clientConnID] = localPending
	pendingCount := len(s.pending)
	s.mu.Unlock()

	// Also register in distributed pending store if enabled
	if s.distributed != nil {
		pendingInfo := &pending.PendingInfo{
			ClientConnID: clientConnID,
			SessionID:    sessionID,
			AgentID:      agentID,
			NodeID:       s.distributed.NodeID(),
			NodeAddr:     s.distributed.NodeAddr(),
			CreatedAt:    localPending.createdAt,
		}
		if err := s.distributed.PendingStore().CreateWithBrowserConn(s.ctx, pendingInfo, ctx.conn); err != nil {
			ctx.logger.Warn("[connectArthas] Failed to register pending in distributed store",
				zap.String("client_connection_id", clientConnID),
				zap.Error(err),
			)
			// Continue with local only
		}
	}

	ctx.logger.Info("[connectArthas] Registered pending connection",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.Int("total_pending", pendingCount),
	)

	// Step 4: Send startTunnel to agent via the existing agentRegister connection.
	vals := url.Values{}
	vals.Set("method", string(methodStartTunnel))
	vals.Set("id", agentID)
	vals.Set("clientConnectionId", clientConnID)
	startTunnel := buildResponseFrame(vals)

	ctx.logger.Info("[connectArthas] Sending startTunnel to agent",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.String("startTunnel_message", startTunnel),
	)

	// Use safeWriteMessage to protect against concurrent writes (ping vs startTunnel).
	if err := agent.safeWriteMessage(websocket.TextMessage, []byte(startTunnel)); err != nil {
		ctx.logger.Error("[connectArthas] Failed to send startTunnel to agent",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.Error(err),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Failed to notify agent, please try again later")
		localPending.close("startTunnel send failed")
		s.cleanupPending(clientConnID)
		return
	}

	// Step 5: Send waiting status to browser.
	if err := sendBrowserStatus(ctx.conn, sessionID, statusWaitingTunnel, "Waiting for agent to establish tunnel..."); err != nil {
		ctx.logger.Warn("[connectArthas] Failed to send waiting status to browser",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		// Continue anyway, browser might still receive tunnel.
	}

	ctx.logger.Info("[connectArthas] startTunnel sent successfully, waiting for agent openTunnel",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.Duration("timeout", s.connectTimeout()),
	)

	// Step 6: Wait for openTunnel from agent.
	timeout := s.connectTimeout()
	t := time.NewTimer(timeout)
	defer t.Stop()

	select {
	case <-s.ctx.Done():
		ctx.logger.Warn("[connectArthas] Server shutdown while waiting for openTunnel",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusClosed, "Server is under maintenance, please try again later")
		localPending.close("server shutdown")
		s.cleanupPending(clientConnID)
		return
	case <-t.C:
		ctx.logger.Error("[connectArthas] Timeout waiting for agent openTunnel",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.Duration("timeout", timeout),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusTimeout, "Timeout waiting for agent response, please check network or retry")
		localPending.close("timeout waiting for openTunnel")
		s.cleanupPending(clientConnID)
		return
	case tunnelConn := <-localPending.tunnelCh:
		// Success: remove pending and start relay.
		s.cleanupPending(clientConnID)

		ctx.logger.Info("[connectArthas] openTunnel received, tunnel bound successfully",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.String("tunnel_remote_addr", tunnelConn.RemoteAddr().String()),
		)

		// Step 7: Send ready status to browser.
		if err := sendBrowserStatus(ctx.conn, sessionID, statusReady, "Connected successfully, terminal is ready"); err != nil {
			ctx.logger.Warn("[connectArthas] Failed to send ready status to browser",
				zap.String("session_id", sessionID),
				zap.Error(err),
			)
			// Continue anyway, relay should still work.
		}

		ctx.logger.Info("[connectArthas] Starting WebSocket relay",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.String("browser_addr", ctx.remoteAddr),
			zap.String("agent_tunnel_addr", tunnelConn.RemoteAddr().String()),
		)

		relayWebSocketPair(s.ctx, ctx.logger, ctx.conn, tunnelConn)
		return
	}
}

// proxyConnectArthas proxies the connectArthas request to another node.
func (s *arthasURICompat) proxyConnectArthas(ctx *compatConnContext, agentID, sessionID, targetNodeAddr string) {
	if s.distributed == nil || s.distributed.Proxy() == nil {
		ctx.logger.Error("[connectArthas] Distributed proxy not available")
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Internal error: distributed proxy not available")
		_ = ctx.conn.Close()
		return
	}

	// Send status to browser
	if err := sendBrowserStatus(ctx.conn, sessionID, statusConnecting, "Connecting to agent (cross-node)..."); err != nil {
		ctx.logger.Warn("[connectArthas] Failed to send connecting status",
			zap.Error(err),
		)
	}

	// Proxy the WebSocket connection to the target node
	err := s.distributed.Proxy().ProxyConnectArthas(s.ctx, targetNodeAddr, ctx.conn, agentID)
	if err != nil {
		ctx.logger.Error("[connectArthas] Failed to proxy to target node",
			zap.String("agent_id", agentID),
			zap.String("target_node", targetNodeAddr),
			zap.Error(err),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Failed to connect to agent node")
		_ = ctx.conn.Close()
	}
}

// cleanupPending removes a pending connection from both local and distributed stores.
func (s *arthasURICompat) cleanupPending(clientConnID string) {
	s.mu.Lock()
	delete(s.pending, clientConnID)
	s.mu.Unlock()

	if s.distributed != nil {
		_ = s.distributed.PendingStore().Delete(s.ctx, clientConnID)
	}
}

func (s *arthasURICompat) handleOpenTunnel(ctx *compatConnContext) {
	clientConnID := firstNonEmpty(
		ctx.query.Get("clientConnectionId"),
		ctx.query.Get("client_connection_id"),
	)

	ctx.logger.Info("[openTunnel] Received agent openTunnel request",
		zap.String("client_connection_id", clientConnID),
		zap.String("full_query", ctx.request.URL.RawQuery),
	)

	if clientConnID == "" {
		ctx.logger.Warn("[openTunnel] Missing clientConnectionId in request")
		_ = writeClose(ctx.conn, 2000, "missing clientConnectionId")
		_ = ctx.conn.Close()
		return
	}

	// Step 1: Check local pending first
	s.mu.Lock()
	localPending := s.pending[clientConnID]
	pendingIDs := make([]string, 0, len(s.pending))
	for id := range s.pending {
		pendingIDs = append(pendingIDs, id)
	}
	s.mu.Unlock()

	ctx.logger.Info("[openTunnel] Looking up pending connection",
		zap.String("client_connection_id", clientConnID),
		zap.Strings("local_pending_ids", pendingIDs),
		zap.Bool("local_pending_found", localPending != nil),
	)

	// If found locally, deliver directly
	if localPending != nil {
		ctx.logger.Info("[openTunnel] Found local pending connection, attempting to bind tunnel",
			zap.String("client_connection_id", clientConnID),
			zap.String("pending_agent_id", localPending.agentID),
			zap.Time("pending_created_at", localPending.createdAt),
			zap.Duration("wait_duration", time.Since(localPending.createdAt)),
		)

		select {
		case localPending.tunnelCh <- ctx.conn:
			ctx.logger.Info("[openTunnel] Tunnel bound successfully (local)",
				zap.String("client_connection_id", clientConnID),
				zap.String("agent_id", localPending.agentID),
				zap.String("tunnel_remote_addr", ctx.remoteAddr),
			)
			return
		default:
			// Already delivered.
			ctx.logger.Warn("[openTunnel] Tunnel already bound (duplicate openTunnel?)",
				zap.String("client_connection_id", clientConnID),
			)
			_ = writeClose(ctx.conn, 2000, "tunnel already bound")
			_ = ctx.conn.Close()
			return
		}
	}

	// Step 2: In distributed mode, check if pending is on another node
	if s.distributed != nil {
		pendingInfo, err := s.distributed.PendingStore().Get(s.ctx, clientConnID)
		if err != nil {
			ctx.logger.Warn("[openTunnel] Failed to lookup pending in distributed store",
				zap.String("client_connection_id", clientConnID),
				zap.Error(err),
			)
		}

		if pendingInfo != nil && pendingInfo.NodeID != s.distributed.NodeID() {
			// Pending is on another node, proxy the tunnel
			ctx.logger.Info("[openTunnel] Pending is on another node, proxying tunnel",
				zap.String("client_connection_id", clientConnID),
				zap.String("target_node_id", pendingInfo.NodeID),
				zap.String("target_node_addr", pendingInfo.NodeAddr),
			)
			s.proxyOpenTunnel(ctx, clientConnID, pendingInfo.NodeAddr)
			return
		}
	}

	// Not found anywhere
	ctx.logger.Warn("[openTunnel] No pending connection found",
		zap.String("client_connection_id", clientConnID),
		zap.Strings("available_local_pending_ids", pendingIDs),
	)
	_ = writeClose(ctx.conn, 2000, "no pending connection")
	_ = ctx.conn.Close()
}

// proxyOpenTunnel proxies the openTunnel request to another node.
func (s *arthasURICompat) proxyOpenTunnel(ctx *compatConnContext, clientConnID, targetNodeAddr string) {
	if s.distributed == nil || s.distributed.Proxy() == nil {
		ctx.logger.Error("[openTunnel] Distributed proxy not available")
		_ = writeClose(ctx.conn, 2000, "internal error")
		_ = ctx.conn.Close()
		return
	}

	err := s.distributed.Proxy().ProxyOpenTunnel(s.ctx, targetNodeAddr, ctx.conn, clientConnID)
	if err != nil {
		ctx.logger.Error("[openTunnel] Failed to proxy to target node",
			zap.String("client_connection_id", clientConnID),
			zap.String("target_node", targetNodeAddr),
			zap.Error(err),
		)
		_ = writeClose(ctx.conn, 2000, "proxy failed")
		_ = ctx.conn.Close()
	}
}

func (s *arthasURICompat) pingInterval() time.Duration {
	if s.cfg.PingInterval > 0 {
		return s.cfg.PingInterval
	}
	return 20 * time.Second
}

func (s *arthasURICompat) pongTimeout() time.Duration {
	if s.cfg.PongTimeout > 0 {
		return s.cfg.PongTimeout
	}
	return 60 * time.Second
}

func (s *arthasURICompat) livenessGrace() time.Duration {
	if s.cfg.LivenessGrace > 0 {
		return s.cfg.LivenessGrace
	}
	return 30 * time.Second
}

// livenessTimeout returns the unified timeout for both ReadDeadline and ListAgents filter.
// livenessTimeout = pongTimeout + livenessGrace
func (s *arthasURICompat) livenessTimeout() time.Duration {
	return s.pongTimeout() + s.livenessGrace()
}

// ListAgents returns a snapshot of all connected and healthy agents.
// In distributed mode, returns agents from all nodes via Redis.
func (s *arthasURICompat) ListAgents() []*ConnectedAgent {
	// In distributed mode, use the registry to get all agents
	if s.distributed != nil {
		agentInfos, err := s.distributed.Registry().List(s.ctx)
		if err != nil {
			s.logger.Warn("Failed to list agents from distributed registry, falling back to local",
				zap.Error(err),
			)
			// Fall through to local
		} else {
			agents := make([]*ConnectedAgent, 0, len(agentInfos))
			for _, info := range agentInfos {
				agents = append(agents, &ConnectedAgent{
					AgentID:     info.AgentID,
					AppID:       info.AppID,
					ServiceName: info.AppName,
					IP:          info.IP,
					Version:     info.ArthasVersion,
					ConnectedAt: info.ConnectedAt,
					LastPingAt:  info.LastPongAt,
				})
			}
			return agents
		}
	}

	// Local mode or fallback
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := make([]*ConnectedAgent, 0, len(s.agents))
	for _, a := range s.agents {
		if a == nil || a.conn == nil {
			continue
		}
		// Filter out timed-out agents.
		if s.isAgentTimeout(a) {
			continue
		}
		agents = append(agents, a.toConnectedAgent())
	}
	return agents
}

// IsAgentOnline checks if agent is registered and healthy (lastPingAt not timeout).
// In distributed mode, checks both local and Redis registry.
func (s *arthasURICompat) IsAgentOnline(agentID string) bool {
	// Check local first
	s.mu.Lock()
	a := s.agents[agentID]
	s.mu.Unlock()

	if a != nil && a.conn != nil && !s.isAgentTimeout(a) {
		return true
	}

	// In distributed mode, check Redis registry
	if s.distributed != nil {
		info, err := s.distributed.Registry().Get(s.ctx, agentID)
		if err != nil {
			s.logger.Warn("Failed to check agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
			return false
		}
		return info != nil
	}

	return false
}

// isAgentTimeout checks if agent's lastPongAt exceeds livenessTimeout.
// Uses atomic read to avoid data race with pong handler.
func (s *arthasURICompat) isAgentTimeout(a *compatAgent) bool {
	if a == nil {
		return true
	}
	lastPong := atomic.LoadInt64(&a.lastPongAt)
	return time.Since(time.Unix(0, lastPong)) > s.livenessTimeout()
}

// toConnectedAgent converts compatAgent to the public ConnectedAgent struct.
// Uses atomic read for lastPongAt to avoid data race.
func (a *compatAgent) toConnectedAgent() *ConnectedAgent {
	if a == nil {
		return nil
	}

	// Parse IP from remoteAddr ("ip:port" -> "ip").
	ip := a.remoteAddr
	if idx := strings.LastIndex(a.remoteAddr, ":"); idx > 0 {
		ip = a.remoteAddr[:idx]
	}

	return &ConnectedAgent{
		AgentID:     a.agentID,
		AppID:       a.appID,
		ServiceName: a.appName,
		IP:          ip,
		Version:     a.arthasVersion,
		ConnectedAt: time.Unix(0, a.connectedAt),
		LastPingAt:  time.Unix(0, atomic.LoadInt64(&a.lastPongAt)),
	}
}

func (s *arthasURICompat) connectTimeout() time.Duration {
	if s.cfg.CompatConnectTimeout > 0 {
		return s.cfg.CompatConnectTimeout
	}
	return 20 * time.Second
}

func randomUpperAlphaNum(n int) string {
	// Generate random bytes, hex-encode and uppercase, then slice.
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	s := strings.ToUpper(hex.EncodeToString(b))
	if len(s) >= n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func buildResponseFrame(vals url.Values) string {
	// Official format: response:/?k=v
	return "response:/?" + vals.Encode()
}

func writeClose(conn *websocket.Conn, code int, reason string) error {
	if conn == nil {
		return errors.New("nil conn")
	}
	deadline := time.Now().Add(2 * time.Second)
	msg := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, msg, deadline)
	return nil
}
