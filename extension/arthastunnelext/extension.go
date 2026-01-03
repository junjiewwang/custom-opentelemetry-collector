// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
)

var _ extension.Extension = (*Extension)(nil)
var _ ArthasTunnel = (*Extension)(nil)

// Extension implements the Arthas tunnel extension.
type Extension struct {
	config   *Config
	logger   *zap.Logger
	settings extension.Settings

	// Connection managers
	agentConns   *agentConnectionManager
	browserConns *browserConnectionManager

	// Session manager
	sessions *sessionManager

	// WebSocket upgrader
	upgrader websocket.Upgrader

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	startOnce  sync.Once
	stopOnce   sync.Once
}

// newExtension creates a new Arthas tunnel extension.
func newExtension(set extension.Settings, cfg *Config) (*Extension, error) {
	return &Extension{
		config:   cfg,
		logger:   set.Logger,
		settings: set,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for now
			},
		},
	}, nil
}

// Start implements component.Component.
func (e *Extension) Start(ctx context.Context, host component.Host) error {
	var err error
	e.startOnce.Do(func() {
		err = e.start(ctx, host)
	})
	return err
}

func (e *Extension) start(ctx context.Context, _ component.Host) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	// Initialize managers
	e.agentConns = newAgentConnectionManager(e.logger)
	e.browserConns = newBrowserConnectionManager(e.logger)
	e.sessions = newSessionManager(e.config, e.logger)

	// Start cleanup goroutine
	e.wg.Add(1)
	go e.cleanupLoop()

	e.logger.Info("Arthas tunnel extension started",
		zap.Int("max_sessions_per_agent", e.config.MaxSessionsPerAgent),
		zap.Duration("session_idle_timeout", e.config.SessionIdleTimeout),
	)

	return nil
}

// Shutdown implements component.Component.
func (e *Extension) Shutdown(ctx context.Context) error {
	var err error
	e.stopOnce.Do(func() {
		err = e.shutdown(ctx)
	})
	return err
}

func (e *Extension) shutdown(_ context.Context) error {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()

	e.logger.Info("Arthas tunnel extension stopped")
	return nil
}

// cleanupLoop periodically cleans up expired sessions.
func (e *Extension) cleanupLoop() {
	defer e.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			expired := e.sessions.cleanupExpiredSessions()
			// Notify agents and browsers about closed sessions
			for _, session := range expired {
				e.notifySessionClosed(session, "session_expired")
			}
		}
	}
}

// notifySessionClosed notifies both agent and browser that a session is closed.
func (e *Extension) notifySessionClosed(session *terminalSessionInfo, reason string) {
	// Notify agent
	if agentConn := e.agentConns.getByAgentID(session.AgentID); agentConn != nil {
		_ = agentConn.send(NewTerminalCloseMessage(session.SessionID))
	}

	// Notify browser
	if browserConn := e.browserConns.get(session.browserConnID); browserConn != nil {
		_ = browserConn.sendTerminalClosed(session.SessionID, reason)
	}
}

// generateConnID generates a unique connection ID.
func generateConnID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "conn-" + hex.EncodeToString(b)
}

// ===== ArthasTunnel Interface Implementation =====

// HandleAgentWebSocket handles WebSocket connections from agents.
func (e *Extension) HandleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	// Upgrade to WebSocket
	conn, err := e.upgrader.Upgrade(w, r, nil)
	if err != nil {
		e.logger.Error("Failed to upgrade agent WebSocket", zap.Error(err))
		return
	}

	connID := generateConnID()
	agentConn := newAgentConnection(connID, conn, e.logger)

	// Get app ID from request header (set by gateway middleware)
	if appID := r.Header.Get("X-App-ID"); appID != "" {
		agentConn.setAppID(appID)
	}

	e.agentConns.add(agentConn)

	// Prefer agent ID propagated by gateway middleware; fall back to query params.
	// This allows mapping agent connections immediately, even if the agent doesn't
	// send a REGISTER message.
	agentID := r.Header.Get("X-Agent-ID")
	if agentID == "" {
		agentID = r.URL.Query().Get("agent_id")
		if agentID == "" {
			agentID = r.URL.Query().Get("agentId")
		}
	}
	if agentID != "" {
		agentConn.agentID = agentID
		e.agentConns.register(agentConn)
	}

	e.logger.Info("Agent WebSocket connected",
		zap.String("conn_id", connID),
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("app_id", agentConn.appID),
		zap.String("agent_id", agentConn.agentID),
	)

	// Start read and write goroutines
	e.wg.Add(2)
	go e.agentReadLoop(agentConn)
	go e.agentWriteLoop(agentConn)
}

