// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

const (
	// DefaultConfigDataId is the fallback config data ID within a group.
	DefaultConfigDataId = "_default_"

	// DefaultScanInterval is the default interval for scanning Nacos configs.
	DefaultScanInterval = 30 * time.Second

	// DefaultLoadTimeout is the default timeout for loading a single config.
	DefaultLoadTimeout = 5 * time.Second

	// DefaultMaxRetries is the default max retries for failed operations.
	DefaultMaxRetries = 3

	// DefaultRetryInterval is the default interval between retries.
	DefaultRetryInterval = 1 * time.Second
)

// MultiAgentConfig holds configuration for MultiAgentNacosConfigManager.
type MultiAgentConfig struct {
	// Namespace for Nacos (empty for default namespace).
	Namespace string `mapstructure:"namespace"`

	// Groups to scan (if empty, scans all groups).
	Groups []string `mapstructure:"groups"`

	// ScanInterval is the interval for periodic config scanning.
	ScanInterval time.Duration `mapstructure:"scan_interval"`

	// LoadTimeout is the timeout for loading a single config.
	LoadTimeout time.Duration `mapstructure:"load_timeout"`

	// MaxRetries is the max retries for failed operations.
	MaxRetries int `mapstructure:"max_retries"`

	// RetryInterval is the interval between retries.
	RetryInterval time.Duration `mapstructure:"retry_interval"`

	// EnableWatch enables Nacos config change watching.
	EnableWatch bool `mapstructure:"enable_watch"`
}

// DefaultMultiAgentConfig returns default configuration.
func DefaultMultiAgentConfig() MultiAgentConfig {
	return MultiAgentConfig{
		Namespace:     "",
		Groups:        nil,
		ScanInterval:  DefaultScanInterval,
		LoadTimeout:   DefaultLoadTimeout,
		MaxRetries:    DefaultMaxRetries,
		RetryInterval: DefaultRetryInterval,
		EnableWatch:   true,
	}
}

// cacheKey generates a cache key from group (token) and dataId (agentId).
func cacheKey(group, dataId string) string {
	return group + ":" + dataId
}

// parseCacheKey parses a cache key into group and dataId.
func parseCacheKey(key string) (group, dataId string) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}

// ConfigEntry represents a cached config entry with metadata.
type ConfigEntry struct {
	Config     *controlplanev1.AgentConfig `json:"config"`
	Group      string                      `json:"group"`       // Token
	DataId     string                      `json:"data_id"`     // AgentId
	LoadedAt   time.Time                   `json:"loaded_at"`
	Version    string                      `json:"version"`
	LoadError  error                       `json:"-"`
	RetryCount int                         `json:"-"`
}

// ConfigChangeEvent represents a config change event.
type ConfigChangeEvent struct {
	Type      string                      `json:"type"` // "created", "updated", "deleted"
	Group     string                      `json:"group"`
	DataId    string                      `json:"data_id"`
	OldConfig *controlplanev1.AgentConfig `json:"old_config,omitempty"`
	NewConfig *controlplanev1.AgentConfig `json:"new_config,omitempty"`
	Timestamp time.Time                   `json:"timestamp"`
}

// MultiAgentConfigChangeCallback is called when any agent config changes.
type MultiAgentConfigChangeCallback func(event *ConfigChangeEvent)

// MultiAgentConfigManager extends ConfigManager to support multiple agents.
type MultiAgentConfigManager interface {
	ConfigManager

	// GetConfigForAgent returns config for a specific agent with token validation.
	GetConfigForAgent(ctx context.Context, token, agentId string) (*controlplanev1.AgentConfig, error)

	// UpdateConfigForAgent updates config for a specific agent.
	UpdateConfigForAgent(ctx context.Context, token, agentId string, config *controlplanev1.AgentConfig) error

	// ValidateToken validates if the token (group) exists.
	ValidateToken(ctx context.Context, token string) (bool, error)

	// GetAgentsByToken returns all agent IDs under a token.
	GetAgentsByToken(ctx context.Context, token string) ([]string, error)

	// GetAllTokens returns all registered tokens.
	GetAllTokens(ctx context.Context) ([]string, error)

	// SubscribeMultiAgent subscribes to config changes for all agents.
	SubscribeMultiAgent(callback MultiAgentConfigChangeCallback)

	// ScanConfigs triggers a manual config scan.
	ScanConfigs(ctx context.Context) error

	// GetCacheStats returns cache statistics.
	GetCacheStats() *CacheStats
}

