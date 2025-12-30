// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// Redis key patterns
const (
	keyAgentInfo      = "%s:info:%s"          // Hash: agent info (with TTL)
	keyOnlineSet      = "%s:online"           // Set: online agent IDs
	keyHeartbeatZSet  = "%s:heartbeat"        // ZSet: agent heartbeats (score = timestamp)
	keyLabelIndex     = "%s:label:%s:%s"      // Set: agents by label
	keyAgentTasks     = "%s:tasks"            // Hash: agentID -> current taskID
	keyEventOnline    = "%s:events:online"    // Pub/Sub: agent online
	keyEventOffline   = "%s:events:offline"   // Pub/Sub: agent offline
)

// RedisAgentRegistry implements AgentRegistry using Redis as backend.
type RedisAgentRegistry struct {
	logger    *zap.Logger
	config    Config
	keyPrefix string

	mu       sync.RWMutex
	client   redis.UniversalClient
	started  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewRedisAgentRegistry creates a new Redis-based agent registry.
func NewRedisAgentRegistry(logger *zap.Logger, config Config, client redis.UniversalClient) (*RedisAgentRegistry, error) {
	keyPrefix := config.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "otel:agents"
	}

	return &RedisAgentRegistry{
		logger:    logger,
		config:    config,
		keyPrefix: keyPrefix,
		client:    client,
		stopChan:  make(chan struct{}),
	}, nil
}

// Ensure RedisAgentRegistry implements AgentRegistry.
var _ AgentRegistry = (*RedisAgentRegistry)(nil)

// Start initializes the Redis client and starts background tasks.
func (r *RedisAgentRegistry) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	r.logger.Info("Starting Redis agent registry",
		zap.String("key_prefix", r.keyPrefix),
	)

	if r.client == nil {
		return errors.New("redis client not provided")
	}

	// Test connection
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	r.started = true

	// Start offline detection goroutine
	r.wg.Add(1)
	go r.offlineDetectionLoop()

	return nil
}

// Register registers a new agent.
func (r *RedisAgentRegistry) Register(ctx context.Context, agent *AgentInfo) error {
	if agent == nil {
		return errors.New("agent cannot be nil")
	}
	if agent.AgentID == "" {
		return errors.New("agent_id is required")
	}

	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

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

	// Serialize agent info
	agentData, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agent.AgentID)
	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)
	heartbeatKey := fmt.Sprintf(keyHeartbeatZSet, r.keyPrefix)

	pipe := client.TxPipeline()

	// Store agent info with TTL
	pipe.Set(ctx, infoKey, agentData, r.config.HeartbeatTTL)

	// Add to online set
	pipe.SAdd(ctx, onlineKey, agent.AgentID)

	// Add to heartbeat sorted set
	pipe.ZAdd(ctx, heartbeatKey, redis.Z{
		Score:  float64(now),
		Member: agent.AgentID,
	})

	// Add to label indexes
	for k, v := range agent.Labels {
		labelKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, k, v)
		pipe.SAdd(ctx, labelKey, agent.AgentID)
	}

	// Publish online event
	if r.config.EnableEvents {
		eventKey := fmt.Sprintf(keyEventOnline, r.keyPrefix)
		pipe.Publish(ctx, eventKey, agent.AgentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	r.logger.Info("Agent registered",
		zap.String("agent_id", agent.AgentID),
		zap.String("hostname", agent.Hostname),
	)

	return nil
}

// Heartbeat updates the agent's heartbeat and status.
func (r *RedisAgentRegistry) Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agentID)
	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)
	heartbeatKey := fmt.Sprintf(keyHeartbeatZSet, r.keyPrefix)

	now := time.Now().UnixNano()

	// Get current agent info
	data, err := client.Get(ctx, infoKey).Result()
	if err == redis.Nil {
		return errors.New("agent not found: " + agentID)
	}
	if err != nil {
		return err
	}

	var agent AgentInfo
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return err
	}

	// Update agent info
	agent.LastHeartbeat = now
	if status != nil {
		status.State = AgentStateOnline
		agent.Status = status
	} else if agent.Status != nil {
		agent.Status.State = AgentStateOnline
	}

	agentData, err := json.Marshal(&agent)
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()

	// Update agent info and refresh TTL
	pipe.Set(ctx, infoKey, agentData, r.config.HeartbeatTTL)

	// Ensure in online set
	pipe.SAdd(ctx, onlineKey, agentID)

	// Update heartbeat timestamp
	pipe.ZAdd(ctx, heartbeatKey, redis.Z{
		Score:  float64(now),
		Member: agentID,
	})

	_, err = pipe.Exec(ctx)
	return err
}

