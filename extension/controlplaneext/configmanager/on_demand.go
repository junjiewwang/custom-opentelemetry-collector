// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

const (
	// DefaultConfigDataId is no longer used but kept for interface compatibility if needed.
	// In the simplified design, we only use ServiceName as DataId.
	DefaultConfigDataId = "_unused_default_"
)

// OnDemandConfig holds configuration for OnDemandConfigManager.
type OnDemandConfig struct {
	// Namespace for Nacos (empty for default namespace).
	Namespace string `mapstructure:"namespace"`

	// LoadTimeout is the timeout for loading a single config.
	LoadTimeout time.Duration `mapstructure:"load_timeout"`

	// MaxRetries is the max retries for failed operations.
	MaxRetries int `mapstructure:"max_retries"`

	// RetryInterval is the interval between retries.
	RetryInterval time.Duration `mapstructure:"retry_interval"`

	// CacheExpiration is how long cached configs remain valid.
	CacheExpiration time.Duration `mapstructure:"cache_expiration"`

	// CleanupInterval is the interval for cleaning up expired cache entries.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
}

// DefaultOnDemandConfig returns default configuration.
func DefaultOnDemandConfig() OnDemandConfig {
	return OnDemandConfig{
		Namespace:       "",
		LoadTimeout:     5 * time.Second,
		MaxRetries:      3,
		RetryInterval:   1 * time.Second,
		CacheExpiration: 5 * time.Minute,
		CleanupInterval: 1 * time.Minute,
	}
}

// AgentConfigEntry represents a cached config entry for an agent.
type AgentConfigEntry struct {
	Config     *model.AgentConfig
	Token      string
	AgentID    string
	LoadedAt   time.Time
	LastAccess time.Time
	Version    string
	LoadError  error
	IsWatching bool
	IsDefault  bool // True if this is the default config for the token
}

// OnDemandCacheStats holds cache statistics.
type OnDemandCacheStats struct {
	TotalCachedConfigs int       `json:"total_cached_configs"`
	TotalWatching      int       `json:"total_watching"`
	TotalRegistered    int       `json:"total_registered"`
	CacheHits          int64     `json:"cache_hits"`
	CacheMisses        int64     `json:"cache_misses"`
	LastCleanupTime    time.Time `json:"last_cleanup_time"`
}

// NacosOnDemandConfigManager implements OnDemandConfigManager using Nacos.
// Uses model.AgentConfig as the canonical type.
type NacosOnDemandConfigManager struct {
	logger *zap.Logger
	config OnDemandConfig
	client config_client.IConfigClient

	// configCache stores configs by "token:agentId" key.
	configCache sync.Map // map[string]*AgentConfigEntry

	// registeredAgents tracks which agents are registered.
	// Key: token, Value: map[agentID]bool
	registeredAgents sync.Map

	// agentSubscribers stores callbacks for agent config changes.
	// Key: "token:agentId", Value: []AgentConfigChangeCallback
	agentSubscribers sync.Map

	// Subscribers for ConfigManager interface.
	subscribers []ConfigChangeCallback
	subMu       sync.RWMutex

	// Stats
	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64
	lastCleanupTime time.Time

	// State
	started atomic.Bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	statsMu sync.RWMutex
}

// NewNacosOnDemandConfigManager creates a new on-demand config manager.
func NewNacosOnDemandConfigManager(
	logger *zap.Logger,
	config OnDemandConfig,
	_ interface{}, // Unused migration config (kept for compatibility during transition)
	client config_client.IConfigClient,
) (*NacosOnDemandConfigManager, error) {
	if client == nil {
		return nil, errors.New("nacos client is required")
	}

	// Apply defaults
	if config.LoadTimeout <= 0 {
		config.LoadTimeout = 5 * time.Second
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 1 * time.Second
	}
	if config.CacheExpiration <= 0 {
		config.CacheExpiration = 5 * time.Minute
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 1 * time.Minute
	}

	return &NacosOnDemandConfigManager{
		logger:      logger,
		config:      config,
		client:      client,
		subscribers: make([]ConfigChangeCallback, 0),
		stopCh:      make(chan struct{}),
	}, nil
}

// Ensure NacosOnDemandConfigManager implements the interfaces.
var (
	_ ConfigManager         = (*NacosOnDemandConfigManager)(nil)
	_ OnDemandConfigManager = (*NacosOnDemandConfigManager)(nil)
)