// agentReadLoop reads messages from agent.
func (e *Extension) agentReadLoop(conn *agentConnection) {
	defer e.wg.Done()
	defer func() {
		e.handleAgentDisconnect(conn)
	}()

	for {
		messageType, data, err := conn.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				e.logger.Warn("Agent WebSocket read error",
					zap.String("conn_id", conn.connID),
					zap.Error(err),
				)
			}
			return
		}

		switch messageType {
		case websocket.TextMessage:
			e.handleAgentTextMessage(conn, data)
		case websocket.BinaryMessage:
			e.handleAgentBinaryMessage(conn, data)
		}
	}
}

// agentWriteLoop writes messages to agent.
func (e *Extension) agentWriteLoop(conn *agentConnection) {
	defer e.wg.Done()

	pingTicker := time.NewTicker(e.config.PingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-conn.closeChan:
			return

		case data := <-conn.sendChan:
			conn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				e.logger.Warn("Agent WebSocket write error",
					zap.String("conn_id", conn.connID),
					zap.Error(err),
				)
				return
			}

		case <-pingTicker.C:
			conn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.send(NewPingMessage()); err != nil {
				return
			}
		}
	}
}

// handleAgentTextMessage handles text messages from agent.
func (e *Extension) handleAgentTextMessage(conn *agentConnection, data []byte) {
	e.logger.Debug("Received agent text message",
		zap.String("conn_id", conn.connID),
		zap.String("data", string(data)),
	)

	// Parse base message to get type
	var baseMsg TunnelMessage
	if err := json.Unmarshal(data, &baseMsg); err != nil {
		e.logger.Warn("Failed to parse agent message",
			zap.Error(err),
			zap.String("raw_data", string(data)),
		)
		return
	}

	e.logger.Debug("Parsed agent message",
		zap.String("type", string(baseMsg.Type)),
		zap.String("conn_id", conn.connID),
	)

	switch baseMsg.Type {
	case MessageTypeRegister:
		var msg RegisterMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		e.handleAgentRegister(conn, &msg)

	case MessageTypePing:
		// Agent sent PING, respond with PONG
		conn.lastPingAt = time.Now()
		pongMsg := NewPongMessage()
		if err := conn.send(pongMsg); err != nil {
			e.logger.Debug("Failed to send PONG to agent", zap.Error(err))
		}

	case MessageTypePong:
		conn.lastPingAt = time.Now()

	case MessageTypeArthasStatus:
		var msg ArthasStatusMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		conn.updateArthasStatus(&msg.Payload)

	case MessageTypeTerminalReady:
		var msg TerminalReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		e.handleTerminalReady(conn, &msg)

	case MessageTypeTerminalRejected:
		var msg TerminalRejectedMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		e.handleTerminalRejected(&msg)

	case MessageTypeTerminalClosed:
		var msg TerminalClosedMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		e.handleTerminalClosedFromAgent(&msg)

	default:
		e.logger.Debug("Unknown agent message type", zap.String("type", string(baseMsg.Type)))
	}
}

// handleAgentBinaryMessage handles binary messages from agent (terminal output).
func (e *Extension) handleAgentBinaryMessage(conn *agentConnection, data []byte) {
	// Binary frame format: [sessionId length (1 byte)] [sessionId (N bytes)] [data]
	if len(data) < 2 {
		return
	}

	sessionIDLen := int(data[0])
	if len(data) < 1+sessionIDLen {
		return
	}

	sessionID := string(data[1 : 1+sessionIDLen])
	outputData := data[1+sessionIDLen:]

	// Find session and forward to browser
	session := e.sessions.getSession(sessionID)
	if session == nil {
		return
	}

	e.sessions.markSessionActive(sessionID)

	// Forward to browser
	browserConn := e.browserConns.get(session.browserConnID)
	if browserConn == nil {
		return
	}

	// Encode as base64 for JSON transport
	encoded := base64.StdEncoding.EncodeToString(outputData)
	_ = browserConn.sendTerminalOutput(sessionID, encoded)
}