// RegisterOrHeartbeat registers a new agent if not exists, or updates heartbeat if exists.
// This provides upsert semantics for automatic registration via status reports.
func (r *RedisAgentRegistry) RegisterOrHeartbeat(ctx context.Context, agent *AgentInfo) error {
	if agent == nil {
		return errors.New("agent cannot be nil")
	}
	if agent.AgentID == "" {
		return errors.New("agent_id is required")
	}

	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agent.AgentID)
	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)
	heartbeatKey := fmt.Sprintf(keyHeartbeatZSet, r.keyPrefix)

	now := time.Now().UnixNano()

	// Try to get existing agent info
	data, err := client.Get(ctx, infoKey).Result()
	if err != nil && err != redis.Nil {
		return err
	}

	var existing *AgentInfo
	if err == nil {
		// Agent exists, unmarshal it
		existing = &AgentInfo{}
		if err := json.Unmarshal([]byte(data), existing); err != nil {
			return err
		}
	}

	if existing != nil {
		// Update existing agent
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
			// Remove old label indexes
			pipe := client.TxPipeline()
			for k, v := range existing.Labels {
				labelKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, k, v)
				pipe.SRem(ctx, labelKey, agent.AgentID)
			}
			_, _ = pipe.Exec(ctx)

			existing.Labels = agent.Labels
		}

		agentData, err := json.Marshal(existing)
		if err != nil {
			return err
		}

		pipe := client.TxPipeline()

		// Update agent info and refresh TTL
		pipe.Set(ctx, infoKey, agentData, r.config.HeartbeatTTL)

		// Ensure in online set
		pipe.SAdd(ctx, onlineKey, agent.AgentID)

		// Update heartbeat timestamp
		pipe.ZAdd(ctx, heartbeatKey, redis.Z{
			Score:  float64(now),
			Member: agent.AgentID,
		})

		// Add new label indexes
		for k, v := range existing.Labels {
			labelKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, k, v)
			pipe.SAdd(ctx, labelKey, agent.AgentID)
		}

		_, err = pipe.Exec(ctx)
		if err != nil {
			return err
		}

		r.logger.Debug("Agent heartbeat updated",
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

	agentData, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()

	// Store agent info with TTL
	pipe.Set(ctx, infoKey, agentData, r.config.HeartbeatTTL)

	// Add to online set
	pipe.SAdd(ctx, onlineKey, agent.AgentID)

	// Add to heartbeat sorted set
	pipe.ZAdd(ctx, heartbeatKey, redis.Z{
		Score:  float64(now),
		Member: agent.AgentID,
	})

	// Add to label indexes
	for k, v := range agent.Labels {
		labelKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, k, v)
		pipe.SAdd(ctx, labelKey, agent.AgentID)
	}

	// Publish online event
	if r.config.EnableEvents {
		eventKey := fmt.Sprintf(keyEventOnline, r.keyPrefix)
		pipe.Publish(ctx, eventKey, agent.AgentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	r.logger.Info("Agent auto-registered via heartbeat",
		zap.String("agent_id", agent.AgentID),
		zap.String("hostname", agent.Hostname),
	)

	return nil
}

// Unregister removes an agent from the registry.
func (r *RedisAgentRegistry) Unregister(ctx context.Context, agentID string) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agentID)
	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)
	heartbeatKey := fmt.Sprintf(keyHeartbeatZSet, r.keyPrefix)
	tasksKey := fmt.Sprintf(keyAgentTasks, r.keyPrefix)

	// Get agent info for label cleanup
	data, err := client.Get(ctx, infoKey).Result()
	var agent AgentInfo
	if err == nil {
		_ = json.Unmarshal([]byte(data), &agent)
	}

	pipe := client.TxPipeline()

	// Remove agent info
	pipe.Del(ctx, infoKey)

	// Remove from online set
	pipe.SRem(ctx, onlineKey, agentID)

	// Remove from heartbeat sorted set
	pipe.ZRem(ctx, heartbeatKey, agentID)

	// Remove current task
	pipe.HDel(ctx, tasksKey, agentID)

	// Remove from label indexes
	for k, v := range agent.Labels {
		labelKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, k, v)
		pipe.SRem(ctx, labelKey, agentID)
	}

	// Publish offline event
	if r.config.EnableEvents {
		eventKey := fmt.Sprintf(keyEventOffline, r.keyPrefix)
		pipe.Publish(ctx, eventKey, agentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	r.logger.Info("Agent unregistered", zap.String("agent_id", agentID))
	return nil
}

// GetAgent retrieves information about a specific agent.
func (r *RedisAgentRegistry) GetAgent(ctx context.Context, agentID string) (*AgentInfo, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agentID)

	data, err := client.Get(ctx, infoKey).Result()
	if err == redis.Nil {
		return nil, errors.New("agent not found: " + agentID)
	}
	if err != nil {
		return nil, err
	}

	var agent AgentInfo
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return nil, err
	}

	return &agent, nil
}

