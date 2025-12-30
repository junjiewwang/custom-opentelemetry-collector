// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"errors"
	"sync"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// MemoryConfigManager implements ConfigManager using in-memory storage.
type MemoryConfigManager struct {
	logger *zap.Logger

	mu            sync.RWMutex
	currentConfig *controlplanev1.AgentConfig
	subscribers   []ConfigChangeCallback
	started       bool
}

// NewMemoryConfigManager creates a new in-memory config manager.
func NewMemoryConfigManager(logger *zap.Logger) *MemoryConfigManager {
	return &MemoryConfigManager{
		logger:      logger,
		subscribers: make([]ConfigChangeCallback, 0),
	}
}

// Ensure MemoryConfigManager implements ConfigManager.
var _ ConfigManager = (*MemoryConfigManager)(nil)

// GetConfig returns the current configuration.
func (m *MemoryConfigManager) GetConfig(ctx context.Context) (*controlplanev1.AgentConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentConfig, nil
}

// UpdateConfig updates the configuration.
func (m *MemoryConfigManager) UpdateConfig(ctx context.Context, config *controlplanev1.AgentConfig) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if err := m.validate(config); err != nil {
		return err
	}

	m.mu.Lock()
	oldConfig := m.currentConfig
	m.currentConfig = config
	subscribers := make([]ConfigChangeCallback, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.mu.Unlock()

	// Notify subscribers
	for _, sub := range subscribers {
		sub(oldConfig, config)
	}

	m.logger.Info("Configuration updated",
		zap.String("version", config.ConfigVersion),
	)

	return nil
}

// Watch is a no-op for memory implementation (no external source to watch).
func (m *MemoryConfigManager) Watch(ctx context.Context, callback ConfigChangeCallback) error {
	// Memory implementation doesn't have external source to watch
	// Just subscribe to local changes
	m.Subscribe(callback)
	return nil
}

// StopWatch is a no-op for memory implementation.
func (m *MemoryConfigManager) StopWatch() error {
	return nil
}

// Subscribe registers a callback for configuration changes.
func (m *MemoryConfigManager) Subscribe(callback ConfigChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers = append(m.subscribers, callback)
}

// Start initializes the config manager.
func (m *MemoryConfigManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting memory config manager")
	m.started = true
	return nil
}

// Close releases resources.
func (m *MemoryConfigManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.started = false
	m.subscribers = nil
	return nil
}

// validate validates the configuration.
func (m *MemoryConfigManager) validate(config *controlplanev1.AgentConfig) error {
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
