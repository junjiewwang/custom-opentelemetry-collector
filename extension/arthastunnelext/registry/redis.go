// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisRegistry is a Redis-based implementation of AgentRegistry.
// It stores agent metadata in Redis for cross-node visibility.
type RedisRegistry struct {
	logger          *zap.Logger
	client          redis.UniversalClient
	keyPrefix       string
	indexTTL        time.Duration
	livenessChecker LivenessChecker
}

// NewRedisRegistry creates a new Redis-based agent registry.
func NewRedisRegistry(
	logger *zap.Logger,
	client redis.UniversalClient,
	keyPrefix string,
	indexTTL time.Duration,
	checker LivenessChecker,
) *RedisRegistry {
	return &RedisRegistry{
		logger:          logger,
		client:          client,
		keyPrefix:       keyPrefix,
		indexTTL:        indexTTL,
		livenessChecker: checker,
	}
}

// Key helpers
func (r *RedisRegistry) agentKey(agentID string) string {
	return r.keyPrefix + "agents:" + agentID
}

func (r *RedisRegistry) onlineZSetKey() string {
	return r.keyPrefix + "agents:online"
}

func (r *RedisRegistry) appIndexKey(appID string) string {
	return r.keyPrefix + "agents:app:" + appID
}

// Register registers an agent in Redis.
func (r *RedisRegistry) Register(ctx context.Context, info *AgentInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal agent info: %w", err)
	}

	pipe := r.client.Pipeline()

	// Store agent info as JSON
	pipe.Set(ctx, r.agentKey(info.AgentID), data, r.indexTTL)

	// Add to online ZSET with lastPongAt as score (for efficient listing)
	score := float64(info.LastPongAt)
	pipe.ZAdd(ctx, r.onlineZSetKey(), redis.Z{
		Score:  score,
		Member: info.AgentID,
	})

	// Add to app index if appID is set
	if info.AppID != "" {
		pipe.SAdd(ctx, r.appIndexKey(info.AppID), info.AgentID)
		pipe.Expire(ctx, r.appIndexKey(info.AppID), r.indexTTL)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("register agent in redis: %w", err)
	}

	r.logger.Debug("Agent registered in Redis",
		zap.String("agent_id", info.AgentID),
		zap.String("node_id", info.NodeID),
	)
	return nil
}