// CacheStats holds cache statistics.
type CacheStats struct {
	TotalEntries   int       `json:"total_entries"`
	TotalTokens    int       `json:"total_tokens"`
	TotalAgents    int       `json:"total_agents"`
	LastScanTime   time.Time `json:"last_scan_time"`
	LastScanError  string    `json:"last_scan_error,omitempty"`
	FailedEntries  int       `json:"failed_entries"`
	WatchingCount  int       `json:"watching_count"`
}

// MultiAgentNacosConfigManager implements MultiAgentConfigManager using Nacos.
type MultiAgentNacosConfigManager struct {
	logger *zap.Logger
	config MultiAgentConfig
	client config_client.IConfigClient

	// configCache stores configs by "group:dataId" key.
	configCache sync.Map // map[string]*ConfigEntry

	// tokenRegistry stores agent IDs by token (group).
	tokenRegistry sync.Map // map[string][]string

	// watchingConfigs tracks which configs are being watched.
	watchingConfigs sync.Map // map[string]bool

	// subscribers for multi-agent config changes.
	multiAgentSubscribers []MultiAgentConfigChangeCallback
	subscribersMu         sync.RWMutex

	// Legacy single-config subscribers (for ConfigManager interface).
	legacySubscribers []ConfigChangeCallback
	legacyMu          sync.RWMutex

	// State
	started       atomic.Bool
	stopCh        chan struct{}
	wg            sync.WaitGroup
	lastScanTime  time.Time
	lastScanError error
	statsMu       sync.RWMutex
}

// NewMultiAgentNacosConfigManager creates a new multi-agent Nacos config manager.
func NewMultiAgentNacosConfigManager(
	logger *zap.Logger,
	config MultiAgentConfig,
	client config_client.IConfigClient,
) (*MultiAgentNacosConfigManager, error) {
	if client == nil {
		return nil, errors.New("nacos client is required")
	}

	// Apply defaults
	if config.ScanInterval <= 0 {
		config.ScanInterval = DefaultScanInterval
	}
	if config.LoadTimeout <= 0 {
		config.LoadTimeout = DefaultLoadTimeout
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = DefaultMaxRetries
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = DefaultRetryInterval
	}

	return &MultiAgentNacosConfigManager{
		logger:                logger,
		config:                config,
		client:                client,
		multiAgentSubscribers: make([]MultiAgentConfigChangeCallback, 0),
		legacySubscribers:     make([]ConfigChangeCallback, 0),
		stopCh:                make(chan struct{}),
	}, nil
}

// Ensure MultiAgentNacosConfigManager implements the interfaces.
var (
	_ ConfigManager           = (*MultiAgentNacosConfigManager)(nil)
	_ MultiAgentConfigManager = (*MultiAgentNacosConfigManager)(nil)
)

// Start initializes the manager and starts background scanning.
func (m *MultiAgentNacosConfigManager) Start(ctx context.Context) error {
	if m.started.Swap(true) {
		return nil
	}

	m.logger.Info("Starting multi-agent Nacos config manager",
		zap.String("namespace", m.config.Namespace),
		zap.Strings("groups", m.config.Groups),
		zap.Duration("scan_interval", m.config.ScanInterval),
	)

	// Initial scan (non-blocking)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		scanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.ScanConfigs(scanCtx); err != nil {
			m.logger.Warn("Initial config scan failed", zap.Error(err))
		}
	}()

	// Start periodic scanner
	m.wg.Add(1)
	go m.runPeriodicScanner()

	return nil
}

// runPeriodicScanner runs periodic config scanning.
func (m *MultiAgentNacosConfigManager) runPeriodicScanner() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := m.ScanConfigs(ctx); err != nil {
				m.logger.Warn("Periodic config scan failed", zap.Error(err))
			}
			cancel()
		}
	}
}

