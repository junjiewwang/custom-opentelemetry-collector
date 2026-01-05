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
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
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

	mu      sync.Mutex
	agents  map[string]*compatAgent
	pending map[string]*compatPendingConn
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

	connectedAt time.Time
	lastPingAt  time.Time
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

func newArthasURICompat(ctx context.Context, logger *zap.Logger, cfg *Config) *arthasURICompat {
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
		agents:  make(map[string]*compatAgent),
		pending: make(map[string]*compatPendingConn),
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

	a := &compatAgent{
		agentID:       agentID,
		appName:       appName,
		arthasVersion: arthasVersion,
		appID:         appID,
		remoteAddr:    remoteAddr,
		conn:          ctx.conn,
		connectedAt:   time.Now(),
		lastPingAt:    time.Now(),
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
	}
	s.mu.Unlock()
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

	// Configure pong handler to track liveness.
	_ = ctx.conn.SetReadDeadline(time.Now().Add(s.pongTimeout()))
	ctx.conn.SetPongHandler(func(string) error {
		a.lastPingAt = time.Now()
		_ = ctx.conn.SetReadDeadline(time.Now().Add(s.pongTimeout()))
		return nil
	})

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

	// Step 1: Strong validation - Agent must be registered (prerequisite).
	s.mu.Lock()
	agent := s.agents[agentID]
	registeredAgentIDs := make([]string, 0, len(s.agents))
	for id := range s.agents {
		registeredAgentIDs = append(registeredAgentIDs, id)
	}
	s.mu.Unlock()

	ctx.logger.Info("[connectArthas] Looking up agent",
		zap.String("requested_agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.Strings("registered_agents", registeredAgentIDs),
		zap.Bool("agent_found", agent != nil),
	)

	// Strong validation: if agent not found, return error immediately.
	if agent == nil || agent.conn == nil {
		ctx.logger.Warn("[connectArthas] Agent not online",
			zap.String("requested_agent_id", agentID),
			zap.Bool("agent_nil", agent == nil),
			zap.Bool("conn_nil", agent != nil && agent.conn == nil),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError,
			"Agent is offline. Please ensure the target application has started Arthas and connected to the server.")
		_ = ctx.conn.Close()
		return
	}

	ctx.logger.Info("[connectArthas] Agent found",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("agent_app_name", agent.appName),
		zap.String("agent_arthas_version", agent.arthasVersion),
		zap.String("agent_remote_addr", agent.remoteAddr),
		zap.Time("agent_connected_at", agent.connectedAt),
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
	pending := &compatPendingConn{
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
	s.pending[clientConnID] = pending
	pendingCount := len(s.pending)
	s.mu.Unlock()

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
		pending.close("startTunnel send failed")
		s.mu.Lock()
		delete(s.pending, clientConnID)
		s.mu.Unlock()
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
		pending.close("server shutdown")
		s.mu.Lock()
		delete(s.pending, clientConnID)
		s.mu.Unlock()
		return
	case <-t.C:
		ctx.logger.Error("[connectArthas] Timeout waiting for agent openTunnel",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.Duration("timeout", timeout),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusTimeout, "Timeout waiting for agent response, please check network or retry")
		pending.close("timeout waiting for openTunnel")
		s.mu.Lock()
		delete(s.pending, clientConnID)
		s.mu.Unlock()
		return
	case tunnelConn := <-pending.tunnelCh:
		// Success: remove pending and start relay.
		s.mu.Lock()
		delete(s.pending, clientConnID)
		s.mu.Unlock()

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

	// List all pending connections for debugging
	s.mu.Lock()
	pending := s.pending[clientConnID]
	pendingIDs := make([]string, 0, len(s.pending))
	for id := range s.pending {
		pendingIDs = append(pendingIDs, id)
	}
	s.mu.Unlock()

	ctx.logger.Info("[openTunnel] Looking up pending connection",
		zap.String("client_connection_id", clientConnID),
		zap.Strings("all_pending_ids", pendingIDs),
		zap.Bool("pending_found", pending != nil),
	)

	if pending == nil {
		ctx.logger.Warn("[openTunnel] No pending connection found",
			zap.String("client_connection_id", clientConnID),
			zap.Strings("available_pending_ids", pendingIDs),
		)
		_ = writeClose(ctx.conn, 2000, "no pending connection")
		_ = ctx.conn.Close()
		return
	}

	ctx.logger.Info("[openTunnel] Found pending connection, attempting to bind tunnel",
		zap.String("client_connection_id", clientConnID),
		zap.String("pending_agent_id", pending.agentID),
		zap.Time("pending_created_at", pending.createdAt),
		zap.Duration("wait_duration", time.Since(pending.createdAt)),
	)

	select {
	case pending.tunnelCh <- ctx.conn:
		ctx.logger.Info("[openTunnel] Tunnel bound successfully",
			zap.String("client_connection_id", clientConnID),
			zap.String("agent_id", pending.agentID),
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

func (s *arthasURICompat) pingInterval() time.Duration {
	if s.cfg.PingInterval > 0 {
		return s.cfg.PingInterval
	}
	return 30 * time.Second
}

func (s *arthasURICompat) pongTimeout() time.Duration {
	if s.cfg.PongTimeout > 0 {
		return s.cfg.PongTimeout
	}
	return 60 * time.Second
}

// ListAgents returns a snapshot of all connected and healthy agents.
func (s *arthasURICompat) ListAgents() []*ConnectedAgent {
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
func (s *arthasURICompat) IsAgentOnline(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.agents[agentID]
	if a == nil || a.conn == nil {
		return false
	}
	return !s.isAgentTimeout(a)
}

// isAgentTimeout checks if agent's lastPingAt exceeds pongTimeout.
// Caller must hold s.mu lock.
func (s *arthasURICompat) isAgentTimeout(a *compatAgent) bool {
	if a == nil {
		return true
	}
	return time.Since(a.lastPingAt) > s.pongTimeout()
}

// toConnectedAgent converts compatAgent to the public ConnectedAgent struct.
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
		ConnectedAt: a.connectedAt,
		LastPingAt:  a.lastPingAt,
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
