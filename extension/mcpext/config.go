// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"errors"
	"fmt"
)

const (
	// defaultInstrumentationExtension is the extension that owns the shared rule manager.
	defaultInstrumentationExtension = "admin"
	// defaultInstrumentationMaxTargets caps how many service instances a rule may target.
	defaultInstrumentationMaxTargets = 100
)

// defaultDeniedClassPrefixes protects JDK internals and the collector's own
// classes from being instrumented through an AI-driven tool.
var defaultDeniedClassPrefixes = []string{
	"java.",
	"javax.",
	"jdk.",
	"sun.",
	"com.sun.",
	"io.opentelemetry.",
}

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

	// Instrumentation configures the optional dynamic instrumentation tools.
	// Disabled by default; enabling it adds a dependency on the extension that
	// owns the shared InstrumentationManager.
	Instrumentation InstrumentationConfig `mapstructure:"instrumentation"`
}

// InstrumentationConfig configures the dynamic instrumentation MCP tools.
type InstrumentationConfig struct {
	// Enabled registers the read-only instrumentation tools.
	Enabled bool `mapstructure:"enabled"`

	// Extension is the name of the extension implementing instrumentationmanager.Provider.
	Extension string `mapstructure:"extension"`

	// AllowWrite additionally registers create/pause/resume/delete tools.
	// When false, write tools are not registered at all.
	AllowWrite bool `mapstructure:"allow_write"`

	// MaxTargets is the maximum number of service instances a created rule may target.
	MaxTargets int `mapstructure:"max_targets"`

	// ManageOnlyOwnRules restricts write tools to rules created through MCP.
	ManageOnlyOwnRules bool `mapstructure:"manage_only_own_rules"`

	// AllowForce permits forcing instrumentation on the target agent.
	AllowForce bool `mapstructure:"allow_force"`

	// AllowCapture permits capturing method arguments and return values.
	AllowCapture bool `mapstructure:"allow_capture"`

	// DeniedClassPrefixes rejects class names starting with any of these prefixes.
	DeniedClassPrefixes []string `mapstructure:"denied_class_prefixes"`

	// AllowedClassPrefixes, when non-empty, only permits class names matching a prefix.
	AllowedClassPrefixes []string `mapstructure:"allowed_class_prefixes"`
}

// AuthConfig configures the authentication for MCP connections.
type AuthConfig struct {
	// Type is the authentication type. Supported: "api_key", "none".
	// Default: "api_key"
	Type string `mapstructure:"type"`

	// APIKeys are static super keys with full privileges across tenants.
	// They remain optional for backward compatibility with existing clients.
	APIKeys []string `mapstructure:"api_keys"`

	// DynamicKeys enables validation of tenant/operator keys (tk_ / ok_)
	// through the shared tenant manager. When enabled, static keys are
	// optional and the control plane must provide a tenant manager.
	DynamicKeys bool `mapstructure:"dynamic_keys"`
}

// createDefaultConfig returns the default configuration for the MCP extension.
func createDefaultConfig() *Config {
	return &Config{
		Endpoint: "0.0.0.0:8686",
		Auth: AuthConfig{
			Type:        "api_key",
			DynamicKeys: false,
		},
		ControlPlaneExtension: "controlplane",
		ArthasTunnelExtension: "arthas_tunnel",
		MaxConcurrentSessions: 10,
		ToolTimeout:           30,
		Instrumentation: InstrumentationConfig{
			Enabled:              false,
			Extension:            defaultInstrumentationExtension,
			AllowWrite:           false,
			MaxTargets:           defaultInstrumentationMaxTargets,
			ManageOnlyOwnRules:   true,
			AllowForce:           false,
			AllowCapture:         false,
			DeniedClassPrefixes:  append([]string(nil), defaultDeniedClassPrefixes...),
			AllowedClassPrefixes: nil,
		},
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return errors.New("endpoint must not be empty")
	}

	switch c.Auth.Type {
	case "api_key":
		if !c.Auth.DynamicKeys && len(c.Auth.APIKeys) == 0 {
			return errors.New("at least one static API key must be configured when dynamic_keys is false")
		}
	case "none":
		if c.Auth.DynamicKeys {
			return errors.New("dynamic_keys requires auth type 'api_key'")
		}
	default:
		return fmt.Errorf("unsupported auth type: %s (supported: api_key, none)", c.Auth.Type)
	}

	if c.MaxConcurrentSessions <= 0 {
		return errors.New("max_concurrent_sessions must be positive")
	}

	if c.ToolTimeout <= 0 {
		return errors.New("tool_timeout must be positive")
	}

	if err := c.Instrumentation.validate(); err != nil {
		return fmt.Errorf("instrumentation: %w", err)
	}

	return nil
}

func (c *InstrumentationConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Extension == "" {
		return errors.New("extension must not be empty when enabled")
	}
	if c.MaxTargets <= 0 {
		return errors.New("max_targets must be positive")
	}
	return nil
}