// GetOnlineAgents returns all online agents.
func (r *RedisAgentRegistry) GetOnlineAgents(ctx context.Context) ([]*AgentInfo, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)

	agentIDs, err := client.SMembers(ctx, onlineKey).Result()
	if err != nil {
		return nil, err
	}

	var agents []*AgentInfo
	for _, agentID := range agentIDs {
		agent, err := r.GetAgent(ctx, agentID)
		if err == nil {
			agents = append(agents, agent)
		}
	}

	return agents, nil
}

// GetAgentsByLabel returns agents matching the specified label.
func (r *RedisAgentRegistry) GetAgentsByLabel(ctx context.Context, labelKey, labelValue string) ([]*AgentInfo, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	indexKey := fmt.Sprintf(keyLabelIndex, r.keyPrefix, labelKey, labelValue)

	agentIDs, err := client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, err
	}

	var agents []*AgentInfo
	for _, agentID := range agentIDs {
		agent, err := r.GetAgent(ctx, agentID)
		if err == nil {
			agents = append(agents, agent)
		}
	}

	return agents, nil
}

// GetAgentsByToken returns all agents under a specific token/app.
func (r *RedisAgentRegistry) GetAgentsByToken(ctx context.Context, token string) ([]*AgentInfo, error) {
	// Get all online agents and filter by token
	agents, err := r.GetOnlineAgents(ctx)
	if err != nil {
		return nil, err
	}

	var result []*AgentInfo
	for _, agent := range agents {
		if agent.Token == token {
			result = append(result, agent)
		}
	}

	return result, nil
}

// GetAgentStats returns statistics about registered agents.
func (r *RedisAgentRegistry) GetAgentStats(ctx context.Context) (*AgentStats, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)

	onlineCount, err := client.SCard(ctx, onlineKey).Result()
	if err != nil {
		return nil, err
	}

	// Get all online agents to count states
	agents, err := r.GetOnlineAgents(ctx)
	if err != nil {
		return nil, err
	}

	stats := &AgentStats{
		TotalAgents:  int(onlineCount),
		OnlineAgents: 0,
		ByLabel:      make(map[string]int),
	}

	for _, agent := range agents {
		if agent.Status != nil {
			switch agent.Status.State {
			case AgentStateOnline:
				stats.OnlineAgents++
			case AgentStateOffline:
				stats.OfflineAgents++
			case AgentStateUnhealthy:
				stats.UnhealthyAgents++
			}
		}

		for k, v := range agent.Labels {
			key := k + ":" + v
			stats.ByLabel[key]++
		}
	}

	return stats, nil
}