// handleAgentRegister handles agent registration.
func (e *Extension) handleAgentRegister(conn *agentConnection, msg *RegisterMessage) {
	conn.setRegistration(&msg.Payload)
	e.agentConns.register(conn)

	e.logger.Info("Agent registered",
		zap.String("agent_id", conn.agentID),
		zap.String("service_name", conn.serviceName),
		zap.String("hostname", conn.hostname),
	)

	// Send ack
	_ = conn.send(NewRegisterAckMessage(true, "registered"))
}

// handleTerminalReady handles terminal ready response from agent.
func (e *Extension) handleTerminalReady(conn *agentConnection, msg *TerminalReadyMessage) {
	err := e.sessions.completePendingRequest(
		msg.Payload.RequestID,
		msg.Payload.SessionID,
		conn.connID,
	)
	if err != nil {
		e.logger.Warn("Failed to complete pending request",
			zap.String("request_id", msg.Payload.RequestID),
			zap.Error(err),
		)
		return
	}

	// Notify browser
	session := e.sessions.getSession(msg.Payload.SessionID)
	if session == nil {
		return
	}

	browserConn := e.browserConns.get(session.browserConnID)
	if browserConn != nil {
		_ = browserConn.sendTerminalReady(msg.Payload.SessionID, conn.agentID)
	}
}

// handleTerminalRejected handles terminal rejected response from agent.
func (e *Extension) handleTerminalRejected(msg *TerminalRejectedMessage) {
	_ = e.sessions.rejectPendingRequest(
		msg.Payload.RequestID,
		msg.Payload.Reason,
		msg.Payload.Message,
	)
}

// handleTerminalClosedFromAgent handles terminal closed notification from agent.
func (e *Extension) handleTerminalClosedFromAgent(msg *TerminalClosedMessage) {
	session := e.sessions.closeSession(msg.Payload.SessionID)
	if session == nil {
		return
	}

	// Notify browser
	browserConn := e.browserConns.get(session.browserConnID)
	if browserConn != nil {
		_ = browserConn.sendTerminalClosed(msg.Payload.SessionID, msg.Payload.Reason)
	}
}

// handleAgentDisconnect handles agent disconnection.
func (e *Extension) handleAgentDisconnect(conn *agentConnection) {
	conn.close()
	e.agentConns.remove(conn.connID)

	// Close all sessions for this agent
	closedSessions := e.sessions.closeAgentSessions(conn.agentID)

	// Notify browsers
	for _, session := range closedSessions {
		browserConn := e.browserConns.get(session.browserConnID)
		if browserConn != nil {
			_ = browserConn.sendTerminalClosed(session.SessionID, "agent_disconnected")
		}
	}

	e.logger.Info("Agent disconnected",
		zap.String("agent_id", conn.agentID),
		zap.Int("closed_sessions", len(closedSessions)),
	)
}

// HandleBrowserWebSocket handles WebSocket connections from browsers.
func (e *Extension) HandleBrowserWebSocket(w http.ResponseWriter, r *http.Request) {
	e.logger.Info("HandleBrowserWebSocket called",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("agent_id", r.URL.Query().Get("agent_id")),
	)

	// Upgrade to WebSocket
	conn, err := e.upgrader.Upgrade(w, r, nil)
	if err != nil {
		e.logger.Error("Failed to upgrade browser WebSocket",
			zap.Error(err),
			zap.String("remote_addr", r.RemoteAddr),
		)
		return
	}

	connID := generateConnID()
	userID := r.URL.Query().Get("user_id")
	agentID := r.URL.Query().Get("agent_id")
	browserConn := newBrowserConnection(connID, userID, conn, e.logger)

	e.browserConns.add(browserConn)

	e.logger.Info("Browser WebSocket connected",
		zap.String("conn_id", connID),
		zap.String("user_id", userID),
		zap.String("agent_id", agentID),
	)

	// Start read and write goroutines
	e.wg.Add(2)
	go e.browserReadLoop(browserConn)
	go e.browserWriteLoop(browserConn)
}

// browserReadLoop reads messages from browser.
func (e *Extension) browserReadLoop(conn *browserConnection) {
	defer e.wg.Done()
	defer func() {
		e.handleBrowserDisconnect(conn)
	}()

	for {
		_, data, err := conn.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				e.logger.Warn("Browser WebSocket read error",
					zap.String("conn_id", conn.connID),
					zap.Error(err),
				)
			}
			return
		}

		e.handleBrowserMessage(conn, data)
	}
}

