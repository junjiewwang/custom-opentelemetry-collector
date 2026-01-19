// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// NacosConfigManager implements ConfigManager using Nacos as backend.
// Uses model.AgentConfig as the canonical type, stored as JSON in Nacos.
type NacosConfigManager struct {
	logger *zap.Logger
	config Config

	mu            sync.RWMutex
	client        config_client.IConfigClient
	currentConfig *model.AgentConfig
	subscribers   []ConfigChangeCallback
	watching      bool
	started       bool
}

// NewNacosConfigManager creates a new Nacos-based config manager.
func NewNacosConfigManager(logger *zap.Logger, config Config, client config_client.IConfigClient) (*NacosConfigManager, error) {
	return &NacosConfigManager{
		logger:      logger,
		config:      config,
		client:      client,
		subscribers: make([]ConfigChangeCallback, 0),
	}, nil
}

// Ensure NacosConfigManager implements ConfigManager.
var _ ConfigManager = (*NacosConfigManager)(nil)

// Start initializes the Nacos client.
func (m *NacosConfigManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting Nacos config manager",
		zap.String("data_id", m.config.DataId),
		zap.String("group", m.config.Group),
	)

	if m.client == nil {
		return errors.New("nacos client not provided")
	}

	m.started = true

	// Load initial config with timeout (non-blocking)
	go func() {
		loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := m.loadConfig(loadCtx); err != nil {
			m.logger.Warn("Failed to load initial config from Nacos", zap.Error(err))
		}
	}()

	return nil
}

// GetConfig returns the current configuration.
func (m *NacosConfigManager) GetConfig(ctx context.Context) (*model.AgentConfig, error) {
	m.mu.RLock()
	config := m.currentConfig
	m.mu.RUnlock()

	if config != nil {
		return config, nil
	}

	// Try to load from Nacos
	if err := m.loadConfig(ctx); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentConfig, nil
}

// UpdateConfig updates the configuration in Nacos.
func (m *NacosConfigManager) UpdateConfig(ctx context.Context, config *model.AgentConfig) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if err := m.validate(config); err != nil {
		return err
	}

	// Serialize config to JSON
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}

	// Write to Nacos
	if err := m.publishConfig(ctx, m.config.DataId, string(data)); err != nil {
		return err
	}

	// Update local cache
	m.mu.Lock()
	oldConfig := m.currentConfig
	m.currentConfig = config
	subs := make([]ConfigChangeCallback, len(m.subscribers))
	copy(subs, m.subscribers)
	m.mu.Unlock()

	// Notify subscribers
	for _, sub := range subs {
		sub(oldConfig, config)
	}

	m.logger.Info("Configuration published to Nacos", zap.String("version", config.Version.Version))
	return nil
}

// Watch starts watching for configuration changes.
func (m *NacosConfigManager) Watch(ctx context.Context, callback ConfigChangeCallback) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client == nil {
		return errors.New("nacos client not initialized")
	}

	// Add to subscribers
	m.subscribers = append(m.subscribers, callback)

	// Start watching if not already
	if !m.watching {
		err := m.client.ListenConfig(vo.ConfigParam{
			DataId: m.config.DataId,
			Group:  m.config.Group,
			OnChange: func(namespace, group, dataId, data string) {
				m.handleConfigChange(data)
			},
		})
		if err != nil {
			return err
		}
		m.watching = true
		m.logger.Info("Started watching Nacos config",
			zap.String("data_id", m.config.DataId),
			zap.String("group", m.config.Group),
		)
	}

	return nil
}

// StopWatch stops watching for configuration changes.
func (m *NacosConfigManager) StopWatch() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client == nil || !m.watching {
		return nil
	}

	err := m.client.CancelListenConfig(vo.ConfigParam{
		DataId: m.config.DataId,
		Group:  m.config.Group,
	})
	if err != nil {
		return err
	}

	m.watching = false
	m.logger.Info("Stopped watching Nacos config")

	return nil
}

// Subscribe registers a callback for configuration changes.
func (m *NacosConfigManager) Subscribe(callback ConfigChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers = append(m.subscribers, callback)
}

// Close releases resources.
func (m *NacosConfigManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	if m.client != nil && m.watching {
		_ = m.client.CancelListenConfig(vo.ConfigParam{DataId: m.config.DataId, Group: m.config.Group})
	}

	// Note: We don't close the Nacos client here because it's managed by the storage extension
	m.started = false
	m.watching = false
	m.subscribers = nil
	m.currentConfig = nil

	return nil
}

// loadConfig loads the configuration from Nacos.
func (m *NacosConfigManager) loadConfig(ctx context.Context) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return errors.New("nacos client not initialized")
	}

	// Use a channel to implement timeout for GetConfig
	type result struct {
		content string
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		content, err := client.GetConfig(vo.ConfigParam{
			DataId: m.config.DataId,
			Group:  m.config.Group,
		})
		resultCh <- result{content: content, err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		if res.content == "" {
			return nil // No config yet
		}

		var config model.AgentConfig
		if err := json.Unmarshal([]byte(res.content), &config); err != nil {
			return err
		}

		m.mu.Lock()
		m.currentConfig = &config
		m.mu.Unlock()

		m.logger.Info("Loaded config from Nacos",
			zap.String("version", config.Version.Version),
		)
	}

	return nil
}

// handleConfigChange handles configuration changes from Nacos.
func (m *NacosConfigManager) handleConfigChange(data string) {
	if data == "" {
		return
	}

	var newConfig model.AgentConfig
	if err := json.Unmarshal([]byte(data), &newConfig); err != nil {
		m.logger.Error("Failed to parse config from Nacos", zap.Error(err))
		return
	}

	m.mu.Lock()
	oldConfig := m.currentConfig
	m.currentConfig = &newConfig
	subscribers := make([]ConfigChangeCallback, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.mu.Unlock()

	m.logger.Info("Config changed in Nacos",
		zap.String("old_version", getConfigVersion(oldConfig)),
		zap.String("new_version", newConfig.Version.Version),
	)

	// Notify subscribers
	for _, sub := range subscribers {
		sub(oldConfig, &newConfig)
	}
}

// validate validates the configuration.
func (m *NacosConfigManager) validate(config *model.AgentConfig) error {
	if config.Version.Version == "" {
		return errors.New("version.version is required")
	}

	if config.Sampler != nil {
		if config.Sampler.Type == model.SamplerTypeTraceIDRatio {
			if config.Sampler.Ratio < 0 || config.Sampler.Ratio > 1 {
				return errors.New("sampler ratio must be between 0 and 1")
			}
		}
	}

	return nil
}

// getConfigVersion safely gets the config version.
func getConfigVersion(config *model.AgentConfig) string {
	if config == nil {
		return ""
	}
	return config.Version.Version
}

func (m *NacosConfigManager) publishConfig(ctx context.Context, dataID string, content string) error {
	m.mu.RLock()
	client := m.client
	group := m.config.Group
	m.mu.RUnlock()

	if client == nil {
		return errors.New("nacos client not initialized")
	}

	success, err := client.PublishConfig(vo.ConfigParam{
		DataId:  dataID,
		Group:   group,
		Content: content,
		Type:    "json",
	})
	if err != nil {
		return err
	}
	if !success {
		return errors.New("failed to publish config to Nacos")
	}
	return nil
}
