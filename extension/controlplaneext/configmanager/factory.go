// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"fmt"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"go.uber.org/zap"
)

// NewConfigManager creates a ConfigManager based on the configuration.
func NewConfigManager(logger *zap.Logger, config Config, nacosClient config_client.IConfigClient) (ConfigManager, error) {
	switch config.Type {
	case "memory":
		return NewMemoryConfigManager(logger), nil

	case "nacos":
		if nacosClient == nil {
			return nil, fmt.Errorf("nacos client is required for nacos config manager")
		}
		return NewNacosConfigManager(logger, config, nacosClient)

	case "multi_agent_nacos":
		// Deprecated: multi_agent_nacos is deprecated, redirect to on_demand
		logger.Warn("multi_agent_nacos config type is deprecated, using on_demand instead")
		fallthrough

	case "on_demand":
		if nacosClient == nil {
			return nil, fmt.Errorf("nacos client is required for on-demand config manager")
		}
		onDemandConfig, err := parseOnDemandConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse on-demand config: %w", err)
		}
		return NewNacosOnDemandConfigManager(logger, onDemandConfig, nil, nacosClient)

	default:
		return nil, fmt.Errorf("unknown config manager type: %s", config.Type)
	}
}

// IsMultiAgentMode checks if the config is for multi-agent mode.
// Deprecated: Use IsOnDemandMode instead.
func IsMultiAgentMode(config Config) bool {
	return config.Type == "multi_agent_nacos" || config.Type == "on_demand" || config.MultiAgent.Enabled
}

// NewOnDemandConfigManager creates an OnDemandConfigManager.
func NewOnDemandConfigManager(logger *zap.Logger, config Config, nacosClient config_client.IConfigClient) (OnDemandConfigManager, error) {
	if config.Type != "on_demand" && config.Type != "multi_agent_nacos" {
		return nil, fmt.Errorf("config type %s does not support on-demand mode", config.Type)
	}

	if nacosClient == nil {
		return nil, fmt.Errorf("nacos client is required for on-demand config manager")
	}

	onDemandConfig, err := parseOnDemandConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse on-demand config: %w", err)
	}

	return NewNacosOnDemandConfigManager(logger, onDemandConfig, nil, nacosClient)
}

// parseOnDemandConfig parses OnDemandConfig from Config.
func parseOnDemandConfig(config Config) (OnDemandConfig, error) {
	onDemandConfig := DefaultOnDemandConfig()

	if config.OnDemand.Namespace != "" {
		onDemandConfig.Namespace = config.OnDemand.Namespace
	}

	if config.OnDemand.LoadTimeout != "" {
		d, err := time.ParseDuration(config.OnDemand.LoadTimeout)
		if err != nil {
			return onDemandConfig, fmt.Errorf("invalid load_timeout: %w", err)
		}
		onDemandConfig.LoadTimeout = d
	}

	if config.OnDemand.MaxRetries > 0 {
		onDemandConfig.MaxRetries = config.OnDemand.MaxRetries
	}

	if config.OnDemand.RetryInterval != "" {
		d, err := time.ParseDuration(config.OnDemand.RetryInterval)
		if err != nil {
			return onDemandConfig, fmt.Errorf("invalid retry_interval: %w", err)
		}
		onDemandConfig.RetryInterval = d
	}

	if config.OnDemand.CacheExpiration != "" {
		d, err := time.ParseDuration(config.OnDemand.CacheExpiration)
		if err != nil {
			return onDemandConfig, fmt.Errorf("invalid cache_expiration: %w", err)
		}
		onDemandConfig.CacheExpiration = d
	}

	if config.OnDemand.CleanupInterval != "" {
		d, err := time.ParseDuration(config.OnDemand.CleanupInterval)
		if err != nil {
			return onDemandConfig, fmt.Errorf("invalid cleanup_interval: %w", err)
		}
		onDemandConfig.CleanupInterval = d
	}

	return onDemandConfig, nil
}

// IsOnDemandMode checks if the config is for on-demand mode.
func IsOnDemandMode(config Config) bool {
	return config.Type == "on_demand" || config.Type == "multi_agent_nacos"
}