// Unregister removes an agent from Redis.
func (r *RedisRegistry) Unregister(ctx context.Context, agentID string) error {
	// First get agent info to know the appID
	info, err := r.Get(ctx, agentID)
	if err != nil {
		return err
	}

	pipe := r.client.Pipeline()

	// Remove agent info
	pipe.Del(ctx, r.agentKey(agentID))

	// Remove from online ZSET
	pipe.ZRem(ctx, r.onlineZSetKey(), agentID)

	// Remove from app index if appID was set
	if info != nil && info.AppID != "" {
		pipe.SRem(ctx, r.appIndexKey(info.AppID), agentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("unregister agent from redis: %w", err)
	}

	r.logger.Debug("Agent unregistered from Redis",
		zap.String("agent_id", agentID),
	)
	return nil
}

// UpdateLiveness updates the last pong time for an agent.
// Uses Lua script to ensure atomicity and monotonicity.
func (r *RedisRegistry) UpdateLiveness(ctx context.Context, agentID string, lastPongAt time.Time) error {
	// Lua script to update lastPongAt only if newer (monotonic)
	script := redis.NewScript(`
		local key = KEYS[1]
		local zsetKey = KEYS[2]
		local newPongAt = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		
		local data = redis.call('GET', key)
		if not data then
			return 0
		end
		
		local info = cjson.decode(data)
		local oldPongAt = info.last_pong_at or 0
		
		-- Only update if new time is greater (monotonic)
		if newPongAt > oldPongAt then
			info.last_pong_at = newPongAt
			redis.call('SET', key, cjson.encode(info), 'EX', ttl)
			redis.call('ZADD', zsetKey, newPongAt, ARGV[3])
		end
		
		return 1
	`)

	err := script.Run(ctx, r.client,
		[]string{r.agentKey(agentID), r.onlineZSetKey()},
		lastPongAt.UnixMilli(),
		int(r.indexTTL.Seconds()),
		agentID,
	).Err()

	if err != nil && err != redis.Nil {
		return fmt.Errorf("update liveness in redis: %w", err)
	}
	return nil
}

// Get retrieves agent info by ID.
func (r *RedisRegistry) Get(ctx context.Context, agentID string) (*AgentInfo, error) {
	data, err := r.client.Get(ctx, r.agentKey(agentID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent from redis: %w", err)
	}

	var info AgentInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("unmarshal agent info: %w", err)
	}
	return &info, nil
}

// List returns all healthy agents from Redis.
// Uses ZSET to efficiently filter by lastPongAt.
func (r *RedisRegistry) List(ctx context.Context) ([]*AgentInfo, error) {
	// Calculate the minimum score (oldest acceptable lastPongAt in milliseconds)
	minScore := float64(time.Now().Add(-r.livenessChecker.LivenessTimeout()).UnixMilli())

	// Get agent IDs with score >= minScore (healthy agents)
	agentIDs, err := r.client.ZRangeByScore(ctx, r.onlineZSetKey(), &redis.ZRangeBy{
		Min: strconv.FormatFloat(minScore, 'f', 0, 64),
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("list agents from redis: %w", err)
	}

	if len(agentIDs) == 0 {
		return []*AgentInfo{}, nil
	}

	// Batch get agent info
	return r.batchGetAgents(ctx, agentIDs)
}

// ListByAppID returns all healthy agents for a specific app ID.
func (r *RedisRegistry) ListByAppID(ctx context.Context, appID string) ([]*AgentInfo, error) {
	// Get agent IDs from app index
	agentIDs, err := r.client.SMembers(ctx, r.appIndexKey(appID)).Result()
	if err != nil {
		return nil, fmt.Errorf("list agents by app from redis: %w", err)
	}

	if len(agentIDs) == 0 {
		return []*AgentInfo{}, nil
	}

	// Batch get and filter by liveness
	agents, err := r.batchGetAgents(ctx, agentIDs)
	if err != nil {
		return nil, err
	}

	// Filter by liveness
	result := make([]*AgentInfo, 0, len(agents))
	for _, info := range agents {
		if !r.livenessChecker.IsTimeout(info.LastPongAt) {
			result = append(result, info)
		}
	}
	return result, nil
}

// batchGetAgents retrieves multiple agents by ID.
func (r *RedisRegistry) batchGetAgents(ctx context.Context, agentIDs []string) ([]*AgentInfo, error) {
	if len(agentIDs) == 0 {
		return []*AgentInfo{}, nil
	}

	// Build keys
	keys := make([]string, len(agentIDs))
	for i, id := range agentIDs {
		keys[i] = r.agentKey(id)
	}

	// MGET all agents
	results, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("batch get agents from redis: %w", err)
	}

	agents := make([]*AgentInfo, 0, len(results))
	for _, result := range results {
		if result == nil {
			continue
		}
		data, ok := result.(string)
		if !ok {
			continue
		}
		var info AgentInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			r.logger.Warn("Failed to unmarshal agent info", zap.Error(err))
			continue
		}
		agents = append(agents, &info)
	}
	return agents, nil
}

// IsLocal always returns false for Redis registry.
// Use CompositeRegistry to check local first.
func (r *RedisRegistry) IsLocal(_ string) bool {
	return false
}

// GetNodeAddr returns the node address where the agent is connected.
func (r *RedisRegistry) GetNodeAddr(ctx context.Context, agentID string) (string, error) {
	info, err := r.Get(ctx, agentID)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", nil
	}
	return info.NodeAddr, nil
}

// Close releases resources.
func (r *RedisRegistry) Close() error {
	// Don't close the Redis client as it's shared
	return nil
}

// CleanupStaleAgents removes agents from dead nodes.
// This should be called periodically by a janitor goroutine.
func (r *RedisRegistry) CleanupStaleAgents(ctx context.Context, aliveNodes map[string]bool) error {
	// Get all agent IDs
	agentIDs, err := r.client.ZRange(ctx, r.onlineZSetKey(), 0, -1).Result()
	if err != nil {
		return fmt.Errorf("get all agents: %w", err)
	}

	for _, agentID := range agentIDs {
		info, err := r.Get(ctx, agentID)
		if err != nil || info == nil {
			continue
		}

		// Check if the node is still alive
		if !aliveNodes[info.NodeID] {
			r.logger.Info("Cleaning up agent from dead node",
				zap.String("agent_id", agentID),
				zap.String("dead_node", info.NodeID),
			)
			_ = r.Unregister(ctx, agentID)
		}
	}
	return nil
}

// CleanupExpiredAgents removes agents that have timed out.
func (r *RedisRegistry) CleanupExpiredAgents(ctx context.Context) error {
	maxScore := float64(time.Now().Add(-r.livenessChecker.LivenessTimeout()).UnixMilli())

	// Remove expired agents from ZSET
	removed, err := r.client.ZRemRangeByScore(ctx, r.onlineZSetKey(), "-inf",
		strconv.FormatFloat(maxScore, 'f', 0, 64)).Result()
	if err != nil {
		return fmt.Errorf("cleanup expired agents: %w", err)
	}

	if removed > 0 {
		r.logger.Debug("Cleaned up expired agents from ZSET",
			zap.Int64("count", removed),
		)
	}
	return nil
}
