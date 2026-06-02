// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisRegistry implements NodeRegistry using Redis for multi-node deployments.
//
// Storage design:
//   - Node descriptor: HASH at "te:node:{nodeID}" (full JSON + individual fields)
//   - Heartbeat TTL:   STRING at "te:node:{nodeID}:heartbeat" with TTL
//   - Node index:      SET at "te:nodes:active" for listing all active nodes
//
// A node is considered alive if its heartbeat key exists (TTL not expired).
type RedisRegistry struct {
	client    redis.UniversalClient
	keyPrefix string
	logger    *zap.Logger
}

// RedisRegistryOption configures the RedisRegistry.
type RedisRegistryOption func(*RedisRegistry)

// WithKeyPrefix sets a custom key prefix (default: "te").
func WithKeyPrefix(prefix string) RedisRegistryOption {
	return func(r *RedisRegistry) {
		r.keyPrefix = prefix
	}
}

// NewRedisRegistry creates a new Redis-backed NodeRegistry.
func NewRedisRegistry(client redis.UniversalClient, logger *zap.Logger, opts ...RedisRegistryOption) *RedisRegistry {
	r := &RedisRegistry{
		client:    client,
		keyPrefix: "te",
		logger:    logger,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *RedisRegistry) nodeKey(nodeID string) string {
	return fmt.Sprintf("%s:node:%s", r.keyPrefix, nodeID)
}

func (r *RedisRegistry) heartbeatKey(nodeID string) string {
	return fmt.Sprintf("%s:node:%s:heartbeat", r.keyPrefix, nodeID)
}

func (r *RedisRegistry) activeSetKey() string {
	return fmt.Sprintf("%s:nodes:active", r.keyPrefix)
}

// Register stores the node descriptor and sets up the heartbeat TTL.
func (r *RedisRegistry) Register(ctx context.Context, node *NodeDescriptor, ttl time.Duration) error {
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshal node descriptor: %w", err)
	}

	pipe := r.client.Pipeline()

	// Store the full descriptor
	pipe.Set(ctx, r.nodeKey(node.ID), data, 0)

	// Set heartbeat with TTL
	pipe.Set(ctx, r.heartbeatKey(node.ID), "1", ttl)

	// Add to active set
	pipe.SAdd(ctx, r.activeSetKey(), node.ID)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("register node %s: %w", node.ID, err)
	}

	r.logger.Info("node registered",
		zap.String("nodeID", node.ID),
		zap.Strings("roles", rolesToStrings(node.Roles)),
		zap.Duration("ttl", ttl),
	)
	return nil
}

// Deregister removes the node from the cluster.
func (r *RedisRegistry) Deregister(ctx context.Context, nodeID string) error {
	pipe := r.client.Pipeline()
	pipe.Del(ctx, r.nodeKey(nodeID))
	pipe.Del(ctx, r.heartbeatKey(nodeID))
	pipe.SRem(ctx, r.activeSetKey(), nodeID)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("deregister node %s: %w", nodeID, err)
	}

	r.logger.Info("node deregistered", zap.String("nodeID", nodeID))
	return nil
}

// Heartbeat extends the node's heartbeat TTL.
func (r *RedisRegistry) Heartbeat(ctx context.Context, nodeID string) error {
	// We need the original TTL — use a fixed heartbeat interval approach:
	// Just refresh the key with GET + conditional SET.
	// Simpler: use a Lua script or just re-set with default TTL.
	// For robustness, we re-read the key's TTL and extend it.
	// But actually the simplest approach is to always use a standard TTL.
	// The caller (heartbeat goroutine) knows the TTL from config.
	// So we just check existence and refresh.
	exists, err := r.client.Exists(ctx, r.nodeKey(nodeID)).Result()
	if err != nil {
		return fmt.Errorf("heartbeat check node %s: %w", nodeID, err)
	}
	if exists == 0 {
		return fmt.Errorf("node %s not registered (node key missing)", nodeID)
	}

	// Extend heartbeat TTL (use EXPIRE to avoid overwriting the value)
	// We use the GetEx trick: get the current TTL and re-set
	ttl, err := r.client.TTL(ctx, r.heartbeatKey(nodeID)).Result()
	if err != nil {
		return fmt.Errorf("heartbeat get TTL for node %s: %w", nodeID, err)
	}

	// Default TTL if key somehow lost its TTL
	refreshTTL := ttl
	if refreshTTL <= 0 {
		refreshTTL = 30 * time.Second
	}

	_, err = r.client.Set(ctx, r.heartbeatKey(nodeID), "1", refreshTTL).Result()
	if err != nil {
		return fmt.Errorf("heartbeat refresh node %s: %w", nodeID, err)
	}
	return nil
}

