// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
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
	Config      *controlplanev1.AgentConfig
	Token       string
	AgentID     string
	LoadedAt    time.Time
	LastAccess  time.Time
	Version     string
	LoadError   error
	IsWatching  bool
	IsDefault   bool // True if this is the default config for the token
}

// AgentConfigChangeEvent represents a config change event for an agent.
type AgentConfigChangeEvent struct {
	Type      string // "created", "updated", "deleted"
	Token     string
	AgentID   string
	OldConfig *controlplanev1.AgentConfig
	NewConfig *controlplanev1.AgentConfig
	Timestamp time.Time
}

// AgentConfigChangeCallback is called when an agent's config changes.
type AgentConfigChangeCallback func(event *AgentConfigChangeEvent)

// OnDemandConfigManager implements on-demand config loading.
// Configs are loaded when agents connect and released when they disconnect.
type OnDemandConfigManager interface {
	ConfigManager

	// RegisterAgent registers an agent and starts watching its config.
	// Returns the agent's config (or nil if not found, agent should use default).
	RegisterAgent(ctx context.Context, token, agentID string) (*controlplanev1.AgentConfig, error)

	// UnregisterAgent unregisters an agent and releases its resources.
	UnregisterAgent(ctx context.Context, token, agentID string) error

	// GetConfigForAgent returns config for a specific agent.
	// If no specific config exists, returns the default config for the token.
	// If no default config exists, returns nil (agent should use local default).
	GetConfigForAgent(ctx context.Context, token, agentID string) (*controlplanev1.AgentConfig, error)

	// SetConfigForAgent sets/updates config for a specific agent.
	SetConfigForAgent(ctx context.Context, token, agentID string, config *controlplanev1.AgentConfig) error

	// SetDefaultConfig sets the default config for a token (all agents under this token).
	SetDefaultConfig(ctx context.Context, token string, config *controlplanev1.AgentConfig) error

	// GetDefaultConfig returns the default config for a token.
	GetDefaultConfig(ctx context.Context, token string) (*controlplanev1.AgentConfig, error)

	// DeleteConfigForAgent deletes config for a specific agent.
	DeleteConfigForAgent(ctx context.Context, token, agentID string) error

	// SubscribeAgentConfig subscribes to config changes for a specific agent.
	SubscribeAgentConfig(token, agentID string, callback AgentConfigChangeCallback)

	// UnsubscribeAgentConfig unsubscribes from config changes.
	UnsubscribeAgentConfig(token, agentID string)

	// GetRegisteredAgents returns all registered agents.
	GetRegisteredAgents() map[string][]string // token -> []agentID

	// GetCacheStats returns cache statistics.
	GetCacheStats() *OnDemandCacheStats
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

	// Legacy subscribers for ConfigManager interface.
	legacySubscribers []ConfigChangeCallback
	legacyMu          sync.RWMutex

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
		logger:            logger,
		config:            config,
		client:            client,
		legacySubscribers: make([]ConfigChangeCallback, 0),
		stopCh:            make(chan struct{}),
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

// RegisterAgent registers an agent and starts watching its config.
func (m *NacosOnDemandConfigManager) RegisterAgent(ctx context.Context, token, agentID string) (*controlplanev1.AgentConfig, error) {
	if token == "" || agentID == "" {
		return nil, errors.New("token and agentID are required")
	}

	m.logger.Debug("Registering agent",
		zap.String("token", token),
		zap.String("agent_id", agentID),
	)

	// Add to registered agents
	agents, _ := m.registeredAgents.LoadOrStore(token, &sync.Map{})
	agentMap := agents.(*sync.Map)
	agentMap.Store(agentID, true)

	// Try to load agent-specific config
	config, err := m.loadConfig(ctx, token, agentID)
	if err != nil {
		m.logger.Debug("No agent-specific config, trying default",
			zap.String("token", token),
			zap.String("agent_id", agentID),
			zap.Error(err),
		)

		// Try default config
		config, err = m.loadConfig(ctx, token, DefaultConfigDataId)
		if err != nil {
			m.logger.Debug("No default config available",
				zap.String("token", token),
				zap.Error(err),
			)
			// No config available, agent should use local default
			return nil, nil
		}
	}

	// Setup watch for agent-specific config
	m.setupWatch(token, agentID)

	// Also watch default config
	m.setupWatch(token, DefaultConfigDataId)

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
			// Cancel default config watch if no agents left
			m.cancelWatch(token, DefaultConfigDataId)
		}
	}

	// Cancel agent-specific watch
	m.cancelWatch(token, agentID)

	// Remove subscribers
	m.agentSubscribers.Delete(m.cacheKey(token, agentID))

	// Note: We don't immediately delete from cache, let cleanup handle it
	// This allows for brief reconnections without reloading

	return nil
}