// IsOnline checks if an agent is currently online.
func (r *RedisAgentRegistry) IsOnline(ctx context.Context, agentID string) (bool, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return false, errors.New("redis client not initialized")
	}

	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)
	return client.SIsMember(ctx, onlineKey, agentID).Result()
}

// UpdateHealth updates an agent's health status.
func (r *RedisAgentRegistry) UpdateHealth(ctx context.Context, agentID string, health *controlplanev1.HealthStatus) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agentID)

	// Get current agent info
	data, err := client.Get(ctx, infoKey).Result()
	if err == redis.Nil {
		return errors.New("agent not found: " + agentID)
	}
	if err != nil {
		return err
	}

	var agent AgentInfo
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return err
	}

	// Update health
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

	agentData, err := json.Marshal(&agent)
	if err != nil {
		return err
	}

	// Get remaining TTL
	ttl, err := client.TTL(ctx, infoKey).Result()
	if err != nil || ttl <= 0 {
		ttl = r.config.HeartbeatTTL
	}

	return client.Set(ctx, infoKey, agentData, ttl).Err()
}

// SetCurrentTask sets the current task for an agent.
func (r *RedisAgentRegistry) SetCurrentTask(ctx context.Context, agentID string, taskID string) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	tasksKey := fmt.Sprintf(keyAgentTasks, r.keyPrefix)
	return client.HSet(ctx, tasksKey, agentID, taskID).Err()
}

// ClearCurrentTask clears the current task for an agent.
func (r *RedisAgentRegistry) ClearCurrentTask(ctx context.Context, agentID string) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	tasksKey := fmt.Sprintf(keyAgentTasks, r.keyPrefix)
	return client.HDel(ctx, tasksKey, agentID).Err()
}

// Close releases resources.
func (r *RedisAgentRegistry) Close() error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = false
	r.mu.Unlock()

	close(r.stopChan)
	r.wg.Wait()

	// Note: We don't close the Redis client here because it's managed by the storage extension

	return nil
}

// offlineDetectionLoop periodically checks for offline agents.
func (r *RedisAgentRegistry) offlineDetectionLoop() {
	defer r.wg.Done()

	interval := r.config.OfflineCheckInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.detectOfflineAgents()
		}
	}
}

// detectOfflineAgents marks agents as offline if their heartbeat is stale.
func (r *RedisAgentRegistry) detectOfflineAgents() {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return
	}

	ctx := context.Background()
	heartbeatKey := fmt.Sprintf(keyHeartbeatZSet, r.keyPrefix)
	onlineKey := fmt.Sprintf(keyOnlineSet, r.keyPrefix)

	ttl := r.config.HeartbeatTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}

	threshold := float64(time.Now().Add(-ttl).UnixNano())

	// Get agents with stale heartbeats
	staleAgents, err := client.ZRangeByScore(ctx, heartbeatKey, &redis.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%f", threshold),
	}).Result()
	if err != nil {
		r.logger.Warn("Failed to get stale agents", zap.Error(err))
		return
	}

	for _, agentID := range staleAgents {
		// Check if agent info key still exists (might have expired)
		infoKey := fmt.Sprintf(keyAgentInfo, r.keyPrefix, agentID)
		exists, err := client.Exists(ctx, infoKey).Result()
		if err != nil {
			continue
		}

		if exists == 0 {
			// Agent info expired, clean up
			pipe := client.TxPipeline()
			pipe.SRem(ctx, onlineKey, agentID)
			pipe.ZRem(ctx, heartbeatKey, agentID)

			if r.config.EnableEvents {
				eventKey := fmt.Sprintf(keyEventOffline, r.keyPrefix)
				pipe.Publish(ctx, eventKey, agentID)
			}

			_, _ = pipe.Exec(ctx)

			r.logger.Warn("Agent marked as offline due to TTL expiration",
				zap.String("agent_id", agentID),
			)
		}
	}
}
