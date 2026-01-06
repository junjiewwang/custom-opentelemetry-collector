// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// CompositeRegistry combines local and Redis registries.
// It writes to both and reads from local first, then Redis.
// This provides fast local lookups with distributed visibility.
type CompositeRegistry struct {
	logger *zap.Logger
	local  *LocalRegistry
	redis  *RedisRegistry
	nodeID string
}

// NewCompositeRegistry creates a new composite registry.
func NewCompositeRegistry(
	logger *zap.Logger,
	local *LocalRegistry,
	redis *RedisRegistry,
	nodeID string,
) *CompositeRegistry {
	return &CompositeRegistry{
		logger: logger,
		local:  local,
		redis:  redis,
		nodeID: nodeID,
	}
}

// Register registers an agent in both local and Redis.
func (r *CompositeRegistry) Register(ctx context.Context, info *AgentInfo) error {
	// Register locally first
	if err := r.local.Register(ctx, info); err != nil {
		return err
	}

	// Register in Redis (best effort, log error but don't fail)
	if r.redis != nil {
		if err := r.redis.Register(ctx, info); err != nil {
			r.logger.Warn("Failed to register agent in Redis, degraded to local only",
				zap.String("agent_id", info.AgentID),
				zap.Error(err),
			)
		}
	}
	return nil
}

// Unregister removes an agent from both local and Redis.
func (r *CompositeRegistry) Unregister(ctx context.Context, agentID string) error {
	// Unregister locally first
	if err := r.local.Unregister(ctx, agentID); err != nil {
		return err
	}

	// Unregister from Redis (best effort)
	if r.redis != nil {
		if err := r.redis.Unregister(ctx, agentID); err != nil {
			r.logger.Warn("Failed to unregister agent from Redis",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}
	return nil
}

// UpdateLiveness updates the last pong time.
// Updates local immediately, Redis is batched by LivenessUpdater.
func (r *CompositeRegistry) UpdateLiveness(ctx context.Context, agentID string, lastPongAt time.Time) error {
	// Update local immediately
	return r.local.UpdateLiveness(ctx, agentID, lastPongAt)
}

// UpdateLivenessRedis updates liveness in Redis.
// This is called by LivenessUpdater for batched updates.
func (r *CompositeRegistry) UpdateLivenessRedis(ctx context.Context, agentID string, lastPongAt time.Time) error {
	if r.redis == nil {
		return nil
	}
	return r.redis.UpdateLiveness(ctx, agentID, lastPongAt)
}

// Get retrieves agent info by ID.
// Checks local first, then Redis.
func (r *CompositeRegistry) Get(ctx context.Context, agentID string) (*AgentInfo, error) {
	// Check local first (fast path)
	info, err := r.local.Get(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if info != nil {
		return info, nil
	}

	// Check Redis (agent might be on another node)
	if r.redis != nil {
		info, err = r.redis.Get(ctx, agentID)
		if err != nil {
			r.logger.Warn("Failed to get agent from Redis, degraded to local only",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
			return nil, nil
		}
		return info, nil
	}

	return nil, nil
}

// List returns all healthy agents from both local and Redis.
func (r *CompositeRegistry) List(ctx context.Context) ([]*AgentInfo, error) {
	// If no Redis, return local only
	if r.redis == nil {
		return r.local.List(ctx)
	}

	// Get from Redis (includes all nodes)
	agents, err := r.redis.List(ctx)
	if err != nil {
		r.logger.Warn("Failed to list agents from Redis, degraded to local only",
			zap.Error(err),
		)
		return r.local.List(ctx)
	}

	return agents, nil
}

// ListByAppID returns all healthy agents for a specific app ID.
func (r *CompositeRegistry) ListByAppID(ctx context.Context, appID string) ([]*AgentInfo, error) {
	// If no Redis, return local only
	if r.redis == nil {
		return r.local.ListByAppID(ctx, appID)
	}

	// Get from Redis
	agents, err := r.redis.ListByAppID(ctx, appID)
	if err != nil {
		r.logger.Warn("Failed to list agents by app from Redis, degraded to local only",
			zap.String("app_id", appID),
			zap.Error(err),
		)
		return r.local.ListByAppID(ctx, appID)
	}

	return agents, nil
}

// IsLocal returns true if the agent is connected to this node.
func (r *CompositeRegistry) IsLocal(agentID string) bool {
	return r.local.IsLocal(agentID)
}

// GetNodeAddr returns the node address where the agent is connected.
func (r *CompositeRegistry) GetNodeAddr(ctx context.Context, agentID string) (string, error) {
	// Check local first
	addr, err := r.local.GetNodeAddr(ctx, agentID)
	if err != nil {
		return "", err
	}
	if addr != "" {
		return addr, nil
	}

	// Check Redis
	if r.redis != nil {
		addr, err = r.redis.GetNodeAddr(ctx, agentID)
		if err != nil {
			r.logger.Warn("Failed to get node addr from Redis",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
			return "", nil
		}
		return addr, nil
	}

	return "", nil
}

// Close releases resources.
func (r *CompositeRegistry) Close() error {
	_ = r.local.Close()
	if r.redis != nil {
		_ = r.redis.Close()
	}
	return nil
}

// GetLocalRegistry returns the underlying local registry.
func (r *CompositeRegistry) GetLocalRegistry() *LocalRegistry {
	return r.local
}

// GetRedisRegistry returns the underlying Redis registry.
func (r *CompositeRegistry) GetRedisRegistry() *RedisRegistry {
	return r.redis
}

// IsAgentOnThisNode checks if the agent is on this node.
func (r *CompositeRegistry) IsAgentOnThisNode(ctx context.Context, agentID string) bool {
	info, err := r.Get(ctx, agentID)
	if err != nil || info == nil {
		return false
	}
	return info.NodeID == r.nodeID
}
