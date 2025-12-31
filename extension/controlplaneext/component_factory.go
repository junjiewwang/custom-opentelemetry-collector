// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"errors"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext"
)

// ComponentFactory creates control plane components.
// This is exported so that adminext can reuse the same component creation logic.
type ComponentFactory struct {
	logger  *zap.Logger
	storage storageext.Storage
}

// NewComponentFactory creates a new component factory.
func NewComponentFactory(logger *zap.Logger, storage storageext.Storage) *ComponentFactory {
	return &ComponentFactory{
		logger:  logger,
		storage: storage,
	}
}

// GetStorageExtension finds and returns the storage extension from the host.
func GetStorageExtension(host component.Host, storageExtName string, logger *zap.Logger) (storageext.Storage, error) {
	if storageExtName == "" {
		return nil, nil
	}

	storageType := component.MustNewType(storageExtName)
	for id, ext := range host.GetExtensions() {
		if id.Type() == storageType {
			if s, ok := ext.(storageext.Storage); ok {
				logger.Info("Using storage extension", zap.String("name", storageExtName))
				return s, nil
			}
		}
	}

	return nil, fmt.Errorf("storage extension %q not found or does not implement Storage interface", storageExtName)
}

// CreateConfigManager creates the appropriate ConfigManager based on config.
func (f *ComponentFactory) CreateConfigManager(cfg configmanager.Config) (configmanager.ConfigManager, error) {
	switch cfg.Type {
	case "nacos":
		if f.storage == nil {
			return nil, fmt.Errorf("storage extension required for nacos config manager")
		}
		nacosName := cfg.NacosName
		if nacosName == "" {
			nacosName = "default"
		}
		client, err := f.storage.GetNacosConfigClient(nacosName)
		if err != nil {
			return nil, fmt.Errorf("failed to get nacos client %q: %w", nacosName, err)
		}
		return configmanager.NewNacosConfigManager(f.logger, cfg, client)

	default:
		return configmanager.NewMemoryConfigManager(f.logger), nil
	}
}

// CreateConfigManagerWithOnDemand creates ConfigManager with on_demand support.
// This is used by adminext which supports the on_demand type.
func (f *ComponentFactory) CreateConfigManagerWithOnDemand(cfg configmanager.Config) (configmanager.ConfigManager, configmanager.OnDemandConfigManager, error) {
	switch cfg.Type {
	case "nacos":
		if f.storage == nil {
			return nil, nil, fmt.Errorf("storage extension required for nacos config manager")
		}
		nacosName := cfg.NacosName
		if nacosName == "" {
			nacosName = "default"
		}
		client, err := f.storage.GetNacosConfigClient(nacosName)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get nacos client %q: %w", nacosName, err)
		}
		mgr, err := configmanager.NewNacosConfigManager(f.logger, cfg, client)
		return mgr, nil, err

	case "on_demand":
		if f.storage == nil {
			return nil, nil, fmt.Errorf("storage extension required for on-demand config manager")
		}
		nacosName := cfg.NacosName
		if nacosName == "" {
			nacosName = "default"
		}
		client, err := f.storage.GetNacosConfigClient(nacosName)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get nacos client %q: %w", nacosName, err)
		}
		onDemandMgr, err := configmanager.NewOnDemandConfigManager(f.logger, cfg, client)
		if err != nil {
			return nil, nil, err
		}
		return onDemandMgr, onDemandMgr, nil

	default:
		return configmanager.NewMemoryConfigManager(f.logger), nil, nil
	}
}

// CreateTaskManager creates the appropriate TaskManager based on config.
func (f *ComponentFactory) CreateTaskManager(cfg taskmanager.Config) (taskmanager.TaskManager, error) {
	switch cfg.Type {
	case "redis":
		if f.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis task manager")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := f.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return taskmanager.NewRedisTaskManager(f.logger, cfg, client)

	default:
		return taskmanager.NewMemoryTaskManager(f.logger, cfg), nil
	}
}

