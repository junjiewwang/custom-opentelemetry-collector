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

// RedisAgentRegistry implements AgentRegistry using Redis as backend.
// Key structure:
//   - {prefix}:app/{appID}/svc/{serviceName}/host/{hostname} -> String (instance info JSON with InstanceTTL)
//   - {prefix}:_hb/{fullPath} -> String ("1" with HeartbeatTTL, for online detection)
//   - {prefix}:_ids -> Hash (agentID -> fullPath)
//   - {prefix}:_online -> ZSet (fullPath with heartbeat timestamps)
//   - {prefix}:_tasks -> Hash (agentID -> taskID)
//   - {prefix}:_apps -> Set (all app IDs, escaped) [optional, when EnableHierarchyIndex=true]
//   - {prefix}:app/{appID}:_services -> Set (service names, escaped) [optional]
//   - {prefix}:app/{appID}/svc/{serviceName}:_instances -> Set (instance keys) [optional]
type RedisAgentRegistry struct {
	logger       *zap.Logger
	config       Config
	keys         *KeyBuilder
	statusHelper *StatusHelper
	statsHelper  *StatsHelper

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
		logger:       logger,
		config:       config,
		keys:         NewKeyBuilderWithMode(keyPrefix, config.InstanceKeyMode),
		statusHelper: NewStatusHelper(),
		statsHelper:  NewStatsHelper(),
		client:       client,
		stopChan:     make(chan struct{}),
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
		zap.String("key_prefix", r.keys.prefix),
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
	if err := agent.Validate(); err != nil {
		return err
	}

	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	now := r.statusHelper.Now()
	agent.RegisteredAt = now
	agent.LastHeartbeat = now

	r.statusHelper.InitializeStatus(agent, now)

	return r.upsertAgent(ctx, client, agent, now, true)
}

// Heartbeat updates the agent's heartbeat and status.
func (r *RedisAgentRegistry) Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error {
	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	// Get agent by ID
	agent, err := r.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}

	now := r.statusHelper.Now()
	agent.LastHeartbeat = now

	r.statusHelper.UpdateHeartbeatStatus(agent, status, now)

	return r.upsertAgent(ctx, client, agent, now, false)
}

// RegisterOrHeartbeat registers a new agent if not exists, or updates heartbeat if exists.
func (r *RedisAgentRegistry) RegisterOrHeartbeat(ctx context.Context, agent *AgentInfo) error {
	if err := agent.Validate(); err != nil {
		return err
	}

	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	now := r.statusHelper.Now()

	// Try to get existing agent
	existing, err := r.GetAgent(ctx, agent.AgentID)
	if err == nil && existing != nil {
		// Update existing agent
		existing.LastHeartbeat = now
		existing.MergeFrom(agent)

		wasOffline := r.statusHelper.UpdateHeartbeatStatus(existing, agent.Status, now)

		if err := r.upsertAgent(ctx, client, existing, now, false); err != nil {
			return err
		}

		if wasOffline {
			r.logger.Info("Agent recovered from offline",
				zap.String("agent_id", agent.AgentID),
			)
		} else {
			r.logger.Debug("Agent heartbeat updated",
				zap.String("agent_id", agent.AgentID),
			)
		}
		return nil
	}

	// Register new agent
	agent.RegisteredAt = now
	agent.LastHeartbeat = now

	r.statusHelper.InitializeStatus(agent, now)

	if err := r.upsertAgent(ctx, client, agent, now, true); err != nil {
		return err
	}

	r.logger.Info("Agent auto-registered via heartbeat",
		zap.String("agent_id", agent.AgentID),
		zap.String("app_id", agent.AppID),
		zap.String("service_name", agent.ServiceName),
		zap.String("hostname", agent.Hostname),
	)

	return nil
}