// Start initializes the manager.
func (m *NacosOnDemandConfigManager) Start(ctx context.Context) error {
	if m.started.Swap(true) {
		return nil
	}

	m.logger.Info("Starting on-demand config manager",
		zap.String("namespace", m.config.Namespace),
		zap.Duration("load_timeout", m.config.LoadTimeout),
		zap.Duration("cache_expiration", m.config.CacheExpiration),
	)

	// Start cleanup goroutine
	m.wg.Add(1)
	go m.runCleanupLoop()

	return nil
}

// runCleanupLoop periodically cleans up expired cache entries.
func (m *NacosOnDemandConfigManager) runCleanupLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanupExpiredEntries()
		}
	}
}

// cleanupExpiredEntries removes expired cache entries for unregistered agents.
func (m *NacosOnDemandConfigManager) cleanupExpiredEntries() {
	now := time.Now()
	expireThreshold := now.Add(-m.config.CacheExpiration)

	var cleaned int
	m.configCache.Range(func(key, value interface{}) bool {
		entry := value.(*AgentConfigEntry)

		// Skip if agent is still registered
		if m.isAgentRegistered(entry.Token, entry.AgentID) {
			return true
		}

		// Remove if expired
		if entry.LastAccess.Before(expireThreshold) {
			// Cancel watch if active
			if entry.IsWatching {
				m.cancelWatch(entry.Token, entry.AgentID)
			}
			m.configCache.Delete(key)
			cleaned++
		}

		return true
	})

	m.statsMu.Lock()
	m.lastCleanupTime = now
	m.statsMu.Unlock()

	if cleaned > 0 {
		m.logger.Debug("Cleaned up expired cache entries", zap.Int("count", cleaned))
	}
}

// isAgentRegistered checks if an agent is registered.
func (m *NacosOnDemandConfigManager) isAgentRegistered(token, agentID string) bool {
	if agents, ok := m.registeredAgents.Load(token); ok {
		agentMap := agents.(*sync.Map)
		_, exists := agentMap.Load(agentID)
		return exists
	}
	return false
}

// cacheKey generates a cache key.
func (m *NacosOnDemandConfigManager) cacheKey(token, agentID string) string {
	return token + ":" + agentID
}

// serviceDataID returns the Nacos DataID for a service-level config.
// In the simplified design, ServiceName is directly used as DataID.
func (m *NacosOnDemandConfigManager) serviceDataID(serviceName string) string {
	return serviceName
}

// RegisterAgent registers an agent and starts watching its config.
func (m *NacosOnDemandConfigManager) RegisterAgent(ctx context.Context, token, agentID, serviceName string) (*model.AgentConfig, error) {
	if token == "" || agentID == "" {
		return nil, errors.New("token and agentID are required")
	}

	m.logger.Debug("Registering agent",
		zap.String("token", token),
		zap.String("agent_id", agentID),
		zap.String("service_name", serviceName),
	)

	// Add to registered agents
	agents, _ := m.registeredAgents.LoadOrStore(token, &sync.Map{})
	agentMap := agents.(*sync.Map)
	agentMap.Store(agentID, true)

	// Try to load config with hierarchy: Instance -> Service -> App Default
	config, err := m.GetConfigForAgent(ctx, token, agentID, serviceName)
	if err != nil {
		m.logger.Debug("Failed to load initial config for agent",
			zap.String("token", token),
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
	}

	// Setup watch for service-level config only
	if svcID := m.serviceDataID(serviceName); svcID != "" {
		m.setupWatch(token, svcID)
	}

	return config, nil
}

// UnregisterAgent unregisters an agent and releases its resources.
func (m *NacosOnDemandConfigManager) UnregisterAgent(ctx context.Context, token, agentID string) error {
	if token == "" || agentID == "" {
		return errors.New("token and agentID are required")
	}

	m.logger.Debug("Unregistering agent",
		zap.String("token", token),
		zap.String("agent_id", agentID),
	)

	// Remove from registered agents
	if agents, ok := m.registeredAgents.Load(token); ok {
		agentMap := agents.(*sync.Map)
		agentMap.Delete(agentID)

		// Check if any agents left under this token
		hasAgents := false
		agentMap.Range(func(_, _ interface{}) bool {
			hasAgents = true
			return false
		})

		if !hasAgents {
			m.registeredAgents.Delete(token)
		}
	}

	// Remove subscribers
	m.agentSubscribers.Delete(m.cacheKey(token, agentID))

	// Note: We don't watch individual agents anymore, and we don't watch default config.
	// Service-level watches are shared and kept active as long as any agent for that service is online.
	// Cleanup of service watches is handled by cache expiration if no one accesses them.

	return nil
}

// GetConfigForAgent returns config for a specific agent.
// In the simplified design, it only looks for service-level configuration.
func (m *NacosOnDemandConfigManager) GetConfigForAgent(ctx context.Context, token, agentID, serviceName string) (*model.AgentConfig, error) {
	if token == "" {
		return nil, errors.New("token is required")
	}

	if serviceName == "" {
		return nil, nil // No service name, no config
	}

	// 1. Try service-specific config from cache
	svcID := m.serviceDataID(serviceName)
	svcKey := m.cacheKey(token, svcID)
	if entry, ok := m.configCache.Load(svcKey); ok {
		e := entry.(*AgentConfigEntry)
		e.LastAccess = time.Now()
		m.cacheHits.Add(1)
		if e.Config != nil {
			return e.Config, nil
		}
	}

	m.cacheMisses.Add(1)

	// 2. Try service-specific config from Nacos
	config, err := m.loadConfig(ctx, token, svcID)
	if err == nil && config != nil {
		return config, nil
	}

	return nil, nil // No config found for this service
}

// loadConfig loads config from Nacos with caching.
func (m *NacosOnDemandConfigManager) loadConfig(ctx context.Context, token, dataID string) (*model.AgentConfig, error) {
	key := m.cacheKey(token, dataID)

	// Load with retry
	var lastErr error
	for retry := 0; retry <= m.config.MaxRetries; retry++ {
		if retry > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(m.config.RetryInterval):
			}
		}

		content, err := m.loadConfigContent(ctx, token, dataID)
		if err != nil {
			lastErr = err
			continue
		}
		if content == "" {
			return nil, errors.New("config not found")
		}

		var cfg model.AgentConfig
		if err := json.Unmarshal([]byte(content), &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}
		m.cacheConfig(token, dataID, &cfg)
		return &cfg, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to load config %s: %w", key, lastErr)
	}
	return nil, errors.New("config not found")
}

