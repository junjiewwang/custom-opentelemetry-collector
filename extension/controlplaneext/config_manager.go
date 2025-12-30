// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"errors"
	"sync"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ConfigSubscriber is a callback function for config changes.
type ConfigSubscriber func(config *controlplanev1.AgentConfig)

// ConfigManager manages agent configuration.
type ConfigManager struct {
	logger *zap.Logger

	mu            sync.RWMutex
	currentConfig *controlplanev1.AgentConfig
	subscribers   []ConfigSubscriber
}

// newConfigManager creates a new config manager.
func newConfigManager(logger *zap.Logger) *ConfigManager {
	return &ConfigManager{
		logger:      logger,
		subscribers: make([]ConfigSubscriber, 0),
	}
}

// UpdateConfig updates the current configuration.
func (m *ConfigManager) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if err := m.validate(config); err != nil {
		return err
	}

	m.mu.Lock()
	m.currentConfig = config
	subscribers := make([]ConfigSubscriber, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.mu.Unlock()

	// Notify subscribers
	for _, sub := range subscribers {
		sub(config)
	}

	m.logger.Info("Configuration updated",
		zap.String("version", config.ConfigVersion),
	)

	return nil
}

// GetCurrentConfig returns the current configuration.
func (m *ConfigManager) GetCurrentConfig() *controlplanev1.AgentConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentConfig
}

// Subscribe registers a callback for configuration changes.
func (m *ConfigManager) Subscribe(subscriber ConfigSubscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers = append(m.subscribers, subscriber)
}

// validate validates the configuration.
func (m *ConfigManager) validate(config *controlplanev1.AgentConfig) error {
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

	if config.Batch != nil {
		if config.Batch.MaxExportBatchSize < 0 {
			return errors.New("batch max_export_batch_size must be non-negative")
		}
		if config.Batch.MaxQueueSize < 0 {
			return errors.New("batch max_queue_size must be non-negative")
		}
	}

	return nil
}
