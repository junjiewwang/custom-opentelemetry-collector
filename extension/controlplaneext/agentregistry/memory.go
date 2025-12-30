// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// MemoryAgentRegistry implements AgentRegistry using in-memory storage.
type MemoryAgentRegistry struct {
	logger *zap.Logger
	config Config

	mu           sync.RWMutex
	agents       map[string]*AgentInfo
	labelIndex   map[string]map[string]bool // labelKey:labelValue -> set of agentIDs
	currentTasks map[string]string          // agentID -> taskID

	// Lifecycle
	started  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMemoryAgentRegistry creates a new in-memory agent registry.
func NewMemoryAgentRegistry(logger *zap.Logger, config Config) *MemoryAgentRegistry {
	return &MemoryAgentRegistry{
		logger:       logger,
		config:       config,
		agents:       make(map[string]*AgentInfo),
		labelIndex:   make(map[string]map[string]bool),
		currentTasks: make(map[string]string),
		stopChan:     make(chan struct{}),
	}
}

// Ensure MemoryAgentRegistry implements AgentRegistry.
var _ AgentRegistry = (*MemoryAgentRegistry)(nil)

// Register registers a new agent.
func (m *MemoryAgentRegistry) Register(ctx context.Context, agent *AgentInfo) error {
	if agent == nil {
		return errors.New("agent cannot be nil")
	}
	if agent.AgentID == "" {
		return errors.New("agent_id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixNano()
	agent.RegisteredAt = now
	agent.LastHeartbeat = now

	if agent.Status == nil {
		agent.Status = &AgentStatus{
			State: AgentStateOnline,
		}
	} else {
		agent.Status.State = AgentStateOnline
	}

	// Store agent
	m.agents[agent.AgentID] = agent

	// Update label index
	m.updateLabelIndex(agent.AgentID, nil, agent.Labels)

	m.logger.Info("Agent registered",
		zap.String("agent_id", agent.AgentID),
		zap.String("hostname", agent.Hostname),
	)

	return nil
}

// Heartbeat updates the agent's heartbeat and status.
func (m *MemoryAgentRegistry) Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return errors.New("agent not found: " + agentID)
	}

	agent.LastHeartbeat = time.Now().UnixNano()

	if status != nil {
		status.State = AgentStateOnline
		agent.Status = status
	} else if agent.Status != nil {
		agent.Status.State = AgentStateOnline
	}

	return nil
}

// RegisterOrHeartbeat registers a new agent if not exists, or updates heartbeat if exists.
// This provides upsert semantics for automatic registration via status reports.
func (m *MemoryAgentRegistry) RegisterOrHeartbeat(ctx context.Context, agent *AgentInfo) error {
	if agent == nil {
		return errors.New("agent cannot be nil")
	}
	if agent.AgentID == "" {
		return errors.New("agent_id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixNano()

	existing, ok := m.agents[agent.AgentID]
	if ok {
		// Agent exists, update heartbeat and status
		existing.LastHeartbeat = now

		// Update mutable fields from the incoming agent info
		if agent.Hostname != "" {
			existing.Hostname = agent.Hostname
		}
		if agent.IP != "" {
			existing.IP = agent.IP
		}
		if agent.Version != "" {
			existing.Version = agent.Version
		}
		if agent.Token != "" {
			existing.Token = agent.Token
		}
		if agent.AppID != "" {
			existing.AppID = agent.AppID
		}

		// Update status
		if agent.Status != nil {
			agent.Status.State = AgentStateOnline
			existing.Status = agent.Status
		} else if existing.Status != nil {
			existing.Status.State = AgentStateOnline
		}

		// Update labels if provided
		if len(agent.Labels) > 0 {
			oldLabels := existing.Labels
			existing.Labels = agent.Labels
			m.updateLabelIndex(agent.AgentID, oldLabels, agent.Labels)
		}

		m.logger.Debug("Agent heartbeat updated",
			zap.String("agent_id", agent.AgentID),
		)
		return nil
	}

	// Agent doesn't exist, register it
	agent.RegisteredAt = now
	agent.LastHeartbeat = now

	if agent.Status == nil {
		agent.Status = &AgentStatus{
			State: AgentStateOnline,
		}
	} else {
		agent.Status.State = AgentStateOnline
	}

	// Store agent
	m.agents[agent.AgentID] = agent

	// Update label index
	m.updateLabelIndex(agent.AgentID, nil, agent.Labels)

	m.logger.Info("Agent auto-registered via heartbeat",
		zap.String("agent_id", agent.AgentID),
		zap.String("hostname", agent.Hostname),
	)

	return nil
}

// Unregister removes an agent from the registry.
func (m *MemoryAgentRegistry) Unregister(ctx context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return nil // Already unregistered
	}

	// Remove from label index
	m.updateLabelIndex(agentID, agent.Labels, nil)

	// Remove agent
	delete(m.agents, agentID)
	delete(m.currentTasks, agentID)

	m.logger.Info("Agent unregistered", zap.String("agent_id", agentID))

	return nil
}

// GetAgent retrieves information about a specific agent.
func (m *MemoryAgentRegistry) GetAgent(ctx context.Context, agentID string) (*AgentInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return nil, errors.New("agent not found: " + agentID)
	}

	return agent, nil
}

// GetOnlineAgents returns all online agents.
func (m *MemoryAgentRegistry) GetOnlineAgents(ctx context.Context) ([]*AgentInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var agents []*AgentInfo
	for _, agent := range m.agents {
		if agent.Status != nil && agent.Status.State == AgentStateOnline {
			agents = append(agents, agent)
		}
	}

	return agents, nil
}

// GetAgentsByLabel returns agents matching the specified label.
func (m *MemoryAgentRegistry) GetAgentsByLabel(ctx context.Context, labelKey, labelValue string) ([]*AgentInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	indexKey := labelKey + ":" + labelValue
	agentIDs, ok := m.labelIndex[indexKey]
	if !ok {
		return nil, nil
	}

	var agents []*AgentInfo
	for agentID := range agentIDs {
		if agent, ok := m.agents[agentID]; ok {
			agents = append(agents, agent)
		}
	}

	return agents, nil
}

// GetAgentsByToken returns all agents under a specific token/app.
func (m *MemoryAgentRegistry) GetAgentsByToken(ctx context.Context, token string) ([]*AgentInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var agents []*AgentInfo
	for _, agent := range m.agents {
		if agent.Token == token {
			agents = append(agents, agent)
		}
	}

	return agents, nil
}

// GetAgentStats returns statistics about registered agents.
func (m *MemoryAgentRegistry) GetAgentStats(ctx context.Context) (*AgentStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &AgentStats{
		TotalAgents: len(m.agents),
		ByLabel:     make(map[string]int),
	}

	for _, agent := range m.agents {
		if agent.Status == nil {
			continue
		}

		switch agent.Status.State {
		case AgentStateOnline:
			stats.OnlineAgents++
		case AgentStateOffline:
			stats.OfflineAgents++
		case AgentStateUnhealthy:
			stats.UnhealthyAgents++
		}
	}

	// Count by label
	for indexKey, agentIDs := range m.labelIndex {
		stats.ByLabel[indexKey] = len(agentIDs)
	}

	return stats, nil
}

// IsOnline checks if an agent is currently online.
func (m *MemoryAgentRegistry) IsOnline(ctx context.Context, agentID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return false, nil
	}

	return agent.Status != nil && agent.Status.State == AgentStateOnline, nil
}

