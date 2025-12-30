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
		if nacosClient == nil {
			return nil, fmt.Errorf("nacos client is required for multi-agent nacos config manager")
		}
		multiConfig, err := parseMultiAgentConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse multi-agent config: %w", err)
		}
		return NewMultiAgentNacosConfigManager(logger, multiConfig, nacosClient)

	case "on_demand":
		if nacosClient == nil {
			return nil, fmt.Errorf("nacos client is required for on-demand config manager")
		}
		onDemandConfig, err := parseOnDemandConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse on-demand config: %w", err)
		}
		return NewNacosOnDemandConfigManager(logger, onDemandConfig, nacosClient)

	default:
		return nil, fmt.Errorf("unknown config manager type: %s", config.Type)
	}
}

// NewMultiAgentConfigManager creates a MultiAgentConfigManager.
// Returns error if the config type doesn't support multi-agent mode.
func NewMultiAgentConfigManager(logger *zap.Logger, config Config, nacosClient config_client.IConfigClient) (MultiAgentConfigManager, error) {
	if config.Type != "multi_agent_nacos" {
		return nil, fmt.Errorf("config type %s does not support multi-agent mode", config.Type)
	}

	if nacosClient == nil {
		return nil, fmt.Errorf("nacos client is required for multi-agent nacos config manager")
	}

	multiConfig, err := parseMultiAgentConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse multi-agent config: %w", err)
	}

	return NewMultiAgentNacosConfigManager(logger, multiConfig, nacosClient)
}

// parseMultiAgentConfig parses MultiAgentConfig from Config.
func parseMultiAgentConfig(config Config) (MultiAgentConfig, error) {
	multiConfig := DefaultMultiAgentConfig()

	if config.MultiAgent.Namespace != "" {
		multiConfig.Namespace = config.MultiAgent.Namespace
	}

	if len(config.MultiAgent.Groups) > 0 {
		multiConfig.Groups = config.MultiAgent.Groups
	}

	if config.MultiAgent.ScanInterval != "" {
		d, err := time.ParseDuration(config.MultiAgent.ScanInterval)
		if err != nil {
			return multiConfig, fmt.Errorf("invalid scan_interval: %w", err)
		}
		multiConfig.ScanInterval = d
	}

	if config.MultiAgent.LoadTimeout != "" {
		d, err := time.ParseDuration(config.MultiAgent.LoadTimeout)
		if err != nil {
			return multiConfig, fmt.Errorf("invalid load_timeout: %w", err)
		}
		multiConfig.LoadTimeout = d
	}

	if config.MultiAgent.MaxRetries > 0 {
		multiConfig.MaxRetries = config.MultiAgent.MaxRetries
	}

	if config.MultiAgent.RetryInterval != "" {
		d, err := time.ParseDuration(config.MultiAgent.RetryInterval)
		if err != nil {
			return multiConfig, fmt.Errorf("invalid retry_interval: %w", err)
		}
		multiConfig.RetryInterval = d
	}

	multiConfig.EnableWatch = config.MultiAgent.EnableWatch

	return multiConfig, nil
}

// IsMultiAgentMode checks if the config is for multi-agent mode.
func IsMultiAgentMode(config Config) bool {
	return config.Type == "multi_agent_nacos" || config.MultiAgent.Enabled
}

// NewOnDemandConfigManager creates an OnDemandConfigManager.
func NewOnDemandConfigManager(logger *zap.Logger, config Config, nacosClient config_client.IConfigClient) (OnDemandConfigManager, error) {
	if config.Type != "on_demand" {
		return nil, fmt.Errorf("config type %s does not support on-demand mode", config.Type)
	}

	if nacosClient == nil {
		return nil, fmt.Errorf("nacos client is required for on-demand config manager")
	}

	onDemandConfig, err := parseOnDemandConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse on-demand config: %w", err)
	}

	return NewNacosOnDemandConfigManager(logger, onDemandConfig, nacosClient)
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
	return config.Type == "on_demand"
}