// browserWriteLoop writes messages to browser.
func (e *Extension) browserWriteLoop(conn *browserConnection) {
	defer e.wg.Done()

	for {
		select {
		case <-conn.closeChan:
			return

		case data := <-conn.sendChan:
			conn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				e.logger.Warn("Browser WebSocket write error",
					zap.String("conn_id", conn.connID),
					zap.Error(err),
				)
				return
			}
		}
	}
}

// handleBrowserMessage handles messages from browser.
func (e *Extension) handleBrowserMessage(conn *browserConnection, data []byte) {
	e.logger.Debug("Received browser message",
		zap.String("conn_id", conn.connID),
		zap.String("data", string(data)),
	)

	var msg BrowserMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		e.logger.Warn("Failed to parse browser message",
			zap.Error(err),
			zap.String("raw_data", string(data)),
		)
		return
	}

	e.logger.Info("Parsed browser message",
		zap.String("type", string(msg.Type)),
		zap.String("conn_id", conn.connID),
	)

	switch msg.Type {
	case BrowserMessageTypeOpenTerminal:
		var payload BrowserOpenTerminalPayload
		if err := remarshal(msg.Payload, &payload); err != nil {
			return
		}
		e.handleBrowserOpenTerminal(conn, &payload)

	case BrowserMessageTypeInput:
		var payload BrowserInputPayload
		if err := remarshal(msg.Payload, &payload); err != nil {
			return
		}
		e.handleBrowserInput(conn, &payload)

	case BrowserMessageTypeResize:
		var payload BrowserResizePayload
		if err := remarshal(msg.Payload, &payload); err != nil {
			return
		}
		e.handleBrowserResize(conn, &payload)

	case BrowserMessageTypeCloseTerminal:
		var payload BrowserCloseTerminalPayload
		if err := remarshal(msg.Payload, &payload); err != nil {
			return
		}
		e.handleBrowserCloseTerminal(conn, &payload)
	}
}