// HeartbeatWithTTL extends the heartbeat with an explicit TTL (preferred API).
func (r *RedisRegistry) HeartbeatWithTTL(ctx context.Context, nodeID string, ttl time.Duration) error {
	exists, err := r.client.Exists(ctx, r.nodeKey(nodeID)).Result()
	if err != nil {
		return fmt.Errorf("heartbeat check node %s: %w", nodeID, err)
	}
	if exists == 0 {
		return fmt.Errorf("node %s not registered", nodeID)
	}

	_, err = r.client.Set(ctx, r.heartbeatKey(nodeID), "1", ttl).Result()
	if err != nil {
		return fmt.Errorf("heartbeat refresh node %s: %w", nodeID, err)
	}
	return nil
}

// GetNode returns the descriptor for a specific node, or nil if not found/expired.
func (r *RedisRegistry) GetNode(ctx context.Context, nodeID string) (*NodeDescriptor, error) {
	// Check heartbeat first (if expired, node is dead)
	alive, err := r.client.Exists(ctx, r.heartbeatKey(nodeID)).Result()
	if err != nil {
		return nil, fmt.Errorf("check heartbeat for node %s: %w", nodeID, err)
	}
	if alive == 0 {
		// Clean up stale entry
		r.cleanupStaleNode(ctx, nodeID)
		return nil, nil
	}

	data, err := r.client.Get(ctx, r.nodeKey(nodeID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", nodeID, err)
	}

	var desc NodeDescriptor
	if err := json.Unmarshal([]byte(data), &desc); err != nil {
		return nil, fmt.Errorf("unmarshal node %s: %w", nodeID, err)
	}
	return &desc, nil
}

// ListNodes returns all active nodes matching the filter.
func (r *RedisRegistry) ListNodes(ctx context.Context, filter *NodeFilter) ([]*NodeDescriptor, error) {
	// Get all registered node IDs
	nodeIDs, err := r.client.SMembers(ctx, r.activeSetKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("list active nodes: %w", err)
	}

	var result []*NodeDescriptor
	var staleIDs []string

	for _, nodeID := range nodeIDs {
		// Check if node is still alive (heartbeat not expired)
		alive, err := r.client.Exists(ctx, r.heartbeatKey(nodeID)).Result()
		if err != nil {
			r.logger.Warn("error checking heartbeat", zap.String("nodeID", nodeID), zap.Error(err))
			continue
		}
		if alive == 0 {
			staleIDs = append(staleIDs, nodeID)
			continue
		}

		// Fetch descriptor
		data, err := r.client.Get(ctx, r.nodeKey(nodeID)).Result()
		if err != nil {
			if err != redis.Nil {
				r.logger.Warn("error fetching node descriptor", zap.String("nodeID", nodeID), zap.Error(err))
			}
			staleIDs = append(staleIDs, nodeID)
			continue
		}

		var desc NodeDescriptor
		if err := json.Unmarshal([]byte(data), &desc); err != nil {
			r.logger.Warn("error unmarshaling node descriptor", zap.String("nodeID", nodeID), zap.Error(err))
			continue
		}

		// Apply filter
		if filter.Matches(&desc) {
			result = append(result, &desc)
		}
	}

	// Async cleanup of stale nodes
	if len(staleIDs) > 0 {
		go r.cleanupStaleNodes(context.Background(), staleIDs)
	}

	return result, nil
}

// CountByCapability returns the number of active nodes with the given capability.
func (r *RedisRegistry) CountByCapability(ctx context.Context, cap Capability) (int, error) {
	nodes, err := r.ListNodes(ctx, &NodeFilter{
		RequiredCapabilities: []Capability{cap},
	})
	if err != nil {
		return 0, err
	}
	return len(nodes), nil
}

// Close releases resources.
func (r *RedisRegistry) Close() error {
	// Redis client is shared; don't close it here
	return nil
}

// cleanupStaleNode removes a single stale node entry.
func (r *RedisRegistry) cleanupStaleNode(ctx context.Context, nodeID string) {
	pipe := r.client.Pipeline()
	pipe.Del(ctx, r.nodeKey(nodeID))
	pipe.SRem(ctx, r.activeSetKey(), nodeID)
	if _, err := pipe.Exec(ctx); err != nil {
		r.logger.Warn("failed to cleanup stale node", zap.String("nodeID", nodeID), zap.Error(err))
	}
}

// cleanupStaleNodes batch-cleans stale entries from the active set.
func (r *RedisRegistry) cleanupStaleNodes(ctx context.Context, nodeIDs []string) {
	for _, nodeID := range nodeIDs {
		r.cleanupStaleNode(ctx, nodeID)
	}
	if len(nodeIDs) > 0 {
		r.logger.Info("cleaned up stale nodes", zap.Int("count", len(nodeIDs)))
	}
}

// rolesToStrings converts a slice of Roles to strings for logging.
func rolesToStrings(roles []Role) []string {
	s := make([]string, len(roles))
	for i, role := range roles {
		s[i] = string(role)
	}
	return s
}