func (m *NacosOnDemandConfigManager) cacheConfig(token, dataID string, cfg *model.AgentConfig) {
	if cfg == nil {
		return
	}
	key := m.cacheKey(token, dataID)
	m.configCache.Store(key, &AgentConfigEntry{
		Config:     cfg,
		Token:      token,
		AgentID:    dataID,
		LoadedAt:   time.Now(),
		LastAccess: time.Now(),
		Version:    cfg.Version,
		IsDefault:  dataID == DefaultConfigDataId,
	})

	m.logger.Debug("Config loaded and cached", zap.String("token", token), zap.String("data_id", dataID), zap.String("version", cfg.Version))
}

// loadConfigContent loads config content from Nacos with timeout.
func (m *NacosOnDemandConfigManager) loadConfigContent(ctx context.Context, group, dataID string) (string, error) {
	type result struct {
		content string
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		content, err := m.client.GetConfig(vo.ConfigParam{
			Group:  group,
			DataId: dataID,
		})
		resultCh <- result{content: content, err: err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(m.config.LoadTimeout):
		return "", errors.New("load config timeout")
	case res := <-resultCh:
		return res.content, res.err
	}
}

// publishConfig publishes config to Nacos.
func (m *NacosOnDemandConfigManager) publishConfig(ctx context.Context, group, dataID, content string) error {
	type result struct {
		success bool
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		success, err := m.client.PublishConfig(vo.ConfigParam{
			Group:   group,
			DataId:  dataID,
			Content: content,
			Type:    "json",
		})
		resultCh <- result{success: success, err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(m.config.LoadTimeout):
		return errors.New("publish config timeout")
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		if !res.success {
			return errors.New("failed to publish config to Nacos")
		}
		return nil
	}
}

// SetServiceConfig sets/updates config for a specific service.
// It automatically manages versioning (Version, UpdatedAt, Etag).
func (m *NacosOnDemandConfigManager) SetServiceConfig(ctx context.Context, token, serviceName string, config *model.AgentConfig) error {
	svcID := m.serviceDataID(serviceName)
	if svcID == "" {
		return errors.New("serviceName is required")
	}

	if config == nil {
		return errors.New("config cannot be nil")
	}

	// 1. Auto-generate Metadata
	now := time.Now().UnixMilli()
	config.UpdatedAt = now
	config.Version = fmt.Sprintf("v%d", now)

	// 2. Compute ETag (excluding metadata fields to get a stable hash of business config)
	// We'll use a temporary copy to clear metadata before hashing
	tempCfg := *config
	tempCfg.Version = ""
	tempCfg.UpdatedAt = 0
	tempCfg.Etag = ""
	
	businessData, _ := json.Marshal(tempCfg)
	hash := md5.Sum(businessData)
	config.Etag = hex.EncodeToString(hash[:])

	// 3. Validate and Publish
	if err := m.validateConfig(config); err != nil {
		return err
	}

	data, err := json.Marshal(config)
	if err != nil {
		return err
	}

	if err := m.publishConfig(ctx, token, svcID, string(data)); err != nil {
		return err
	}

	m.cacheConfig(token, svcID, config)
	m.logger.Info("Config set for service", zap.String("token", token), zap.String("service_name", serviceName), zap.String("version", config.Version))
	return nil
}

// GetServiceConfig returns the config for a specific service.
func (m *NacosOnDemandConfigManager) GetServiceConfig(ctx context.Context, token, serviceName string) (*model.AgentConfig, error) {
	svcID := m.serviceDataID(serviceName)
	if svcID == "" {
		return nil, errors.New("serviceName is required")
	}
	return m.loadConfig(ctx, token, svcID)
}

// DeleteServiceConfig deletes config for a specific service.
func (m *NacosOnDemandConfigManager) DeleteServiceConfig(ctx context.Context, token, serviceName string) error {
	svcID := m.serviceDataID(serviceName)
	if svcID == "" {
		return errors.New("serviceName is required")
	}

	type result struct {
		success bool
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		success, err := m.client.DeleteConfig(vo.ConfigParam{Group: token, DataId: svcID})
		resultCh <- result{success: success, err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		if !res.success {
			return errors.New("failed to delete config from Nacos")
		}
	}

	m.configCache.Delete(m.cacheKey(token, svcID))
	m.logger.Info("Config deleted for service", zap.String("token", token), zap.String("service_name", serviceName))
	return nil
}

// SetDefaultConfig is deprecated and returns an error.
func (m *NacosOnDemandConfigManager) SetDefaultConfig(ctx context.Context, token string, config *model.AgentConfig) error {
	return errors.New("default config is no longer supported")
}

// GetDefaultConfig is deprecated and returns nil.
func (m *NacosOnDemandConfigManager) GetDefaultConfig(ctx context.Context, token string) (*model.AgentConfig, error) {
	return nil, nil
}

// SetConfigForAgent is deprecated and returns an error.
func (m *NacosOnDemandConfigManager) SetConfigForAgent(ctx context.Context, token, agentID string, config *model.AgentConfig) error {
	return errors.New("instance-level config is no longer supported")
}

// DeleteConfigForAgent is deprecated and returns an error.
func (m *NacosOnDemandConfigManager) DeleteConfigForAgent(ctx context.Context, token, agentID string) error {
	return errors.New("instance-level config is no longer supported")
}

// setupWatch sets up config change watching.
func (m *NacosOnDemandConfigManager) setupWatch(token, dataID string) {
	key := m.cacheKey(token, dataID)

	// Check if already watching
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		if e.IsWatching {
			return
		}
	}

	err := m.client.ListenConfig(vo.ConfigParam{
		Group:  token,
		DataId: dataID,
		OnChange: func(namespace, group, dataId, data string) {
			m.handleConfigChange(group, dataId, data)
		},
	})

	if err != nil {
		m.logger.Warn("Failed to setup config watch",
			zap.String("token", token),
			zap.String("data_id", dataID),
			zap.Error(err),
		)
		return
	}

	// Mark as watching
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		e.IsWatching = true
	} else {
		// Create placeholder entry
		m.configCache.Store(key, &AgentConfigEntry{
			Token:      token,
			AgentID:    dataID,
			LastAccess: time.Now(),
			IsWatching: true,
			IsDefault:  dataID == DefaultConfigDataId,
		})
	}

	m.logger.Debug("Setup config watch",
		zap.String("token", token),
		zap.String("data_id", dataID),
	)
}

// cancelWatch cancels config change watching.
func (m *NacosOnDemandConfigManager) cancelWatch(token, dataID string) {
	key := m.cacheKey(token, dataID)

	// Check if watching
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		if !e.IsWatching {
			return
		}
		e.IsWatching = false
	}

	err := m.client.CancelListenConfig(vo.ConfigParam{
		Group:  token,
		DataId: dataID,
	})

	if err != nil {
		m.logger.Warn("Failed to cancel config watch",
			zap.String("token", token),
			zap.String("data_id", dataID),
			zap.Error(err),
		)
		return
	}

	m.logger.Debug("Cancelled config watch",
		zap.String("token", token),
		zap.String("data_id", dataID),
	)
}