// remarshal re-marshals payload to target type.
func remarshal(src any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// handleBrowserOpenTerminal handles terminal open request from browser.
func (e *Extension) handleBrowserOpenTerminal(conn *browserConnection, payload *BrowserOpenTerminalPayload) {
	// Check if agent is connected
	agentConn := e.agentConns.getByAgentID(payload.AgentID)
	if agentConn == nil {
		_ = conn.sendError("AGENT_NOT_CONNECTED", "Agent is not connected")
		return
	}

	// Check session limit
	if e.sessions.getAgentSessionCount(payload.AgentID) >= e.config.MaxSessionsPerAgent {
		_ = conn.sendError("MAX_SESSIONS_REACHED", "Maximum sessions per agent reached")
		return
	}

	// Create pending request
	req := e.sessions.createPendingRequest(
		payload.AgentID,
		conn.connID,
		conn.userID,
		payload.Cols,
		payload.Rows,
	)

	// Send terminal open to agent
	err := agentConn.send(NewTerminalOpenMessage(req.RequestID, conn.userID, payload.Cols, payload.Rows))
	if err != nil {
		_ = conn.sendError("SEND_FAILED", "Failed to send request to agent")
		return
	}

	// Wait for response (with timeout)
	go func() {
		select {
		case result := <-req.ResultChan:
			if result.Error != nil {
				_ = conn.sendError("TERMINAL_REJECTED", result.Error.Error())
			}
			// Success case is handled in handleTerminalReady

		case <-time.After(30 * time.Second):
			_ = e.sessions.rejectPendingRequest(req.RequestID, "TIMEOUT", "Request timeout")
			_ = conn.sendError("TIMEOUT", "Request timeout")
		}
	}()
}

// handleBrowserInput handles terminal input from browser.
func (e *Extension) handleBrowserInput(conn *browserConnection, payload *BrowserInputPayload) {
	session := e.sessions.getSession(payload.SessionID)
	if session == nil {
		return
	}

	// Verify browser owns this session
	if session.browserConnID != conn.connID {
		return
	}

	e.sessions.markSessionActive(payload.SessionID)

	// Forward to agent
	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn == nil {
		return
	}

	_ = agentConn.send(NewTerminalInputMessage(payload.SessionID, payload.Data))
}

// handleBrowserResize handles terminal resize from browser.
func (e *Extension) handleBrowserResize(conn *browserConnection, payload *BrowserResizePayload) {
	session := e.sessions.getSession(payload.SessionID)
	if session == nil {
		return
	}

	if session.browserConnID != conn.connID {
		return
	}

	// Forward to agent
	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn == nil {
		return
	}

	_ = agentConn.send(NewTerminalResizeMessage(payload.SessionID, payload.Cols, payload.Rows))
}

// handleBrowserCloseTerminal handles terminal close from browser.
func (e *Extension) handleBrowserCloseTerminal(conn *browserConnection, payload *BrowserCloseTerminalPayload) {
	session := e.sessions.closeSession(payload.SessionID)
	if session == nil {
		return
	}

	// Notify agent
	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn != nil {
		_ = agentConn.send(NewTerminalCloseMessage(payload.SessionID))
	}
}

// handleBrowserDisconnect handles browser disconnection.
func (e *Extension) handleBrowserDisconnect(conn *browserConnection) {
	conn.close()
	e.browserConns.remove(conn.connID)

	// Close all sessions for this browser
	closedSessions := e.sessions.closeBrowserSessions(conn.connID)

	// Notify agents
	for _, session := range closedSessions {
		agentConn := e.agentConns.getByAgentID(session.AgentID)
		if agentConn != nil {
			_ = agentConn.send(NewTerminalCloseMessage(session.SessionID))
		}
	}

	e.logger.Info("Browser disconnected",
		zap.String("conn_id", conn.connID),
		zap.Int("closed_sessions", len(closedSessions)),
	)
}

// ===== Public API Methods =====

// GetAgentArthasStatus returns the Arthas status for an agent.
func (e *Extension) GetAgentArthasStatus(agentID string) (*ArthasStatus, error) {
	conn := e.agentConns.getByAgentID(agentID)
	if conn == nil {
		return nil, errors.New("agent not connected")
	}
	return conn.getArthasStatus(), nil
}

// ListConnectedAgents returns all agents with active tunnel connections.
func (e *Extension) ListConnectedAgents() []*ConnectedAgent {
	return e.agentConns.listConnectedAgents()
}

// IsAgentConnected checks if an agent has an active tunnel connection.
func (e *Extension) IsAgentConnected(agentID string) bool {
	return e.agentConns.isAgentConnected(agentID)
}

// OpenTerminal opens a terminal session to an agent.
func (e *Extension) OpenTerminal(agentID, userID string, cols, rows int) (string, error) {
	agentConn := e.agentConns.getByAgentID(agentID)
	if agentConn == nil {
		return "", errors.New("agent not connected")
	}

	if e.sessions.getAgentSessionCount(agentID) >= e.config.MaxSessionsPerAgent {
		return "", errors.New("maximum sessions per agent reached")
	}

	req := e.sessions.createPendingRequest(agentID, "", userID, cols, rows)

	err := agentConn.send(NewTerminalOpenMessage(req.RequestID, userID, cols, rows))
	if err != nil {
		return "", err
	}

	select {
	case result := <-req.ResultChan:
		if result.Error != nil {
			return "", result.Error
		}
		return result.SessionID, nil
	case <-time.After(30 * time.Second):
		_ = e.sessions.rejectPendingRequest(req.RequestID, "TIMEOUT", "Request timeout")
		return "", errors.New("request timeout")
	}
}

// CloseTerminal closes a terminal session.
func (e *Extension) CloseTerminal(sessionID string) error {
	session := e.sessions.closeSession(sessionID)
	if session == nil {
		return errors.New("session not found")
	}

	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn != nil {
		_ = agentConn.send(NewTerminalCloseMessage(sessionID))
	}

	return nil
}

// SendTerminalInput sends input to a terminal session.
func (e *Extension) SendTerminalInput(sessionID, data string) error {
	session := e.sessions.getSession(sessionID)
	if session == nil {
		return errors.New("session not found")
	}

	e.sessions.markSessionActive(sessionID)

	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn == nil {
		return errors.New("agent not connected")
	}

	return agentConn.send(NewTerminalInputMessage(sessionID, data))
}

// ResizeTerminal resizes a terminal session.
func (e *Extension) ResizeTerminal(sessionID string, cols, rows int) error {
	session := e.sessions.getSession(sessionID)
	if session == nil {
		return errors.New("session not found")
	}

	agentConn := e.agentConns.getByAgentID(session.AgentID)
	if agentConn == nil {
		return errors.New("agent not connected")
	}

	return agentConn.send(NewTerminalResizeMessage(sessionID, cols, rows))
}