// CreateAgentRegistry creates the appropriate AgentRegistry based on config.
func (f *ComponentFactory) CreateAgentRegistry(cfg agentregistry.Config) (agentregistry.AgentRegistry, error) {
	switch cfg.Type {
	case "redis":
		if f.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis agent registry")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := f.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return agentregistry.NewRedisAgentRegistry(f.logger, cfg, client)

	default:
		return agentregistry.NewMemoryAgentRegistry(f.logger, cfg), nil
	}
}

// CreateTokenManager creates the appropriate TokenManager based on config.
func (f *ComponentFactory) CreateTokenManager(cfg tokenmanager.Config) (tokenmanager.TokenManager, error) {
	switch cfg.Type {
	case "redis":
		if f.storage == nil {
			return nil, fmt.Errorf("storage extension required for redis token manager")
		}
		redisName := cfg.RedisName
		if redisName == "" {
			redisName = "default"
		}
		client, err := f.storage.GetRedis(redisName)
		if err != nil {
			return nil, fmt.Errorf("failed to get redis client %q: %w", redisName, err)
		}
		return tokenmanager.NewRedisTokenManager(f.logger, cfg, client)

	default:
		return tokenmanager.NewMemoryTokenManager(f.logger, cfg), nil
	}
}

// ComponentConfigs holds the configuration for all control plane components.
type ComponentConfigs struct {
	StorageExtension string
	ConfigManager    configmanager.Config
	TaskManager      taskmanager.Config
	AgentRegistry    agentregistry.Config
	TokenManager     tokenmanager.Config
}

// ValidateComponentConfigs validates the component configurations.
// This is the common validation logic shared between controlplaneext and adminext.
func ValidateComponentConfigs(cfg ComponentConfigs) error {
	// Validate ConfigManager
	validConfigTypes := map[string]bool{
		"":                  true,
		"memory":            true,
		"nacos":             true,
		"multi_agent_nacos": true,
		"on_demand":         true,
	}
	if !validConfigTypes[cfg.ConfigManager.Type] {
		return errors.New("config_manager.type must be 'memory', 'nacos', 'multi_agent_nacos', or 'on_demand'")
	}

	requiresStorage := cfg.ConfigManager.Type == "nacos" ||
		cfg.ConfigManager.Type == "multi_agent_nacos" ||
		cfg.ConfigManager.Type == "on_demand"
	if requiresStorage && cfg.StorageExtension == "" {
		return errors.New("storage_extension is required when config_manager.type is 'nacos', 'multi_agent_nacos', or 'on_demand'")
	}

	// Validate TaskManager
	if cfg.TaskManager.Type != "" && cfg.TaskManager.Type != "memory" && cfg.TaskManager.Type != "redis" {
		return errors.New("task_manager.type must be 'memory' or 'redis'")
	}

	if cfg.TaskManager.Type == "redis" && cfg.StorageExtension == "" {
		return errors.New("storage_extension is required when task_manager.type is 'redis'")
	}

	// Validate AgentRegistry
	if cfg.AgentRegistry.Type != "" && cfg.AgentRegistry.Type != "memory" && cfg.AgentRegistry.Type != "redis" {
		return errors.New("agent_registry.type must be 'memory' or 'redis'")
	}

	if cfg.AgentRegistry.Type == "redis" && cfg.StorageExtension == "" {
		return errors.New("storage_extension is required when agent_registry.type is 'redis'")
	}

	// Validate TokenManager
	if cfg.TokenManager.Type != "" && cfg.TokenManager.Type != "memory" && cfg.TokenManager.Type != "redis" {
		return errors.New("token_manager.type must be 'memory' or 'redis'")
	}

	if cfg.TokenManager.Type == "redis" && cfg.StorageExtension == "" {
		return errors.New("storage_extension is required when token_manager.type is 'redis'")
	}

	return nil
}
