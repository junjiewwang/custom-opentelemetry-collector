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

// agentConnection represents a WebSocket connection from an agent.
type agentConnection struct {
	connID       string
	agentID      string
	appID        string
	conn         *websocket.Conn
	logger       *zap.Logger
	
	// Agent info from registration
	serviceName  string
	hostname     string
	ip           string
	version      string
	
	// Connection state
	connectedAt  time.Time
	lastPingAt   time.Time
	
	// Arthas status
	arthasStatus *ArthasStatus
	statusMu     sync.RWMutex
	
	// Send channel
	sendChan     chan []byte
	
	// Close handling
	closeChan    chan struct{}
	closeOnce    sync.Once
}

// newAgentConnection creates a new agent connection.
func newAgentConnection(connID string, conn *websocket.Conn, logger *zap.Logger) *agentConnection {
	return &agentConnection{
		connID:      connID,
		conn:        conn,
		logger:      logger,
		connectedAt: time.Now(),
		lastPingAt:  time.Now(),
		sendChan:    make(chan []byte, 256),
		closeChan:   make(chan struct{}),
	}
}

// setRegistration sets agent registration info.
func (c *agentConnection) setRegistration(payload *RegisterPayload) {
	c.agentID = payload.AgentID
	c.serviceName = payload.ServiceName
	c.hostname = payload.Hostname
	c.ip = payload.IP
	c.version = payload.Version
}

// setAppID sets the app ID for this connection.
func (c *agentConnection) setAppID(appID string) {
	c.appID = appID
}

// updateArthasStatus updates the Arthas status.
func (c *agentConnection) updateArthasStatus(payload *ArthasStatusPayload) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	
	c.arthasStatus = &ArthasStatus{
		State:          payload.State,
		ArthasVersion:  payload.ArthasVersion,
		ActiveSessions: payload.ActiveSessions,
		MaxSessions:    payload.MaxSessions,
		UptimeMs:       payload.UptimeMs,
	}
}

// getArthasStatus returns the current Arthas status.
func (c *agentConnection) getArthasStatus() *ArthasStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	
	if c.arthasStatus == nil {
		return nil
	}
	
	// Return a copy
	return &ArthasStatus{
		State:          c.arthasStatus.State,
		ArthasVersion:  c.arthasStatus.ArthasVersion,
		ActiveSessions: c.arthasStatus.ActiveSessions,
		MaxSessions:    c.arthasStatus.MaxSessions,
		UptimeMs:       c.arthasStatus.UptimeMs,
	}
}

// toConnectedAgent converts to ConnectedAgent.
func (c *agentConnection) toConnectedAgent() *ConnectedAgent {
	return &ConnectedAgent{
		AgentID:      c.agentID,
		AppID:        c.appID,
		ServiceName:  c.serviceName,
		Hostname:     c.hostname,
		IP:           c.ip,
		Version:      c.version,
		ConnectedAt:  c.connectedAt,
		LastPingAt:   c.lastPingAt,
		ArthasStatus: c.getArthasStatus(),
	}
}

// send sends a message to the agent.
func (c *agentConnection) send(msg any) error {
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
		c.logger.Warn("Agent send channel full, dropping message",
			zap.String("agent_id", c.agentID),
		)
		return nil
	}
}

// sendBinary sends binary data to the agent.
func (c *agentConnection) sendBinary(data []byte) error {
	select {
	case c.sendChan <- data:
		return nil
	case <-c.closeChan:
		return websocket.ErrCloseSent
	default:
		return nil
	}
}

// close closes the connection.
func (c *agentConnection) close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.conn.Close()
	})
}

// isClosed returns whether the connection is closed.
func (c *agentConnection) isClosed() bool {
	select {
	case <-c.closeChan:
		return true
	default:
		return false
	}
}

// agentConnectionManager manages all agent connections.
type agentConnectionManager struct {
	logger *zap.Logger
	mu     sync.RWMutex
	
	// connections maps connID to connection
	connections map[string]*agentConnection
	
	// agentConnections maps agentID to connID
	agentConnections map[string]string
}

// newAgentConnectionManager creates a new agent connection manager.
func newAgentConnectionManager(logger *zap.Logger) *agentConnectionManager {
	return &agentConnectionManager{
		logger:           logger,
		connections:      make(map[string]*agentConnection),
		agentConnections: make(map[string]string),
	}
}

// add adds a connection.
func (m *agentConnectionManager) add(conn *agentConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.connections[conn.connID] = conn
}

// register registers an agent with its connection.
func (m *agentConnectionManager) register(conn *agentConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Close existing connection for this agent if any
	if existingConnID, ok := m.agentConnections[conn.agentID]; ok {
		if existingConn, ok := m.connections[existingConnID]; ok {
			m.logger.Info("Closing existing connection for agent",
				zap.String("agent_id", conn.agentID),
				zap.String("old_conn_id", existingConnID),
			)
			existingConn.close()
			delete(m.connections, existingConnID)
		}
	}
	
	m.agentConnections[conn.agentID] = conn.connID
}

// remove removes a connection.
func (m *agentConnectionManager) remove(connID string) *agentConnection {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	conn, ok := m.connections[connID]
	if !ok {
		return nil
	}
	
	delete(m.connections, connID)
	
	// Remove from agent connections if this is the current connection
	if conn.agentID != "" {
		if currentConnID, ok := m.agentConnections[conn.agentID]; ok && currentConnID == connID {
			delete(m.agentConnections, conn.agentID)
		}
	}
	
	return conn
}

// getByConnID returns a connection by connection ID.
func (m *agentConnectionManager) getByConnID(connID string) *agentConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connections[connID]
}

// getByAgentID returns a connection by agent ID.
func (m *agentConnectionManager) getByAgentID(agentID string) *agentConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	connID, ok := m.agentConnections[agentID]
	if !ok {
		return nil
	}
	return m.connections[connID]
}

// isAgentConnected checks if an agent is connected.
func (m *agentConnectionManager) isAgentConnected(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	connID, ok := m.agentConnections[agentID]
	if !ok {
		return false
	}
	
	conn, ok := m.connections[connID]
	return ok && !conn.isClosed()
}

// listConnectedAgents returns all connected agents.
func (m *agentConnectionManager) listConnectedAgents() []*ConnectedAgent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	agents := make([]*ConnectedAgent, 0, len(m.agentConnections))
	for agentID, connID := range m.agentConnections {
		conn, ok := m.connections[connID]
		if !ok || conn.isClosed() {
			continue
		}
		if conn.agentID != agentID {
			continue
		}
		agents = append(agents, conn.toConnectedAgent())
	}
	return agents
}

// count returns the number of connections.
func (m *agentConnectionManager) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.agentConnections)
}
