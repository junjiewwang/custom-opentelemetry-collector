// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

// sessionManager manages terminal sessions between browsers and agents.
type sessionManager struct {
	config  *Config
	logger  *zap.Logger
	mu      sync.RWMutex
	
	// sessions maps sessionID to session
	sessions map[string]*terminalSessionInfo
	
	// agentSessions maps agentID to set of sessionIDs
	agentSessions map[string]map[string]struct{}
	
	// browserSessions maps browserConnID to set of sessionIDs
	browserSessions map[string]map[string]struct{}
	
	// pendingRequests maps requestID to pending terminal open request
	pendingRequests map[string]*pendingTerminalRequest
}

// terminalSessionInfo holds internal session information.
type terminalSessionInfo struct {
	*TerminalSession
	browserConnID string
	agentConnID   string
}

// pendingTerminalRequest represents a pending terminal open request.
type pendingTerminalRequest struct {
	RequestID     string
	AgentID       string
	BrowserConnID string
	UserID        string
	Cols          int
	Rows          int
	CreatedAt     time.Time
	ResultChan    chan *terminalOpenResult
}

// terminalOpenResult is the result of a terminal open request.
type terminalOpenResult struct {
	SessionID string
	Error     error
}

// newSessionManager creates a new session manager.
func newSessionManager(config *Config, logger *zap.Logger) *sessionManager {
	return &sessionManager{
		config:          config,
		logger:          logger,
		sessions:        make(map[string]*terminalSessionInfo),
		agentSessions:   make(map[string]map[string]struct{}),
		browserSessions: make(map[string]map[string]struct{}),
		pendingRequests: make(map[string]*pendingTerminalRequest),
	}
}

// generateSessionID generates a unique session ID.
func generateSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "sess-" + hex.EncodeToString(b)
}

// generateRequestID generates a unique request ID.
func generateRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req-" + hex.EncodeToString(b)
}

// createPendingRequest creates a pending terminal open request.
func (m *sessionManager) createPendingRequest(agentID, browserConnID, userID string, cols, rows int) *pendingTerminalRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	req := &pendingTerminalRequest{
		RequestID:     generateRequestID(),
		AgentID:       agentID,
		BrowserConnID: browserConnID,
		UserID:        userID,
		Cols:          cols,
		Rows:          rows,
		CreatedAt:     time.Now(),
		ResultChan:    make(chan *terminalOpenResult, 1),
	}
	
	m.pendingRequests[req.RequestID] = req
	return req
}

// completePendingRequest completes a pending request with success.
func (m *sessionManager) completePendingRequest(requestID, sessionID, agentConnID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	req, ok := m.pendingRequests[requestID]
	if !ok {
		return errors.New("pending request not found")
	}
	
	delete(m.pendingRequests, requestID)
	
	// Create the session
	now := time.Now()
	session := &terminalSessionInfo{
		TerminalSession: &TerminalSession{
			SessionID:    sessionID,
			AgentID:      req.AgentID,
			UserID:       req.UserID,
			Cols:         req.Cols,
			Rows:         req.Rows,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		browserConnID: req.BrowserConnID,
		agentConnID:   agentConnID,
	}
	
	m.sessions[sessionID] = session
	
	// Update agent sessions index
	if m.agentSessions[req.AgentID] == nil {
		m.agentSessions[req.AgentID] = make(map[string]struct{})
	}
	m.agentSessions[req.AgentID][sessionID] = struct{}{}
	
	// Update browser sessions index
	if m.browserSessions[req.BrowserConnID] == nil {
		m.browserSessions[req.BrowserConnID] = make(map[string]struct{})
	}
	m.browserSessions[req.BrowserConnID][sessionID] = struct{}{}
	
	// Send result
	req.ResultChan <- &terminalOpenResult{SessionID: sessionID}
	
	m.logger.Info("Terminal session created",
		zap.String("session_id", sessionID),
		zap.String("agent_id", req.AgentID),
		zap.String("user_id", req.UserID),
	)
	
	return nil
}

// rejectPendingRequest rejects a pending request with error.
func (m *sessionManager) rejectPendingRequest(requestID string, reason, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	req, ok := m.pendingRequests[requestID]
	if !ok {
		return errors.New("pending request not found")
	}
	
	delete(m.pendingRequests, requestID)
	
	// Send error result
	req.ResultChan <- &terminalOpenResult{
		Error: errors.New(reason + ": " + message),
	}
	
	m.logger.Warn("Terminal session rejected",
		zap.String("request_id", requestID),
		zap.String("reason", reason),
		zap.String("message", message),
	)
	
	return nil
}

// getSession returns a session by ID.
func (m *sessionManager) getSession(sessionID string) *terminalSessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// markSessionActive updates the last active time of a session.
func (m *sessionManager) markSessionActive(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if session, ok := m.sessions[sessionID]; ok {
		session.LastActiveAt = time.Now()
	}
}

// closeSession closes a session and removes it from all indexes.
func (m *sessionManager) closeSession(sessionID string) *terminalSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	session, ok := m.sessions[sessionID]
	if !ok {
		return nil
	}
	
	delete(m.sessions, sessionID)
	
	// Remove from agent sessions
	if agentSessions, ok := m.agentSessions[session.AgentID]; ok {
		delete(agentSessions, sessionID)
		if len(agentSessions) == 0 {
			delete(m.agentSessions, session.AgentID)
		}
	}
	
	// Remove from browser sessions
	if browserSessions, ok := m.browserSessions[session.browserConnID]; ok {
		delete(browserSessions, sessionID)
		if len(browserSessions) == 0 {
			delete(m.browserSessions, session.browserConnID)
		}
	}
	
	m.logger.Info("Terminal session closed",
		zap.String("session_id", sessionID),
		zap.String("agent_id", session.AgentID),
	)
	
	return session
}