// ScanConfigs scans all configs from Nacos.
func (m *MultiAgentNacosConfigManager) ScanConfigs(ctx context.Context) error {
	m.logger.Debug("Starting config scan")

	groups := m.config.Groups
	if len(groups) == 0 {
		// If no groups specified, we need to discover them
		// Note: Nacos doesn't have a direct API to list all groups,
		// so we rely on pre-configured groups or SearchConfig with pattern
		m.logger.Warn("No groups configured, skipping scan")
		return nil
	}

	var scanErrors []error
	scannedCount := 0

	for _, group := range groups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Search for all configs in this group
		configs, err := m.searchConfigsInGroup(ctx, group)
		if err != nil {
			m.logger.Warn("Failed to search configs in group",
				zap.String("group", group),
				zap.Error(err),
			)
			scanErrors = append(scanErrors, fmt.Errorf("group %s: %w", group, err))
			continue
		}

		// Load each config
		agentIds := make([]string, 0, len(configs))
		for _, cfg := range configs {
			if err := m.loadAndCacheConfig(ctx, group, cfg.DataId); err != nil {
				m.logger.Warn("Failed to load config",
					zap.String("group", group),
					zap.String("data_id", cfg.DataId),
					zap.Error(err),
				)
				scanErrors = append(scanErrors, err)
			} else {
				agentIds = append(agentIds, cfg.DataId)
				scannedCount++
			}

			// Setup watch if enabled
			if m.config.EnableWatch {
				m.setupWatch(group, cfg.DataId)
			}
		}

		// Update token registry
		m.tokenRegistry.Store(group, agentIds)
	}

	// Update stats
	m.statsMu.Lock()
	m.lastScanTime = time.Now()
	if len(scanErrors) > 0 {
		m.lastScanError = fmt.Errorf("scan completed with %d errors", len(scanErrors))
	} else {
		m.lastScanError = nil
	}
	m.statsMu.Unlock()

	m.logger.Info("Config scan completed",
		zap.Int("scanned", scannedCount),
		zap.Int("errors", len(scanErrors)),
	)

	if len(scanErrors) > 0 {
		return fmt.Errorf("scan completed with %d errors", len(scanErrors))
	}
	return nil
}

// searchConfigsInGroup searches for all configs in a group.
func (m *MultiAgentNacosConfigManager) searchConfigsInGroup(ctx context.Context, group string) ([]model.ConfigItem, error) {
	// Use SearchConfig to find all configs in the group
	type searchResult struct {
		items []model.ConfigItem
		err   error
	}
	resultCh := make(chan searchResult, 1)

	go func() {
		page, err := m.client.SearchConfig(vo.SearchConfigParam{
			Search:   "accurate",
			Group:    group,
			PageNo:   1,
			PageSize: 1000, // Adjust based on expected number of agents
		})
		if err != nil {
			resultCh <- searchResult{err: err}
			return
		}
		resultCh <- searchResult{items: page.PageItems}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resultCh:
		return res.items, res.err
	}
}

// loadAndCacheConfig loads a config and stores it in cache.
func (m *MultiAgentNacosConfigManager) loadAndCacheConfig(ctx context.Context, group, dataId string) error {
	key := cacheKey(group, dataId)

	// Load with timeout and retry
	var content string
	var err error

	for retry := 0; retry <= m.config.MaxRetries; retry++ {
		if retry > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.config.RetryInterval):
			}
		}

		content, err = m.loadConfigContent(ctx, group, dataId)
		if err == nil {
			break
		}

		m.logger.Debug("Config load retry",
			zap.String("group", group),
			zap.String("data_id", dataId),
			zap.Int("retry", retry),
			zap.Error(err),
		)
	}

	if err != nil {
		// Store error entry for tracking
		entry := &ConfigEntry{
			Group:      group,
			DataId:     dataId,
			LoadedAt:   time.Now(),
			LoadError:  err,
			RetryCount: m.config.MaxRetries,
		}
		m.configCache.Store(key, entry)
		return fmt.Errorf("failed to load config %s after %d retries: %w", key, m.config.MaxRetries, err)
	}

	// Parse config
	var config controlplanev1.AgentConfig
	if content != "" {
		if err := json.Unmarshal([]byte(content), &config); err != nil {
			return fmt.Errorf("failed to parse config %s: %w", key, err)
		}
	}

	// Check for changes
	var oldConfig *controlplanev1.AgentConfig
	var eventType string

	if existing, ok := m.configCache.Load(key); ok {
		oldEntry := existing.(*ConfigEntry)
		oldConfig = oldEntry.Config
		if oldConfig == nil && config.ConfigVersion != "" {
			eventType = "created"
		} else if oldConfig != nil && oldConfig.ConfigVersion != config.ConfigVersion {
			eventType = "updated"
		}
	} else {
		if config.ConfigVersion != "" {
			eventType = "created"
		}
	}

	// Store in cache
	entry := &ConfigEntry{
		Config:   &config,
		Group:    group,
		DataId:   dataId,
		LoadedAt: time.Now(),
		Version:  config.ConfigVersion,
	}
	m.configCache.Store(key, entry)

	// Notify subscribers if changed
	if eventType != "" {
		m.notifyMultiAgentSubscribers(&ConfigChangeEvent{
			Type:      eventType,
			Group:     group,
			DataId:    dataId,
			OldConfig: oldConfig,
			NewConfig: &config,
			Timestamp: time.Now(),
		})
	}

	return nil
}