// handleConfigChange handles config change from Nacos watch.
func (m *NacosOnDemandConfigManager) handleConfigChange(token, dataID, data string) {
	key := m.cacheKey(token, dataID)

	m.logger.Info("Config changed",
		zap.String("token", token),
		zap.String("data_id", dataID),
	)

	// Get old config
	var oldConfig *model.AgentConfig
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		oldConfig = e.Config
	}

	// Parse new config
	var newConfig *model.AgentConfig
	var eventType string

	if data == "" {
		eventType = "deleted"
		// Remove from cache but keep watching flag
		if entry, ok := m.configCache.Load(key); ok {
			e := entry.(*AgentConfigEntry)
			e.Config = nil
			e.LastAccess = time.Now()
		}
	} else {
		var config model.AgentConfig
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			m.logger.Error("Failed to parse changed config",
				zap.String("token", token),
				zap.String("data_id", dataID),
				zap.Error(err),
			)
			return
		}

		newConfig = &config
		if oldConfig == nil {
			eventType = "created"
		} else {
			eventType = "updated"
		}

		// Update cache
		if entry, ok := m.configCache.Load(key); ok {
			e := entry.(*AgentConfigEntry)
			e.Config = newConfig
			e.Version = config.Version
			e.LastAccess = time.Now()
			e.LoadedAt = time.Now()
		} else {
			m.configCache.Store(key, &AgentConfigEntry{
				Config:     newConfig,
				Token:      token,
				AgentID:    dataID,
				LoadedAt:   time.Now(),
				LastAccess: time.Now(),
				Version:    config.Version,
				IsDefault:  dataID == DefaultConfigDataId,
			})
		}
	}

	// Create event
	event := &AgentConfigChangeEvent{
		Type:      eventType,
		Token:     token,
		AgentID:   dataID,
		OldConfig: oldConfig,
		NewConfig: newConfig,
		Timestamp: time.Now().UnixMilli(),
	}

	// Notify agent-specific subscribers
	m.notifyAgentSubscribers(token, dataID, event)

	// Notify ConfigManager subscribers
	if newConfig != nil {
		m.notifySubscribers(oldConfig, newConfig)
	}
}