// closeAgentSessions closes all sessions for an agent.
func (m *sessionManager) closeAgentSessions(agentID string) []*terminalSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	sessionIDs, ok := m.agentSessions[agentID]
	if !ok {
		return nil
	}
	
	var closed []*terminalSessionInfo
	for sessionID := range sessionIDs {
		if session, ok := m.sessions[sessionID]; ok {
			delete(m.sessions, sessionID)
			
			// Remove from browser sessions
			if browserSessions, ok := m.browserSessions[session.browserConnID]; ok {
				delete(browserSessions, sessionID)
				if len(browserSessions) == 0 {
					delete(m.browserSessions, session.browserConnID)
				}
			}
			
			closed = append(closed, session)
		}
	}
	
	delete(m.agentSessions, agentID)
	
	m.logger.Info("Closed all sessions for agent",
		zap.String("agent_id", agentID),
		zap.Int("count", len(closed)),
	)
	
	return closed
}

// closeBrowserSessions closes all sessions for a browser connection.
func (m *sessionManager) closeBrowserSessions(browserConnID string) []*terminalSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	sessionIDs, ok := m.browserSessions[browserConnID]
	if !ok {
		return nil
	}
	
	var closed []*terminalSessionInfo
	for sessionID := range sessionIDs {
		if session, ok := m.sessions[sessionID]; ok {
			delete(m.sessions, sessionID)
			
			// Remove from agent sessions
			if agentSessions, ok := m.agentSessions[session.AgentID]; ok {
				delete(agentSessions, sessionID)
				if len(agentSessions) == 0 {
					delete(m.agentSessions, session.AgentID)
				}
			}
			
			closed = append(closed, session)
		}
	}
	
	delete(m.browserSessions, browserConnID)
	
	m.logger.Info("Closed all sessions for browser",
		zap.String("browser_conn_id", browserConnID),
		zap.Int("count", len(closed)),
	)
	
	return closed
}

// getAgentSessionCount returns the number of sessions for an agent.
func (m *sessionManager) getAgentSessionCount(agentID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if sessions, ok := m.agentSessions[agentID]; ok {
		return len(sessions)
	}
	return 0
}

// cleanupExpiredSessions removes expired sessions.
func (m *sessionManager) cleanupExpiredSessions() []*terminalSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	now := time.Now()
	var expired []*terminalSessionInfo
	
	for _, session := range m.sessions {
		// Check idle timeout
		if now.Sub(session.LastActiveAt) > m.config.SessionIdleTimeout {
			expired = append(expired, session)
			continue
		}
		
		// Check max duration
		if now.Sub(session.CreatedAt) > m.config.SessionMaxDuration {
			expired = append(expired, session)
			continue
		}
	}
	
	// Remove expired sessions
	for _, session := range expired {
		delete(m.sessions, session.SessionID)
		
		if agentSessions, ok := m.agentSessions[session.AgentID]; ok {
			delete(agentSessions, session.SessionID)
			if len(agentSessions) == 0 {
				delete(m.agentSessions, session.AgentID)
			}
		}
		
		if browserSessions, ok := m.browserSessions[session.browserConnID]; ok {
			delete(browserSessions, session.SessionID)
			if len(browserSessions) == 0 {
				delete(m.browserSessions, session.browserConnID)
			}
		}
	}
	
	// Also cleanup expired pending requests
	for reqID, req := range m.pendingRequests {
		if now.Sub(req.CreatedAt) > 30*time.Second {
			delete(m.pendingRequests, reqID)
			req.ResultChan <- &terminalOpenResult{
				Error: errors.New("request timeout"),
			}
		}
	}
	
	if len(expired) > 0 {
		m.logger.Info("Cleaned up expired sessions", zap.Int("count", len(expired)))
	}
	
	return expired
}

// getAllSessions returns all active sessions.
func (m *sessionManager) getAllSessions() []*TerminalSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	sessions := make([]*TerminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session.TerminalSession)
	}
	return sessions
}
