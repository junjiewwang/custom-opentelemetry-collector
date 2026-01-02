// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

// Config defines the configuration for the admin extension.
type Config struct {
	// StorageExtension is the name of the storage extension to use.
	// Only needed if not using ControlPlaneExtension.
	StorageExtension string `mapstructure:"storage_extension"`

	// ControlPlaneExtension is the name of the controlplane extension to reuse components from.
	// When set, admin will reuse ConfigManager, TaskManager, AgentRegistry, and TokenManager
	// from the controlplane extension instead of creating its own instances.
	// This is the recommended configuration to avoid duplicate component instances.
	ControlPlaneExtension string `mapstructure:"controlplane_extension"`

	// ArthasTunnelExtension is the name of the arthas tunnel extension to use.
	// When set, admin will expose Arthas-related APIs and WebSocket endpoints.
	ArthasTunnelExtension string `mapstructure:"arthas_tunnel_extension"`

	// HTTP server configuration.
	HTTP HTTPConfig `mapstructure:"http"`

	// CORS configuration.
	CORS CORSConfig `mapstructure:"cors"`

	// Auth configuration.
	Auth AuthConfig `mapstructure:"auth"`

	// ConfigManager configuration.
	ConfigManager configmanager.Config `mapstructure:"config_manager"`

	// TaskManager configuration.
	TaskManager taskmanager.Config `mapstructure:"task_manager"`

	// AgentRegistry configuration.
	AgentRegistry agentregistry.Config `mapstructure:"agent_registry"`

	// TokenManager configuration.
	TokenManager tokenmanager.Config `mapstructure:"token_manager"`
}

// HTTPConfig defines HTTP server settings.
type HTTPConfig struct {
	// Endpoint is the address to listen on.
	Endpoint string `mapstructure:"endpoint"`

	// ReadTimeout is the maximum duration for reading the entire request.
	ReadTimeout time.Duration `mapstructure:"read_timeout"`

	// WriteTimeout is the maximum duration before timing out writes of the response.
	WriteTimeout time.Duration `mapstructure:"write_timeout"`

	// IdleTimeout is the maximum amount of time to wait for the next request.
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`
}

// CORSConfig defines CORS settings.
type CORSConfig struct {
	// Enabled enables CORS support.
	Enabled bool `mapstructure:"enabled"`

	// AllowedOrigins is a list of allowed origins.
	AllowedOrigins []string `mapstructure:"allowed_origins"`

	// AllowedMethods is a list of allowed HTTP methods.
	AllowedMethods []string `mapstructure:"allowed_methods"`

	// AllowedHeaders is a list of allowed HTTP headers.
	AllowedHeaders []string `mapstructure:"allowed_headers"`

	// AllowCredentials indicates whether credentials are allowed.
	AllowCredentials bool `mapstructure:"allow_credentials"`

	// MaxAge is the maximum age (in seconds) of preflight request results.
	MaxAge int `mapstructure:"max_age"`
}

// AuthConfig defines authentication settings.
type AuthConfig struct {
	// Enabled enables authentication.
	Enabled bool `mapstructure:"enabled"`

	// Type is the authentication type: "basic", "jwt", "api_key".
	Type string `mapstructure:"type"`

	// Basic authentication settings.
	Basic BasicAuthConfig `mapstructure:"basic"`

	// JWT authentication settings.
	JWT JWTAuthConfig `mapstructure:"jwt"`

	// API key authentication settings.
	APIKey APIKeyAuthConfig `mapstructure:"api_key"`
}

// BasicAuthConfig defines basic authentication settings.
type BasicAuthConfig struct {
	// Username is the expected username.
	Username string `mapstructure:"username"`

	// Password is the expected password.
	Password string `mapstructure:"password"`
}

// JWTAuthConfig defines JWT authentication settings.
type JWTAuthConfig struct {
	// Secret is the JWT signing secret.
	Secret string `mapstructure:"secret"`

	// Issuer is the expected JWT issuer.
	Issuer string `mapstructure:"issuer"`

	// Audience is the expected JWT audience.
	Audience string `mapstructure:"audience"`
}

// APIKeyAuthConfig defines API key authentication settings.
type APIKeyAuthConfig struct {
	// Header is the HTTP header name for the API key.
	Header string `mapstructure:"header"`

	// Keys is a list of valid API keys.
	Keys []string `mapstructure:"keys"`
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg.HTTP.Endpoint == "" {
		return errors.New("http.endpoint is required")
	}

	// Validate auth config
	if cfg.Auth.Enabled {
		switch cfg.Auth.Type {
		case "basic":
			if cfg.Auth.Basic.Username == "" || cfg.Auth.Basic.Password == "" {
				return errors.New("auth.basic.username and auth.basic.password are required")
			}
		case "jwt":
			if cfg.Auth.JWT.Secret == "" {
				return errors.New("auth.jwt.secret is required")
			}
		case "api_key":
			if len(cfg.Auth.APIKey.Keys) == 0 {
				return errors.New("auth.api_key.keys is required")
			}
		case "":
			return errors.New("auth.type is required when auth.enabled is true")
		default:
			return errors.New("auth.type must be 'basic', 'jwt', or 'api_key'")
		}
	}

	// If using controlplane extension, skip component config validation
	// as components will be reused from controlplane
	if cfg.ControlPlaneExtension != "" {
		return nil
	}

	// Validate component configs using shared validation from controlplaneext
	return controlplaneext.ValidateComponentConfigs(controlplaneext.ComponentConfigs{
		StorageExtension: cfg.StorageExtension,
		ConfigManager:    cfg.ConfigManager,
		TaskManager:      cfg.TaskManager,
		AgentRegistry:    cfg.AgentRegistry,
		TokenManager:     cfg.TokenManager,
	})
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		StorageExtension: "",
		HTTP: HTTPConfig{
			Endpoint:     "0.0.0.0:8080",
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
		CORS: CORSConfig{
			Enabled:          false,
			AllowedOrigins:   []string{"*"},
			AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: false,
			MaxAge:           86400,
		},
		Auth: AuthConfig{
			Enabled: false,
			Type:    "",
			APIKey: APIKeyAuthConfig{
				Header: "X-API-Key",
			},
		},
		ConfigManager: configmanager.DefaultConfig(),
		TaskManager:   taskmanager.DefaultConfig(),
		AgentRegistry: agentregistry.DefaultConfig(),
		TokenManager:  tokenmanager.DefaultConfig(),
	}
}