// notifyAgentSubscribers notifies subscribers for a specific agent.
func (m *NacosOnDemandConfigManager) notifyAgentSubscribers(token, agentID string, event *AgentConfigChangeEvent) {
	key := m.cacheKey(token, agentID)
	if subs, ok := m.agentSubscribers.Load(key); ok {
		callbacks := subs.([]AgentConfigChangeCallback)
		for _, cb := range callbacks {
			cb(event)
		}
	}
}

// notifyAllAgentsForToken notifies all agents under a token about default config change.
func (m *NacosOnDemandConfigManager) notifyAllAgentsForToken(token string, event *AgentConfigChangeEvent) {
	if agents, ok := m.registeredAgents.Load(token); ok {
		agentMap := agents.(*sync.Map)
		agentMap.Range(func(agentID, _ interface{}) bool {
			// Create agent-specific event
			agentEvent := &AgentConfigChangeEvent{
				Type:      event.Type,
				Token:     token,
				AgentID:   agentID.(string),
				OldConfig: event.OldConfig,
				NewConfig: event.NewConfig,
				Timestamp: event.Timestamp,
			}
			m.notifyAgentSubscribers(token, agentID.(string), agentEvent)
			return true
		})
	}
}

// notifySubscribers notifies ConfigManager subscribers.
func (m *NacosOnDemandConfigManager) notifySubscribers(oldConfig, newConfig *model.AgentConfig) {
	m.subMu.RLock()
	subscribers := make([]ConfigChangeCallback, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.subMu.RUnlock()

	for _, sub := range subscribers {
		sub(oldConfig, newConfig)
	}
}

// SubscribeAgentConfig subscribes to config changes for a specific agent.
func (m *NacosOnDemandConfigManager) SubscribeAgentConfig(token, agentID string, callback AgentConfigChangeCallback) {
	key := m.cacheKey(token, agentID)

	existing, _ := m.agentSubscribers.LoadOrStore(key, []AgentConfigChangeCallback{})
	callbacks := existing.([]AgentConfigChangeCallback)
	callbacks = append(callbacks, callback)
	m.agentSubscribers.Store(key, callbacks)
}

// UnsubscribeAgentConfig unsubscribes from config changes.
func (m *NacosOnDemandConfigManager) UnsubscribeAgentConfig(token, agentID string) {
	key := m.cacheKey(token, agentID)
	m.agentSubscribers.Delete(key)
}

// GetRegisteredAgents returns all registered agents.
func (m *NacosOnDemandConfigManager) GetRegisteredAgents() map[string][]string {
	result := make(map[string][]string)

	m.registeredAgents.Range(func(token, agents interface{}) bool {
		agentMap := agents.(*sync.Map)
		var agentList []string
		agentMap.Range(func(agentID, _ interface{}) bool {
			agentList = append(agentList, agentID.(string))
			return true
		})
		result[token.(string)] = agentList
		return true
	})

	return result
}

// GetCacheStats returns cache statistics.
func (m *NacosOnDemandConfigManager) GetCacheStats() *OnDemandCacheStats {
	stats := &OnDemandCacheStats{
		CacheHits:   m.cacheHits.Load(),
		CacheMisses: m.cacheMisses.Load(),
	}

	// Count cached configs
	m.configCache.Range(func(_, value interface{}) bool {
		stats.TotalCachedConfigs++
		entry := value.(*AgentConfigEntry)
		if entry.IsWatching {
			stats.TotalWatching++
		}
		return true
	})

	// Count registered agents
	m.registeredAgents.Range(func(_, agents interface{}) bool {
		agentMap := agents.(*sync.Map)
		agentMap.Range(func(_, _ interface{}) bool {
			stats.TotalRegistered++
			return true
		})
		return true
	})

	m.statsMu.RLock()
	stats.LastCleanupTime = m.lastCleanupTime
	m.statsMu.RUnlock()

	return stats
}

// ============================================================================
// ConfigManager interface implementation
// ============================================================================

// GetConfig returns any available config (for compatibility).
func (m *NacosOnDemandConfigManager) GetConfig(ctx context.Context) (*model.AgentConfig, error) {
	var firstConfig *model.AgentConfig

	m.configCache.Range(func(_, value interface{}) bool {
		entry := value.(*AgentConfigEntry)
		if entry.Config != nil {
			firstConfig = entry.Config
			return false
		}
		return true
	})

	if firstConfig != nil {
		return firstConfig, nil
	}

	return nil, errors.New("no config available")
}

// UpdateConfig updates config (for compatibility).
func (m *NacosOnDemandConfigManager) UpdateConfig(ctx context.Context, config *model.AgentConfig) error {
	// Find first token and update default config
	var firstToken string

	m.registeredAgents.Range(func(token, _ interface{}) bool {
		firstToken = token.(string)
		return false
	})

	if firstToken == "" {
		return errors.New("no token available for update")
	}

	return errors.New("update config is no longer supported via this interface")
}

// Watch starts watching for config changes (ConfigManager interface).
func (m *NacosOnDemandConfigManager) Watch(ctx context.Context, callback ConfigChangeCallback) error {
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, callback)
	m.subMu.Unlock()
	return nil
}

