// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ConfigChangeCallback is called when configuration changes.
type ConfigChangeCallback func(oldConfig, newConfig *model.AgentConfig)

// ConfigManager defines the interface for configuration management.
// Uses model.AgentConfig as the canonical type.
type ConfigManager interface {
	// GetConfig returns the current configuration.
	GetConfig(ctx context.Context) (*model.AgentConfig, error)

	// UpdateConfig updates the configuration.
	UpdateConfig(ctx context.Context, config *model.AgentConfig) error

	// Watch starts watching for configuration changes.
	// The callback is invoked when configuration changes.
	Watch(ctx context.Context, callback ConfigChangeCallback) error

	// StopWatch stops watching for configuration changes.
	StopWatch() error

	// Subscribe registers a callback for configuration changes (local notifications).
	Subscribe(callback ConfigChangeCallback)

	// Start initializes the config manager.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// AgentConfigChangeEvent represents a config change event for an agent.
type AgentConfigChangeEvent struct {
	Type      string // "created", "updated", "deleted"
	Token     string
	AgentID   string
	OldConfig *model.AgentConfig
	NewConfig *model.AgentConfig
	Timestamp int64 // millis
}

// AgentConfigChangeCallback is called when an agent's config changes.
type AgentConfigChangeCallback func(event *AgentConfigChangeEvent)

// OnDemandConfigManager is the interface for on-demand config loading mode.
// Configs are loaded when agents connect and released when they disconnect.
type OnDemandConfigManager interface {
	ConfigManager

	// RegisterAgent registers an agent and starts watching its config.
	// Returns the agent's config (or nil if not found, agent should use default).
	RegisterAgent(ctx context.Context, token, agentID, serviceName string) (*model.AgentConfig, error)

	// UnregisterAgent unregisters an agent and releases its resources.
	UnregisterAgent(ctx context.Context, token, agentID string) error

	// GetConfigForAgent returns config for a specific agent.
	// If no specific config exists, returns the service config if serviceName is provided.
	// If no service config exists, returns the default config for the token.
	// If no default config exists, returns nil (agent should use local default).
	GetConfigForAgent(ctx context.Context, token, agentID, serviceName string) (*model.AgentConfig, error)

	// SetConfigForAgent sets/updates config for a specific agent.
	SetConfigForAgent(ctx context.Context, token, agentID string, config *model.AgentConfig) error

	// SetServiceConfig sets/updates config for a specific service.
	SetServiceConfig(ctx context.Context, token, serviceName string, config *model.AgentConfig) error

	// GetServiceConfig returns the config for a specific service.
	GetServiceConfig(ctx context.Context, token, serviceName string) (*model.AgentConfig, error)

	// DeleteServiceConfig deletes config for a specific service.
	DeleteServiceConfig(ctx context.Context, token, serviceName string) error

	// SetDefaultConfig sets the default config for a token (all agents under this token).
	SetDefaultConfig(ctx context.Context, token string, config *model.AgentConfig) error

	// GetDefaultConfig returns the default config for a token.
	GetDefaultConfig(ctx context.Context, token string) (*model.AgentConfig, error)

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

// Config holds the configuration for ConfigManager.
type Config struct {
	// Type specifies the backend type: "memory", "nacos", "multi_agent_nacos", or "on_demand"
	Type string `mapstructure:"type"`

	// NacosName is the name of the Nacos connection from storage extension
	NacosName string `mapstructure:"nacos_name"`

	// DataId is the configuration data ID (for single-agent mode)
	DataId string `mapstructure:"data_id"`

	// Group is the configuration group (for single-agent mode, also used as token)
	Group string `mapstructure:"group"`

	// MultiAgent holds configuration for multi-agent mode (deprecated, use on_demand)
	MultiAgent MultiAgentModeConfig `mapstructure:"multi_agent"`

	// OnDemand holds configuration for on-demand mode
	OnDemand OnDemandModeConfig `mapstructure:"on_demand"`
}

// MultiAgentModeConfig holds configuration for multi-agent Nacos mode.
// Deprecated: Use OnDemandModeConfig instead.
type MultiAgentModeConfig struct {
	// Enabled enables multi-agent mode
	Enabled bool `mapstructure:"enabled"`

	// Namespace for Nacos (empty for default namespace)
	Namespace string `mapstructure:"namespace"`

	// Groups (tokens) to scan. If empty, no automatic scanning.
	Groups []string `mapstructure:"groups"`

	// ScanInterval is the interval for periodic config scanning
	ScanInterval string `mapstructure:"scan_interval"`

	// LoadTimeout is the timeout for loading a single config
	LoadTimeout string `mapstructure:"load_timeout"`

	// MaxRetries is the max retries for failed operations
	MaxRetries int `mapstructure:"max_retries"`

	// RetryInterval is the interval between retries
	RetryInterval string `mapstructure:"retry_interval"`

	// EnableWatch enables Nacos config change watching
	EnableWatch bool `mapstructure:"enable_watch"`
}

// OnDemandModeConfig holds configuration for on-demand config loading mode.
type OnDemandModeConfig struct {
	// Namespace for Nacos (empty for default namespace)
	Namespace string `mapstructure:"namespace"`

	// LoadTimeout is the timeout for loading a single config
	LoadTimeout string `mapstructure:"load_timeout"`

	// MaxRetries is the max retries for failed operations
	MaxRetries int `mapstructure:"max_retries"`

	// RetryInterval is the interval between retries
	RetryInterval string `mapstructure:"retry_interval"`

	// CacheExpiration is how long cached configs remain valid after agent disconnects
	CacheExpiration string `mapstructure:"cache_expiration"`

	// CleanupInterval is the interval for cleaning up expired cache entries
	CleanupInterval string `mapstructure:"cleanup_interval"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:      "memory",
		NacosName: "default",
		Group:     "OTEL_COLLECTOR",
		DataId:    "otel-agent-config",
		MultiAgent: MultiAgentModeConfig{
			Enabled:       false,
			Namespace:     "",
			Groups:        nil,
			ScanInterval:  "30s",
			LoadTimeout:   "5s",
			MaxRetries:    3,
			RetryInterval: "1s",
			EnableWatch:   true,
		},
		OnDemand: OnDemandModeConfig{
			Namespace:       "",
			LoadTimeout:     "5s",
			MaxRetries:      3,
			RetryInterval:   "1s",
			CacheExpiration: "5m",
			CleanupInterval: "1m",
		},
	}
}
