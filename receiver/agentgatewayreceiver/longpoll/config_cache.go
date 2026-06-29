// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
)

// configCache encapsulates the configuration lifecycle for a single service:
// loading from the config manager, caching, and change watching.
//
// All methods are safe for concurrent use. The onChange callback is called
// whenever a config change is detected via the watch, enabling the handler
// to notify waiters without configCache needing to know about them.
type configCache struct {
	mu          sync.RWMutex
	appID       string
	serviceName string
	config      *model.AgentConfig
	isWatching  bool
	configMgr   configmanager.OnDemandConfigManager
	onChange    func(newConfig *model.AgentConfig) // set by handler
	logger      *zap.Logger
}

// newConfigCache creates a new configCache for a service.
func newConfigCache(appID, serviceName string, configMgr configmanager.OnDemandConfigManager, logger *zap.Logger) *configCache {
	return &configCache{
		appID:       appID,
		serviceName: serviceName,
		configMgr:   configMgr,
		logger:      logger,
	}
}

// Get returns the cached config, or nil if never loaded.
func (c *configCache) Get() *model.AgentConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}

// Set stores a new config value.
func (c *configCache) Set(config *model.AgentConfig) {
	c.mu.Lock()
	c.config = config
	c.mu.Unlock()
}

// SetOnChange registers a callback for config change notifications.
// Called from ConfigPollHandler.getOrCreateServiceState to wire waiter notification.
func (c *configCache) SetOnChange(fn func(newConfig *model.AgentConfig)) {
	c.mu.Lock()
	c.onChange = fn
	c.mu.Unlock()
}

// LoadFromConfigMgr fetches the current config from the config manager.
// Returns nil, nil if no config exists for this service (e.g., Nacos returns 404).
func (c *configCache) LoadFromConfigMgr(ctx context.Context) (*model.AgentConfig, error) {
	if c.serviceName == "" {
		return nil, nil
	}
	cfg, err := c.configMgr.GetServiceConfig(ctx, c.appID, c.serviceName)
	if err != nil {
		if configmanager.IsConfigNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if cfg != nil && cfg.Etag == "" {
		cfg.Etag = computeBusinessEtagFromModel(cfg)
	}
	return cfg, nil
}

// EnsureWatching starts watching for config changes if not already watching.
// Safe to call multiple times — second call is a no-op.
func (c *configCache) EnsureWatching() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isWatching || c.serviceName == "" || c.configMgr == nil {
		return
	}

	// Capture for closure to avoid data race.
	appID := c.appID
	serviceName := c.serviceName
	onChange := c.onChange

	c.configMgr.WatchServiceConfig(appID, serviceName, func(event *configmanager.AgentConfigChangeEvent) {
		newConfig := event.NewConfig
		c.mu.Lock()
		c.config = newConfig
		c.mu.Unlock()

		if onChange != nil {
			onChange(newConfig)
		}
	})
	c.isWatching = true

	c.logger.Debug("Setup config watch via configMgr",
		zap.String("app_id", appID),
		zap.String("service", serviceName),
	)
}

// Unwatch stops watching for config changes for this service.
func (c *configCache) Unwatch() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isWatching || c.configMgr == nil {
		return
	}
	c.configMgr.UnwatchServiceConfig(c.appID, c.serviceName)
	c.isWatching = false
}

// IsWatching returns whether the service is currently being watched.
func (c *configCache) IsWatching() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isWatching
}