// GetConfigForAgent returns config for a specific agent.
func (m *NacosOnDemandConfigManager) GetConfigForAgent(ctx context.Context, token, agentID string) (*controlplanev1.AgentConfig, error) {
	if token == "" || agentID == "" {
		return nil, errors.New("token and agentID are required")
	}

	// Try agent-specific config from cache
	key := m.cacheKey(token, agentID)
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		e.LastAccess = time.Now()
		m.cacheHits.Add(1)
		if e.Config != nil {
			return e.Config, nil
		}
	}

	// Try default config from cache
	defaultKey := m.cacheKey(token, DefaultConfigDataId)
	if entry, ok := m.configCache.Load(defaultKey); ok {
		e := entry.(*AgentConfigEntry)
		e.LastAccess = time.Now()
		m.cacheHits.Add(1)
		if e.Config != nil {
			return e.Config, nil
		}
	}

	m.cacheMisses.Add(1)

	// Load from Nacos
	config, err := m.loadConfig(ctx, token, agentID)
	if err != nil {
		// Try default
		config, err = m.loadConfig(ctx, token, DefaultConfigDataId)
		if err != nil {
			return nil, nil // No config, use local default
		}
	}

	return config, nil
}

// loadConfig loads config from Nacos with caching.
func (m *NacosOnDemandConfigManager) loadConfig(ctx context.Context, token, dataID string) (*controlplanev1.AgentConfig, error) {
	key := m.cacheKey(token, dataID)

	// Load with timeout and retry
	var content string
	var err error

	for retry := 0; retry <= m.config.MaxRetries; retry++ {
		if retry > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(m.config.RetryInterval):
			}
		}

		content, err = m.loadConfigContent(ctx, token, dataID)
		if err == nil {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load config %s: %w", key, err)
	}

	if content == "" {
		return nil, errors.New("config not found")
	}

	// Parse config
	var config controlplanev1.AgentConfig
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Cache it
	entry := &AgentConfigEntry{
		Config:     &config,
		Token:      token,
		AgentID:    dataID,
		LoadedAt:   time.Now(),
		LastAccess: time.Now(),
		Version:    config.ConfigVersion,
		IsDefault:  dataID == DefaultConfigDataId,
	}
	m.configCache.Store(key, entry)

	m.logger.Debug("Config loaded and cached",
		zap.String("token", token),
		zap.String("data_id", dataID),
		zap.String("version", config.ConfigVersion),
	)

	return &config, nil
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

// SetConfigForAgent sets/updates config for a specific agent.
func (m *NacosOnDemandConfigManager) SetConfigForAgent(ctx context.Context, token, agentID string, config *controlplanev1.AgentConfig) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if err := m.validateConfig(config); err != nil {
		return err
	}

	// Serialize config
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}

	// Publish to Nacos
	type result struct {
		success bool
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		success, err := m.client.PublishConfig(vo.ConfigParam{
			Group:   token,
			DataId:  agentID,
			Content: string(data),
			Type:    "json",
		})
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
			return errors.New("failed to publish config to Nacos")
		}
	}

	// Update cache
	key := m.cacheKey(token, agentID)
	entry := &AgentConfigEntry{
		Config:     config,
		Token:      token,
		AgentID:    agentID,
		LoadedAt:   time.Now(),
		LastAccess: time.Now(),
		Version:    config.ConfigVersion,
		IsDefault:  agentID == DefaultConfigDataId,
	}
	m.configCache.Store(key, entry)

	m.logger.Info("Config set for agent",
		zap.String("token", token),
		zap.String("agent_id", agentID),
		zap.String("version", config.ConfigVersion),
	)

	return nil
}

// SetDefaultConfig sets the default config for a token.
func (m *NacosOnDemandConfigManager) SetDefaultConfig(ctx context.Context, token string, config *controlplanev1.AgentConfig) error {
	return m.SetConfigForAgent(ctx, token, DefaultConfigDataId, config)
}

