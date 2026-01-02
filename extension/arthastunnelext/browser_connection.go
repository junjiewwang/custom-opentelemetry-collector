// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// browserConnection represents a WebSocket connection from a browser.
type browserConnection struct {
	connID      string
	userID      string
	conn        *websocket.Conn
	logger      *zap.Logger
	connectedAt time.Time
	
	// Send channel
	sendChan    chan []byte
	
	// Close handling
	closeChan   chan struct{}
	closeOnce   sync.Once
}

// newBrowserConnection creates a new browser connection.
func newBrowserConnection(connID, userID string, conn *websocket.Conn, logger *zap.Logger) *browserConnection {
	return &browserConnection{
		connID:      connID,
		userID:      userID,
		conn:        conn,
		logger:      logger,
		connectedAt: time.Now(),
		sendChan:    make(chan []byte, 256),
		closeChan:   make(chan struct{}),
	}
}

// send sends a message to the browser.
func (c *browserConnection) send(msg *BrowserMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	
	select {
	case c.sendChan <- data:
		return nil
	case <-c.closeChan:
		return websocket.ErrCloseSent
	default:
		c.logger.Warn("Browser send channel full, dropping message",
			zap.String("conn_id", c.connID),
		)
		return nil
	}
}

// sendTerminalReady sends terminal ready message.
func (c *browserConnection) sendTerminalReady(sessionID, agentID string) error {
	return c.send(&BrowserMessage{
		Type: BrowserMessageTypeTerminalReady,
		Payload: BrowserTerminalReadyPayload{
			SessionID: sessionID,
			AgentID:   agentID,
		},
	})
}

// sendTerminalOutput sends terminal output message.
func (c *browserConnection) sendTerminalOutput(sessionID, data string) error {
	return c.send(&BrowserMessage{
		Type: BrowserMessageTypeTerminalOutput,
		Payload: BrowserTerminalOutputPayload{
			SessionID: sessionID,
			Data:      data,
		},
	})
}

// sendTerminalClosed sends terminal closed message.
func (c *browserConnection) sendTerminalClosed(sessionID, reason string) error {
	return c.send(&BrowserMessage{
		Type: BrowserMessageTypeTerminalClosed,
		Payload: BrowserTerminalClosedPayload{
			SessionID: sessionID,
			Reason:    reason,
		},
	})
}

// sendError sends error message.
func (c *browserConnection) sendError(code, message string) error {
	return c.send(&BrowserMessage{
		Type: BrowserMessageTypeError,
		Payload: BrowserErrorPayload{
			Code:    code,
			Message: message,
		},
	})
}

// close closes the connection.
func (c *browserConnection) close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.conn.Close()
	})
}

// isClosed returns whether the connection is closed.
func (c *browserConnection) isClosed() bool {
	select {
	case <-c.closeChan:
		return true
	default:
		return false
	}
}

// browserConnectionManager manages all browser connections.
type browserConnectionManager struct {
	logger *zap.Logger
	mu     sync.RWMutex
	
	// connections maps connID to connection
	connections map[string]*browserConnection
}

// newBrowserConnectionManager creates a new browser connection manager.
func newBrowserConnectionManager(logger *zap.Logger) *browserConnectionManager {
	return &browserConnectionManager{
		logger:      logger,
		connections: make(map[string]*browserConnection),
	}
}

// add adds a connection.
func (m *browserConnectionManager) add(conn *browserConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[conn.connID] = conn
}

// remove removes a connection.
func (m *browserConnectionManager) remove(connID string) *browserConnection {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	conn, ok := m.connections[connID]
	if !ok {
		return nil
	}
	
	delete(m.connections, connID)
	return conn
}

// get returns a connection by ID.
func (m *browserConnectionManager) get(connID string) *browserConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connections[connID]
}

// count returns the number of connections.
func (m *browserConnectionManager) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}