// UpdateHealth updates an agent's health status.
func (m *MemoryAgentRegistry) UpdateHealth(ctx context.Context, agentID string, health *controlplanev1.HealthStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return errors.New("agent not found: " + agentID)
	}

	if agent.Status == nil {
		agent.Status = &AgentStatus{}
	}
	agent.Status.Health = health

	// Update state based on health
	if health != nil {
		switch health.State {
		case controlplanev1.HealthStateHealthy:
			agent.Status.State = AgentStateOnline
		case controlplanev1.HealthStateUnhealthy:
			agent.Status.State = AgentStateUnhealthy
		}
	}

	return nil
}

// SetCurrentTask sets the current task for an agent.
func (m *MemoryAgentRegistry) SetCurrentTask(ctx context.Context, agentID string, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	agent, ok := m.agents[agentID]
	if !ok {
		return errors.New("agent not found: " + agentID)
	}

	m.currentTasks[agentID] = taskID
	if agent.Status != nil {
		agent.Status.CurrentTask = taskID
	}

	return nil
}

// ClearCurrentTask clears the current task for an agent.
func (m *MemoryAgentRegistry) ClearCurrentTask(ctx context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.currentTasks, agentID)

	if agent, ok := m.agents[agentID]; ok && agent.Status != nil {
		agent.Status.CurrentTask = ""
	}

	return nil
}

// Start initializes the agent registry.
func (m *MemoryAgentRegistry) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting memory agent registry")

	// Start offline detection goroutine
	m.wg.Add(1)
	go m.offlineDetectionLoop()

	m.started = true
	return nil
}

// Close releases resources.
func (m *MemoryAgentRegistry) Close() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = false
	m.mu.Unlock()

	close(m.stopChan)
	m.wg.Wait()

	return nil
}

// offlineDetectionLoop periodically checks for offline agents.
func (m *MemoryAgentRegistry) offlineDetectionLoop() {
	defer m.wg.Done()

	interval := m.config.OfflineCheckInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.detectOfflineAgents()
		}
	}
}

// detectOfflineAgents marks agents as offline if their heartbeat is stale.
func (m *MemoryAgentRegistry) detectOfflineAgents() {
	m.mu.Lock()
	defer m.mu.Unlock()

	ttl := m.config.HeartbeatTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}

	threshold := time.Now().Add(-ttl).UnixNano()

	for agentID, agent := range m.agents {
		if agent.LastHeartbeat < threshold {
			if agent.Status != nil && agent.Status.State == AgentStateOnline {
				agent.Status.State = AgentStateOffline
				m.logger.Warn("Agent marked as offline due to heartbeat timeout",
					zap.String("agent_id", agentID),
				)
			}
		}
	}
}

// updateLabelIndex updates the label index when agent labels change.
func (m *MemoryAgentRegistry) updateLabelIndex(agentID string, oldLabels, newLabels map[string]string) {
	// Remove old labels
	for k, v := range oldLabels {
		indexKey := k + ":" + v
		if agentIDs, ok := m.labelIndex[indexKey]; ok {
			delete(agentIDs, agentID)
			if len(agentIDs) == 0 {
				delete(m.labelIndex, indexKey)
			}
		}
	}

	// Add new labels
	for k, v := range newLabels {
		indexKey := k + ":" + v
		if _, ok := m.labelIndex[indexKey]; !ok {
			m.labelIndex[indexKey] = make(map[string]bool)
		}
		m.labelIndex[indexKey][agentID] = true
	}
}
