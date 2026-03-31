// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"errors"
	"fmt"
)

// Config defines the configuration for the MCP extension.
type Config struct {
	// Endpoint is the address to listen on for MCP Streamable HTTP connections.
	// Format: "host:port"
	// Default: "0.0.0.0:8686"
	Endpoint string `mapstructure:"endpoint"`

	// Auth configures the authentication for MCP connections.
	Auth AuthConfig `mapstructure:"auth"`

	// ControlPlaneExtension is the name of the control plane extension to use.
	// Default: "controlplane"
	ControlPlaneExtension string `mapstructure:"controlplane_extension"`

	// ArthasTunnelExtension is the name of the Arthas tunnel extension to use.
	// Default: "arthas_tunnel"
	ArthasTunnelExtension string `mapstructure:"arthas_tunnel_extension"`

	// MaxConcurrentSessions is the maximum number of concurrent MCP sessions.
	// Default: 10
	MaxConcurrentSessions int `mapstructure:"max_concurrent_sessions"`

	// ToolTimeout is the default timeout for tool execution in seconds.
	// Default: 30
	ToolTimeout int `mapstructure:"tool_timeout"`
}

// AuthConfig configures the authentication for MCP connections.
type AuthConfig struct {
	// Type is the authentication type. Supported: "api_key", "none".
	// Default: "api_key"
	Type string `mapstructure:"type"`

	// APIKeys is a list of valid API keys when Type is "api_key".
	APIKeys []string `mapstructure:"api_keys"`
}

// createDefaultConfig returns the default configuration for the MCP extension.
func createDefaultConfig() *Config {
	return &Config{
		Endpoint: "0.0.0.0:8686",
		Auth: AuthConfig{
			Type: "api_key",
		},
		ControlPlaneExtension: "controlplane",
		ArthasTunnelExtension: "arthas_tunnel",
		MaxConcurrentSessions: 10,
		ToolTimeout:           30,
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return errors.New("endpoint must not be empty")
	}

	switch c.Auth.Type {
	case "api_key":
		if len(c.Auth.APIKeys) == 0 {
			return errors.New("at least one API key must be configured when auth type is 'api_key'")
		}
	case "none":
		// No validation needed
	default:
		return fmt.Errorf("unsupported auth type: %s (supported: api_key, none)", c.Auth.Type)
	}

	if c.MaxConcurrentSessions <= 0 {
		return errors.New("max_concurrent_sessions must be positive")
	}

	if c.ToolTimeout <= 0 {
		return errors.New("tool_timeout must be positive")
	}

	return nil
}