// GetDefaultConfig returns the default config for a token.
func (m *NacosOnDemandConfigManager) GetDefaultConfig(ctx context.Context, token string) (*controlplanev1.AgentConfig, error) {
	return m.GetConfigForAgent(ctx, token, DefaultConfigDataId)
}

// DeleteConfigForAgent deletes config for a specific agent.
func (m *NacosOnDemandConfigManager) DeleteConfigForAgent(ctx context.Context, token, agentID string) error {
	// Delete from Nacos
	type result struct {
		success bool
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		success, err := m.client.DeleteConfig(vo.ConfigParam{
			Group:  token,
			DataId: agentID,
		})
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

	// Remove from cache
	key := m.cacheKey(token, agentID)
	m.configCache.Delete(key)

	m.logger.Info("Config deleted for agent",
		zap.String("token", token),
		zap.String("agent_id", agentID),
	)

	return nil
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
	var oldConfig *controlplanev1.AgentConfig
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*AgentConfigEntry)
		oldConfig = e.Config
	}

	// Parse new config
	var newConfig *controlplanev1.AgentConfig
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
		var config controlplanev1.AgentConfig
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
			e.Version = config.ConfigVersion
			e.LastAccess = time.Now()
			e.LoadedAt = time.Now()
		} else {
			m.configCache.Store(key, &AgentConfigEntry{
				Config:     newConfig,
				Token:      token,
				AgentID:    dataID,
				LoadedAt:   time.Now(),
				LastAccess: time.Now(),
				Version:    config.ConfigVersion,
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
		Timestamp: time.Now(),
	}

	// Notify agent-specific subscribers
	m.notifyAgentSubscribers(token, dataID, event)

	// If this is a default config change, notify all agents under this token
	if dataID == DefaultConfigDataId {
		m.notifyAllAgentsForToken(token, event)
	}

	// Notify legacy subscribers
	if newConfig != nil {
		m.notifyLegacySubscribers(oldConfig, newConfig)
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

// notifyLegacySubscribers notifies legacy ConfigManager subscribers.
func (m *NacosOnDemandConfigManager) notifyLegacySubscribers(oldConfig, newConfig *controlplanev1.AgentConfig) {
	m.legacyMu.RLock()
	subscribers := make([]ConfigChangeCallback, len(m.legacySubscribers))
	copy(subscribers, m.legacySubscribers)
	m.legacyMu.RUnlock()

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

// GetConfig returns any available config (for legacy compatibility).
func (m *NacosOnDemandConfigManager) GetConfig(ctx context.Context) (*controlplanev1.AgentConfig, error) {
	var firstConfig *controlplanev1.AgentConfig

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

// UpdateConfig updates config (for legacy compatibility).
func (m *NacosOnDemandConfigManager) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	// Find first token and update default config
	var firstToken string

	m.registeredAgents.Range(func(token, _ interface{}) bool {
		firstToken = token.(string)
		return false
	})

	if firstToken == "" {
		return errors.New("no token available for update")
	}

	return m.SetDefaultConfig(ctx, firstToken, config)
}

// Watch starts watching for config changes (legacy interface).
func (m *NacosOnDemandConfigManager) Watch(ctx context.Context, callback ConfigChangeCallback) error {
	m.legacyMu.Lock()
	m.legacySubscribers = append(m.legacySubscribers, callback)
	m.legacyMu.Unlock()
	return nil
}

// StopWatch stops watching (legacy interface).
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

// Subscribe registers a callback (legacy interface).
func (m *NacosOnDemandConfigManager) Subscribe(callback ConfigChangeCallback) {
	m.legacyMu.Lock()
	defer m.legacyMu.Unlock()
	m.legacySubscribers = append(m.legacySubscribers, callback)
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
func (m *NacosOnDemandConfigManager) validateConfig(config *controlplanev1.AgentConfig) error {
	if config.ConfigVersion == "" {
		return errors.New("config_version is required")
	}

	if config.Sampler != nil {
		if config.Sampler.Type == controlplanev1.SamplerTypeTraceIDRatio {
			if config.Sampler.Ratio < 0 || config.Sampler.Ratio > 1 {
				return errors.New("sampler ratio must be between 0 and 1")
			}
		}
	}

	return nil
}