// StopWatch stops watching (ConfigManager interface).
func (m *NacosOnDemandConfigManager) StopWatch() error {
	// Cancel all watches
	m.configCache.Range(func(key, value interface{}) bool {
		entry := value.(*AgentConfigEntry)
		if entry.IsWatching {
			m.cancelWatch(entry.Token, entry.AgentID)
		}
		return true
	})

	return nil
}

// Subscribe registers a callback (ConfigManager interface).
func (m *NacosOnDemandConfigManager) Subscribe(callback ConfigChangeCallback) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	m.subscribers = append(m.subscribers, callback)
}

// Close releases resources.
func (m *NacosOnDemandConfigManager) Close() error {
	if !m.started.Swap(false) {
		return nil
	}

	// Signal stop
	close(m.stopCh)

	// Wait for goroutines
	m.wg.Wait()

	// Stop all watches
	_ = m.StopWatch()

	m.logger.Info("On-demand config manager stopped")
	return nil
}

// validateConfig validates config.
func (m *NacosOnDemandConfigManager) validateConfig(config *model.AgentConfig) error {
	if config.Sampler != nil {
		if config.Sampler.Type == model.SamplerTypeTraceIDRatio {
			if config.Sampler.Ratio < 0 || config.Sampler.Ratio > 1 {
				return errors.New("sampler ratio must be between 0 and 1")
			}
		}
	}

	return nil
}