// loadConfigContent loads config content from Nacos with timeout.
func (m *MultiAgentNacosConfigManager) loadConfigContent(ctx context.Context, group, dataId string) (string, error) {
	type result struct {
		content string
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		content, err := m.client.GetConfig(vo.ConfigParam{
			Group:  group,
			DataId: dataId,
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

// setupWatch sets up config change watching.
func (m *MultiAgentNacosConfigManager) setupWatch(group, dataId string) {
	key := cacheKey(group, dataId)

	// Check if already watching
	if _, ok := m.watchingConfigs.Load(key); ok {
		return
	}

	err := m.client.ListenConfig(vo.ConfigParam{
		Group:  group,
		DataId: dataId,
		OnChange: func(namespace, g, d, data string) {
			m.handleConfigChange(g, d, data)
		},
	})

	if err != nil {
		m.logger.Warn("Failed to setup config watch",
			zap.String("group", group),
			zap.String("data_id", dataId),
			zap.Error(err),
		)
		return
	}

	m.watchingConfigs.Store(key, true)
	m.logger.Debug("Setup config watch",
		zap.String("group", group),
		zap.String("data_id", dataId),
	)
}

// handleConfigChange handles config change from Nacos watch.
func (m *MultiAgentNacosConfigManager) handleConfigChange(group, dataId, data string) {
	key := cacheKey(group, dataId)

	m.logger.Info("Config changed",
		zap.String("group", group),
		zap.String("data_id", dataId),
	)

	// Get old config
	var oldConfig *controlplanev1.AgentConfig
	if existing, ok := m.configCache.Load(key); ok {
		oldEntry := existing.(*ConfigEntry)
		oldConfig = oldEntry.Config
	}

	// Parse new config
	var newConfig *controlplanev1.AgentConfig
	var eventType string

	if data == "" {
		eventType = "deleted"
		// Remove from cache
		m.configCache.Delete(key)
	} else {
		var config controlplanev1.AgentConfig
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			m.logger.Error("Failed to parse changed config",
				zap.String("group", group),
				zap.String("data_id", dataId),
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
		entry := &ConfigEntry{
			Config:   newConfig,
			Group:    group,
			DataId:   dataId,
			LoadedAt: time.Now(),
			Version:  config.ConfigVersion,
		}
		m.configCache.Store(key, entry)
	}

	// Notify subscribers
	m.notifyMultiAgentSubscribers(&ConfigChangeEvent{
		Type:      eventType,
		Group:     group,
		DataId:    dataId,
		OldConfig: oldConfig,
		NewConfig: newConfig,
		Timestamp: time.Now(),
	})

	// Also notify legacy subscribers with the new config
	if newConfig != nil {
		m.notifyLegacySubscribers(oldConfig, newConfig)
	}
}

// notifyMultiAgentSubscribers notifies all multi-agent subscribers.
func (m *MultiAgentNacosConfigManager) notifyMultiAgentSubscribers(event *ConfigChangeEvent) {
	m.subscribersMu.RLock()
	subscribers := make([]MultiAgentConfigChangeCallback, len(m.multiAgentSubscribers))
	copy(subscribers, m.multiAgentSubscribers)
	m.subscribersMu.RUnlock()

	for _, sub := range subscribers {
		sub(event)
	}
}

// notifyLegacySubscribers notifies legacy ConfigManager subscribers.
func (m *MultiAgentNacosConfigManager) notifyLegacySubscribers(oldConfig, newConfig *controlplanev1.AgentConfig) {
	m.legacyMu.RLock()
	subscribers := make([]ConfigChangeCallback, len(m.legacySubscribers))
	copy(subscribers, m.legacySubscribers)
	m.legacyMu.RUnlock()

	for _, sub := range subscribers {
		sub(oldConfig, newConfig)
	}
}

// GetConfigForAgent returns config for a specific agent with token validation.
func (m *MultiAgentNacosConfigManager) GetConfigForAgent(ctx context.Context, token, agentId string) (*controlplanev1.AgentConfig, error) {
	// First try exact match
	key := cacheKey(token, agentId)
	if entry, ok := m.configCache.Load(key); ok {
		e := entry.(*ConfigEntry)
		if e.LoadError != nil {
			return nil, fmt.Errorf("config load error: %w", e.LoadError)
		}
		if e.Config != nil {
			return e.Config, nil
		}
	}

	// Try default config for the token/group
	defaultKey := cacheKey(token, DefaultConfigDataId)
	if entry, ok := m.configCache.Load(defaultKey); ok {
		e := entry.(*ConfigEntry)
		if e.LoadError != nil {
			return nil, fmt.Errorf("default config load error: %w", e.LoadError)
		}
		if e.Config != nil {
			return e.Config, nil
		}
	}

	// Try to load from Nacos directly
	content, err := m.loadConfigContent(ctx, token, agentId)
	if err != nil {
		// Try default
		content, err = m.loadConfigContent(ctx, token, DefaultConfigDataId)
		if err != nil {
			return nil, fmt.Errorf("config not found for agent %s with token %s", agentId, token)
		}
	}

	if content == "" {
		return nil, nil
	}

	var config controlplanev1.AgentConfig
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// UpdateConfigForAgent updates config for a specific agent.
func (m *MultiAgentNacosConfigManager) UpdateConfigForAgent(ctx context.Context, token, agentId string, config *controlplanev1.AgentConfig) error {
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
			DataId:  agentId,
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
	key := cacheKey(token, agentId)
	entry := &ConfigEntry{
		Config:   config,
		Group:    token,
		DataId:   agentId,
		LoadedAt: time.Now(),
		Version:  config.ConfigVersion,
	}
	m.configCache.Store(key, entry)

	m.logger.Info("Config updated for agent",
		zap.String("token", token),
		zap.String("agent_id", agentId),
		zap.String("version", config.ConfigVersion),
	)

	return nil
}

// ValidateToken validates if the token (group) exists.
func (m *MultiAgentNacosConfigManager) ValidateToken(ctx context.Context, token string) (bool, error) {
	// Check in registry first
	if _, ok := m.tokenRegistry.Load(token); ok {
		return true, nil
	}

	// Check in configured groups
	for _, g := range m.config.Groups {
		if g == token {
			return true, nil
		}
	}

	// Try to search in Nacos
	configs, err := m.searchConfigsInGroup(ctx, token)
	if err != nil {
		return false, err
	}

	return len(configs) > 0, nil
}

// GetAgentsByToken returns all agent IDs under a token.
func (m *MultiAgentNacosConfigManager) GetAgentsByToken(ctx context.Context, token string) ([]string, error) {
	if agents, ok := m.tokenRegistry.Load(token); ok {
		return agents.([]string), nil
	}

	// Search in Nacos
	configs, err := m.searchConfigsInGroup(ctx, token)
	if err != nil {
		return nil, err
	}

	agents := make([]string, 0, len(configs))
	for _, cfg := range configs {
		agents = append(agents, cfg.DataId)
	}

	return agents, nil
}

// GetAllTokens returns all registered tokens.
func (m *MultiAgentNacosConfigManager) GetAllTokens(ctx context.Context) ([]string, error) {
	tokens := make([]string, 0)

	m.tokenRegistry.Range(func(key, value interface{}) bool {
		tokens = append(tokens, key.(string))
		return true
	})

	return tokens, nil
}

// SubscribeMultiAgent subscribes to config changes for all agents.
func (m *MultiAgentNacosConfigManager) SubscribeMultiAgent(callback MultiAgentConfigChangeCallback) {
	m.subscribersMu.Lock()
	defer m.subscribersMu.Unlock()
	m.multiAgentSubscribers = append(m.multiAgentSubscribers, callback)
}

// GetCacheStats returns cache statistics.
func (m *MultiAgentNacosConfigManager) GetCacheStats() *CacheStats {
	stats := &CacheStats{}

	// Count entries
	var failedCount int
	m.configCache.Range(func(key, value interface{}) bool {
		stats.TotalEntries++
		entry := value.(*ConfigEntry)
		if entry.LoadError != nil {
			failedCount++
		}
		return true
	})
	stats.FailedEntries = failedCount

	// Count tokens and agents
	m.tokenRegistry.Range(func(key, value interface{}) bool {
		stats.TotalTokens++
		agents := value.([]string)
		stats.TotalAgents += len(agents)
		return true
	})

	// Count watching
	m.watchingConfigs.Range(func(key, value interface{}) bool {
		stats.WatchingCount++
		return true
	})

	// Last scan info
	m.statsMu.RLock()
	stats.LastScanTime = m.lastScanTime
	if m.lastScanError != nil {
		stats.LastScanError = m.lastScanError.Error()
	}
	m.statsMu.RUnlock()

	return stats
}

// ============================================================================
// ConfigManager interface implementation (for backward compatibility)
// ============================================================================

// GetConfig returns the first available config (for legacy compatibility).
func (m *MultiAgentNacosConfigManager) GetConfig(ctx context.Context) (*controlplanev1.AgentConfig, error) {
	var firstConfig *controlplanev1.AgentConfig

	m.configCache.Range(func(key, value interface{}) bool {
		entry := value.(*ConfigEntry)
		if entry.Config != nil && entry.LoadError == nil {
			firstConfig = entry.Config
			return false // Stop iteration
		}
		return true
	})

	if firstConfig != nil {
		return firstConfig, nil
	}

	return nil, errors.New("no config available")
}

// UpdateConfig updates the first available config (for legacy compatibility).
func (m *MultiAgentNacosConfigManager) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	// Find first token and update default config
	var firstToken string

	m.tokenRegistry.Range(func(key, value interface{}) bool {
		firstToken = key.(string)
		return false
	})

	if firstToken == "" && len(m.config.Groups) > 0 {
		firstToken = m.config.Groups[0]
	}

	if firstToken == "" {
		return errors.New("no token available for update")
	}

	return m.UpdateConfigForAgent(ctx, firstToken, DefaultConfigDataId, config)
}

// Watch starts watching for config changes (legacy interface).
func (m *MultiAgentNacosConfigManager) Watch(ctx context.Context, callback ConfigChangeCallback) error {
	m.legacyMu.Lock()
	m.legacySubscribers = append(m.legacySubscribers, callback)
	m.legacyMu.Unlock()
	return nil
}

// StopWatch stops watching (legacy interface).
func (m *MultiAgentNacosConfigManager) StopWatch() error {
	// Cancel all watches
	m.watchingConfigs.Range(func(key, value interface{}) bool {
		group, dataId := parseCacheKey(key.(string))
		_ = m.client.CancelListenConfig(vo.ConfigParam{
			Group:  group,
			DataId: dataId,
		})
		return true
	})

	// Clear watching map
	m.watchingConfigs = sync.Map{}

	return nil
}

// Subscribe registers a callback (legacy interface).
func (m *MultiAgentNacosConfigManager) Subscribe(callback ConfigChangeCallback) {
	m.legacyMu.Lock()
	defer m.legacyMu.Unlock()
	m.legacySubscribers = append(m.legacySubscribers, callback)
}

// Close releases resources.
func (m *MultiAgentNacosConfigManager) Close() error {
	if !m.started.Swap(false) {
		return nil
	}

	// Signal stop
	close(m.stopCh)

	// Wait for goroutines
	m.wg.Wait()

	// Stop all watches
	_ = m.StopWatch()

	m.logger.Info("Multi-agent Nacos config manager stopped")
	return nil
}

// validateConfig validates config.
func (m *MultiAgentNacosConfigManager) validateConfig(config *controlplanev1.AgentConfig) error {
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