// Unregister removes an agent from the registry.
func (r *RedisAgentRegistry) Unregister(ctx context.Context, agentID string) error {
	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	// Get agent info first
	agent, err := r.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}

	instanceKey := r.keys.InstanceKey(agent.AppID, agent.ServiceName, agent.Hostname, agent.IP)
	fullPath := r.keys.FullKeyPath(agent.AppID, agent.ServiceName, agent.Hostname, agent.IP)
	heartbeatKey := r.keys.HeartbeatKey(fullPath)

	pipe := client.TxPipeline()

	// Remove essential keys
	pipe.Del(ctx, instanceKey)
	pipe.Del(ctx, heartbeatKey)
	pipe.ZRem(ctx, r.keys.OnlineKey(), fullPath)
	pipe.HDel(ctx, r.keys.AgentIDsKey(), agentID)
	pipe.HDel(ctx, r.keys.TasksKey(), agentID)

	// Remove from hierarchy indexes if enabled
	if r.config.EnableHierarchyIndex {
		instanceID := r.keys.InstanceID(agent.Hostname, agent.IP)
		pipe.SRem(ctx, r.keys.InstancesKey(agent.AppID, agent.ServiceName), instanceID)
	}

	// Publish offline event
	if r.config.EnableEvents {
		pipe.Publish(ctx, r.keys.EventOfflineKey(), agentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	// Clean up empty indexes if hierarchy index is enabled
	if r.config.EnableHierarchyIndex {
		r.cleanupEmptyIndexes(ctx, client, agent.AppID, agent.ServiceName)
	}

	r.logger.Info("Agent unregistered", zap.String("agent_id", agentID))
	return nil
}

// GetAgent retrieves information about a specific agent.
func (r *RedisAgentRegistry) GetAgent(ctx context.Context, agentID string) (*AgentInfo, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	// Get full key path from agent IDs hash
	fullPath, err := client.HGet(ctx, r.keys.AgentIDsKey(), agentID).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	if err != nil {
		return nil, err
	}

	// Parse full path to get instance key
	appGroupB64, serviceNameB64, instanceKey, err := r.keys.ParseFullKeyPath(fullPath)
	if err != nil {
		return nil, err
	}

	// Get instance info
	instanceInfoKey := r.keys.InstanceKeyFromParts(appGroupB64, serviceNameB64, instanceKey)
	data, err := client.Get(ctx, instanceInfoKey).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
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
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	// Get all members from online sorted set
	fullPaths, err := client.ZRange(ctx, r.keys.OnlineKey(), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	var agents []*AgentInfo
	for _, fullPath := range fullPaths {
		appGroupB64, serviceNameB64, instanceKey, err := r.keys.ParseFullKeyPath(fullPath)
		if err != nil {
			continue
		}

		instanceInfoKey := r.keys.InstanceKeyFromParts(appGroupB64, serviceNameB64, instanceKey)
		data, err := client.Get(ctx, instanceInfoKey).Result()
		if err != nil {
			continue
		}

		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// GetAllAgents returns all agents (including offline ones within InstanceTTL).
// This uses the _ids hash to get all agent IDs, then fetches their info.
func (r *RedisAgentRegistry) GetAllAgents(ctx context.Context) ([]*AgentInfo, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	// Get all agent ID -> fullPath mappings from _ids hash
	agentPaths, err := client.HGetAll(ctx, r.keys.AgentIDsKey()).Result()
	if err != nil {
		return nil, err
	}

	var agents []*AgentInfo
	for _, fullPath := range agentPaths {
		appIDEsc, serviceNameEsc, instanceKey, err := r.keys.ParseFullKeyPath(fullPath)
		if err != nil {
			continue
		}

		instanceInfoKey := r.keys.InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey)
		data, err := client.Get(ctx, instanceInfoKey).Result()
		if err != nil {
			// Instance info expired (TTL), skip it
			continue
		}

		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// GetAgentsByLabel returns agents matching the specified label.
func (r *RedisAgentRegistry) GetAgentsByLabel(ctx context.Context, labelKey, labelValue string) ([]*AgentInfo, error) {
	// Get all online agents and filter by label
	agents, err := r.GetOnlineAgents(ctx)
	if err != nil {
		return nil, err
	}

	var result []*AgentInfo
	for _, agent := range agents {
		if v, ok := agent.Labels[labelKey]; ok && v == labelValue {
			result = append(result, agent)
		}
	}

	return result, nil
}

// GetAgentsByToken returns all agents under a specific token/app.
func (r *RedisAgentRegistry) GetAgentsByToken(ctx context.Context, token string) ([]*AgentInfo, error) {
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

// GetApps returns all app IDs.
func (r *RedisAgentRegistry) GetApps(ctx context.Context) ([]string, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	encodedGroups, err := client.SMembers(ctx, r.keys.GroupsKey()).Result()
	if err != nil {
		return nil, err
	}

	groups := make([]string, 0, len(encodedGroups))
	for _, encoded := range encodedGroups {
		decoded, err := r.keys.Decode(encoded)
		if err != nil {
			continue
		}
		groups = append(groups, decoded)
	}

	return groups, nil
}

// GetServicesByApp returns all service names under an app.
func (r *RedisAgentRegistry) GetServicesByApp(ctx context.Context, appID string) ([]string, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	encodedServices, err := client.SMembers(ctx, r.keys.ServicesKey(appID)).Result()
	if err != nil {
		return nil, err
	}

	services := make([]string, 0, len(encodedServices))
	for _, encoded := range encodedServices {
		decoded, err := r.keys.Decode(encoded)
		if err != nil {
			continue
		}
		services = append(services, decoded)
	}

	return services, nil
}

// GetInstancesByService returns all instances under a service.
func (r *RedisAgentRegistry) GetInstancesByService(ctx context.Context, appID, serviceName string) ([]*AgentInfo, error) {
	client := r.getClient()
	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	instanceKeys, err := client.SMembers(ctx, r.keys.InstancesKey(appID, serviceName)).Result()
	if err != nil {
		return nil, err
	}

	appIDB64 := r.keys.Encode(appID)
	serviceNameB64 := r.keys.Encode(serviceName)

	var agents []*AgentInfo
	for _, instanceKey := range instanceKeys {
		instanceInfoKey := r.keys.InstanceKeyFromParts(appIDB64, serviceNameB64, instanceKey)
		data, err := client.Get(ctx, instanceInfoKey).Result()
		if err != nil {
			continue
		}

		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// GetAgentStats returns statistics about registered agents.
func (r *RedisAgentRegistry) GetAgentStats(ctx context.Context) (*AgentStats, error) {
	// Get all agents (including offline) for accurate statistics
	agents, err := r.GetAllAgents(ctx)
	if err != nil {
		return nil, err
	}

	return r.statsHelper.CalculateStats(agents), nil
}

// IsOnline checks if an agent is currently online.
// Online status is determined by the existence of the heartbeat key.
func (r *RedisAgentRegistry) IsOnline(ctx context.Context, agentID string) (bool, error) {
	client := r.getClient()
	if client == nil {
		return false, errors.New("redis client not initialized")
	}

	// Get fullPath from agent IDs hash
	fullPath, err := client.HGet(ctx, r.keys.AgentIDsKey(), agentID).Result()
	if err == redis.Nil {
		return false, nil // Agent not found, not online
	}
	if err != nil {
		return false, err
	}

	// Check if heartbeat key exists
	heartbeatKey := r.keys.HeartbeatKey(fullPath)
	exists, err := client.Exists(ctx, heartbeatKey).Result()
	if err != nil {
		return false, err
	}

	return exists > 0, nil
}

// UpdateHealth updates an agent's health status.
func (r *RedisAgentRegistry) UpdateHealth(ctx context.Context, agentID string, health *controlplanev1.HealthStatus) error {
	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	agent, err := r.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}

	now := r.statusHelper.Now()
	r.statusHelper.UpdateHealthStatus(agent, health, now)

	agentData, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	instanceKey := r.keys.InstanceKey(agent.AppID, agent.ServiceName, agent.Hostname, agent.IP)

	// Get remaining TTL
	ttl, err := client.TTL(ctx, instanceKey).Result()
	if err != nil || ttl <= 0 {
		ttl = r.config.InstanceTTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
	}

	return client.Set(ctx, instanceKey, agentData, ttl).Err()
}

// SetCurrentTask sets the current task for an agent.
func (r *RedisAgentRegistry) SetCurrentTask(ctx context.Context, agentID string, taskID string) error {
	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	return client.HSet(ctx, r.keys.TasksKey(), agentID, taskID).Err()
}

// ClearCurrentTask clears the current task for an agent.
func (r *RedisAgentRegistry) ClearCurrentTask(ctx context.Context, agentID string) error {
	client := r.getClient()
	if client == nil {
		return errors.New("redis client not initialized")
	}

	return client.HDel(ctx, r.keys.TasksKey(), agentID).Err()
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

	return nil
}

// Helper methods

func (r *RedisAgentRegistry) getClient() redis.UniversalClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

func (r *RedisAgentRegistry) upsertAgent(ctx context.Context, client redis.UniversalClient, agent *AgentInfo, now int64, isNew bool) error {
	agentData, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	instanceKey := r.keys.InstanceKey(agent.AppID, agent.ServiceName, agent.Hostname, agent.IP)
	fullPath := r.keys.FullKeyPath(agent.AppID, agent.ServiceName, agent.Hostname, agent.IP)
	heartbeatKey := r.keys.HeartbeatKey(fullPath)

	// Determine TTLs
	instanceTTL := r.config.InstanceTTL
	if instanceTTL <= 0 {
		instanceTTL = 24 * time.Hour
	}
	heartbeatTTL := r.config.HeartbeatTTL
	if heartbeatTTL <= 0 {
		heartbeatTTL = 30 * time.Second
	}

	pipe := client.TxPipeline()

	// 1. Store instance info with long TTL (for historical queries)
	pipe.Set(ctx, instanceKey, agentData, instanceTTL)

	// 2. Set heartbeat key with short TTL (for online detection)
	// When this key expires, the agent is considered offline
	pipe.Set(ctx, heartbeatKey, "1", heartbeatTTL)

	// 3. Add to online sorted set with heartbeat timestamp
	pipe.ZAdd(ctx, r.keys.OnlineKey(), redis.Z{
		Score:  float64(now),
		Member: fullPath,
	})

	// 4. Set agent ID in _ids hash
	pipe.HSet(ctx, r.keys.AgentIDsKey(), agent.AgentID, fullPath)

	// Optional hierarchy indexes (only when enabled):
	if r.config.EnableHierarchyIndex {
		appIDEsc := r.keys.Escape(agent.AppID)
		serviceNameEsc := r.keys.Escape(agent.ServiceName)
		instanceID := r.keys.InstanceID(agent.Hostname, agent.IP)

		// Add to groups index (apps index)
		pipe.SAdd(ctx, r.keys.GroupsKey(), appIDEsc)

		// Add to services index
		pipe.SAdd(ctx, r.keys.ServicesKeyEscaped(appIDEsc), serviceNameEsc)

		// Add to instances index
		pipe.SAdd(ctx, r.keys.InstancesKeyEscaped(appIDEsc, serviceNameEsc), instanceID)
	}

	// Publish online event for new registrations
	if isNew && r.config.EnableEvents {
		pipe.Publish(ctx, r.keys.EventOnlineKey(), agent.AgentID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	if isNew {
		r.logger.Info("Agent registered",
			zap.String("agent_id", agent.AgentID),
			zap.String("app_id", agent.AppID),
			zap.String("service_name", agent.ServiceName),
			zap.String("hostname", agent.Hostname),
		)
	}

	return nil
}

func (r *RedisAgentRegistry) cleanupEmptyIndexes(ctx context.Context, client redis.UniversalClient, appID, serviceName string) {
	appIDB64 := r.keys.Encode(appID)
	serviceNameB64 := r.keys.Encode(serviceName)

	// Check if instances set is empty
	instancesKey := r.keys.InstancesKeyEncoded(appIDB64, serviceNameB64)
	count, err := client.SCard(ctx, instancesKey).Result()
	if err != nil {
		return
	}

	if count == 0 {
		// Remove empty instances set and service from services index
		pipe := client.TxPipeline()
		pipe.Del(ctx, instancesKey)
		pipe.SRem(ctx, r.keys.ServicesKeyEncoded(appIDB64), serviceNameB64)
		_, _ = pipe.Exec(ctx)

		// Check if services set is empty
		servicesKey := r.keys.ServicesKeyEncoded(appIDB64)
		serviceCount, err := client.SCard(ctx, servicesKey).Result()
		if err != nil {
			return
		}

		if serviceCount == 0 {
			// Remove empty services set and app from groups index
			pipe := client.TxPipeline()
			pipe.Del(ctx, servicesKey)
			pipe.SRem(ctx, r.keys.GroupsKey(), appIDB64)
			_, _ = pipe.Exec(ctx)
		}
	}
}

// markAgentOffline marks an agent as offline and updates its status in the instance info.
func (r *RedisAgentRegistry) markAgentOffline(ctx context.Context, client redis.UniversalClient, fullPath string, agentIDMap map[string]string, now int64) {
	appIDEsc, serviceNameEsc, instanceKey, err := r.keys.ParseFullKeyPath(fullPath)
	if err != nil {
		// Failed to parse - might be old format data, remove it directly
		r.logger.Warn("Failed to parse offline agent path, removing from online set",
			zap.String("full_path", fullPath),
			zap.Error(err),
		)
		client.ZRem(ctx, r.keys.OnlineKey(), fullPath)
		return
	}

	// Get instance info key
	instanceInfoKey := r.keys.InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey)

	// Try to update instance info with offline status
	data, err := client.Get(ctx, instanceInfoKey).Result()
	if err == nil {
		// Instance info exists, update status to offline
		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err == nil {
			// Use statusHelper to mark offline
			if r.statusHelper.MarkOffline(&agent, now) {
				// Write back with remaining TTL
				ttl, _ := client.TTL(ctx, instanceInfoKey).Result()
				if ttl <= 0 {
					instanceTTL := r.config.InstanceTTL
					if instanceTTL <= 0 {
						instanceTTL = 24 * time.Hour
					}
					ttl = instanceTTL
				}

				if agentData, err := json.Marshal(&agent); err == nil {
					client.Set(ctx, instanceInfoKey, agentData, ttl)
				}
			}
		}
	}

	// Remove from online set
	pipe := client.TxPipeline()
	pipe.ZRem(ctx, r.keys.OnlineKey(), fullPath)

	// Remove from instances set (only if hierarchy index is enabled)
	if r.config.EnableHierarchyIndex {
		pipe.SRem(ctx, r.keys.InstancesKeyEscaped(appIDEsc, serviceNameEsc), instanceKey)
	}

	// Get agentID for event publishing
	agentID := agentIDMap[fullPath]

	// Publish offline event
	if r.config.EnableEvents && agentID != "" {
		pipe.Publish(ctx, r.keys.EventOfflineKey(), agentID)
	}

	_, execErr := pipe.Exec(ctx)
	if execErr != nil {
		r.logger.Warn("Failed to mark agent offline",
			zap.String("full_path", fullPath),
			zap.Error(execErr),
		)
		return
	}

	r.logger.Info("Agent marked as offline due to heartbeat expiration",
		zap.String("full_path", fullPath),
		zap.String("agent_id", agentID),
	)

	// Clean up empty indexes (only if hierarchy index is enabled)
	if r.config.EnableHierarchyIndex {
		appID := r.keys.Unescape(appIDEsc)
		serviceName := r.keys.Unescape(serviceNameEsc)
		if appID != "" && serviceName != "" {
			r.cleanupEmptyIndexes(ctx, client, appID, serviceName)
		}
	}
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

// detectOfflineAgents checks for offline agents based on heartbeat key expiration.
// When a heartbeat key expires, the agent is marked as offline and its status is updated.
func (r *RedisAgentRegistry) detectOfflineAgents() {
	client := r.getClient()
	if client == nil {
		return
	}

	ctx := context.Background()

	// Get all members from online sorted set
	fullPaths, err := client.ZRange(ctx, r.keys.OnlineKey(), 0, -1).Result()
	if err != nil {
		r.logger.Warn("Failed to get online agents", zap.Error(err))
		return
	}

	if len(fullPaths) == 0 {
		return
	}

	// Build heartbeat keys for batch checking
	heartbeatKeys := make([]string, len(fullPaths))
	for i, fullPath := range fullPaths {
		heartbeatKeys[i] = r.keys.HeartbeatKey(fullPath)
	}

	// Batch check heartbeat key existence using pipeline
	pipe := client.Pipeline()
	existsCmds := make([]*redis.IntCmd, len(heartbeatKeys))
	for i, key := range heartbeatKeys {
		existsCmds[i] = pipe.Exists(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		r.logger.Warn("Failed to check heartbeat keys", zap.Error(err))
		return
	}

	// Build a map of fullPath -> agentID for efficient lookup
	agentIDMap := make(map[string]string) // fullPath -> agentID
	allAgentIDs, err := client.HGetAll(ctx, r.keys.AgentIDsKey()).Result()
	if err != nil {
		r.logger.Warn("Failed to get agent IDs", zap.Error(err))
	} else {
		for agentID, fullPath := range allAgentIDs {
			agentIDMap[fullPath] = agentID
		}
	}

	now := r.statusHelper.Now()

	// Process offline agents
	for i, fullPath := range fullPaths {
		exists, err := existsCmds[i].Result()
		if err != nil {
			continue
		}

		// Heartbeat key exists, agent is online
		if exists > 0 {
			continue
		}

		// Heartbeat key expired, agent is offline
		r.markAgentOffline(ctx, client, fullPath, agentIDMap, now)
	}
}
