// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// LocalRegistry is an in-memory implementation of AgentRegistry.
// It only stores agents connected to this node.
type LocalRegistry struct {
	logger          *zap.Logger
	nodeID          string
	nodeAddr        string
	livenessChecker LivenessChecker

	mu     sync.RWMutex
	agents map[string]*AgentInfo
}

// NewLocalRegistry creates a new local (in-memory) agent registry.
func NewLocalRegistry(logger *zap.Logger, nodeID, nodeAddr string, checker LivenessChecker) *LocalRegistry {
	return &LocalRegistry{
		logger:          logger,
		nodeID:          nodeID,
		nodeAddr:        nodeAddr,
		livenessChecker: checker,
		agents:          make(map[string]*AgentInfo),
	}
}

// Register registers an agent locally.
func (r *LocalRegistry) Register(_ context.Context, info *AgentInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure node info is set
	info.NodeID = r.nodeID
	info.NodeAddr = r.nodeAddr

	r.agents[info.AgentID] = info
	r.logger.Debug("Agent registered locally",
		zap.String("agent_id", info.AgentID),
		zap.String("app_id", info.AppID),
	)
	return nil
}

// Unregister removes an agent from local registry.
func (r *LocalRegistry) Unregister(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; ok {
		delete(r.agents, agentID)
		r.logger.Debug("Agent unregistered locally",
			zap.String("agent_id", agentID),
		)
	}
	return nil
}

// UpdateLiveness updates the last pong time for an agent.
func (r *LocalRegistry) UpdateLiveness(_ context.Context, agentID string, lastPongAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if info, ok := r.agents[agentID]; ok {
		info.LastPongAt = lastPongAt.UnixMilli()
	}
	return nil
}

// Get retrieves agent info by ID.
func (r *LocalRegistry) Get(_ context.Context, agentID string) (*AgentInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.agents[agentID]
	if !ok {
		return nil, nil
	}
	// Return a copy to avoid race conditions
	copied := *info
	return &copied, nil
}

// List returns all healthy local agents.
func (r *LocalRegistry) List(_ context.Context) ([]*AgentInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*AgentInfo, 0, len(r.agents))
	for _, info := range r.agents {
		if r.livenessChecker != nil && r.livenessChecker.IsTimeout(info.LastPongAt) {
			continue
		}
		copied := *info
		result = append(result, &copied)
	}
	return result, nil
}

// ListByAppID returns all healthy local agents for a specific app ID.
func (r *LocalRegistry) ListByAppID(_ context.Context, appID string) ([]*AgentInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*AgentInfo, 0)
	for _, info := range r.agents {
		if info.AppID != appID {
			continue
		}
		if r.livenessChecker != nil && r.livenessChecker.IsTimeout(info.LastPongAt) {
			continue
		}
		copied := *info
		result = append(result, &copied)
	}
	return result, nil
}

// IsLocal returns true if the agent is connected to this node.
func (r *LocalRegistry) IsLocal(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[agentID]
	return ok
}

// GetNodeAddr returns the node address where the agent is connected.
func (r *LocalRegistry) GetNodeAddr(_ context.Context, agentID string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.agents[agentID]; ok {
		return info.NodeAddr, nil
	}
	return "", nil
}

// Close releases resources.
func (r *LocalRegistry) Close() error {
	return nil
}

// GetLocalAgent returns the local agent info for internal use.
// This is used by arthasURICompat to access the local agent directly.
func (r *LocalRegistry) GetLocalAgent(agentID string) *AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[agentID]
}

// RangeLocal iterates over all local agents.
func (r *LocalRegistry) RangeLocal(fn func(agentID string, info *AgentInfo) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, info := range r.agents {
		if !fn(id, info) {
			break
		}
	}
}
