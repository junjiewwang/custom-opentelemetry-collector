// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"errors"

	"go.opentelemetry.io/collector/config/configgrpc"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/confmap"
)

// Config defines the configuration for the agent gateway receiver.
type Config struct {
	// HTTP server configuration
	HTTP *confighttp.ServerConfig `mapstructure:"http"`

	// gRPC server configuration (for OTLP gRPC only)
	GRPC *configgrpc.ServerConfig `mapstructure:"grpc"`

	// OTLP configuration
	OTLP OTLPConfig `mapstructure:"otlp"`

	// Control Plane configuration
	ControlPlane ControlPlaneConfig `mapstructure:"control_plane"`

	// Arthas Tunnel configuration
	ArthasTunnel ArthasTunnelConfig `mapstructure:"arthas_tunnel"`

	// Token authentication configuration
	TokenAuth TokenAuthConfig `mapstructure:"token_auth"`
}

// OTLPConfig defines OTLP protocol configuration.
type OTLPConfig struct {
	// Enabled indicates whether OTLP endpoints are enabled.
	Enabled bool `mapstructure:"enabled"`

	// TracesPath is the URL path for traces. Default: /v1/traces
	TracesPath string `mapstructure:"traces_path"`

	// MetricsPath is the URL path for metrics. Default: /v1/metrics
	MetricsPath string `mapstructure:"metrics_path"`

	// LogsPath is the URL path for logs. Default: /v1/logs
	LogsPath string `mapstructure:"logs_path"`
}

// ControlPlaneConfig defines the control plane configuration.
type ControlPlaneConfig struct {
	// Enabled indicates whether control plane APIs are enabled.
	Enabled bool `mapstructure:"enabled"`

	// PathPrefix is the URL path prefix for control plane APIs. Default: /v1/control
	PathPrefix string `mapstructure:"path_prefix"`
}

// ArthasTunnelConfig defines the Arthas tunnel configuration.
type ArthasTunnelConfig struct {
	// Enabled indicates whether Arthas tunnel is enabled.
	Enabled bool `mapstructure:"enabled"`

	// Path is the WebSocket path for agent connections. Default: /v1/arthas/ws
	Path string `mapstructure:"path"`
}

// TokenAuthConfig defines the token authentication configuration.
type TokenAuthConfig struct {
	// Enabled indicates whether token authentication is enabled.
	Enabled bool `mapstructure:"enabled"`

	// HeaderName is the HTTP header name for the token. Default: Authorization
	HeaderName string `mapstructure:"header_name"`

	// HeaderPrefix is the prefix before the token in the header value. Default: Bearer
	HeaderPrefix string `mapstructure:"header_prefix"`

	// SkipPaths are paths that skip authentication.
	SkipPaths []string `mapstructure:"skip_paths"`

	// InjectAttributeKey is the resource attribute key to inject the validated app ID.
	// When set, the app ID from token validation will be written to this attribute.
	InjectAttributeKey string `mapstructure:"inject_attribute_key"`
}

var _ confmap.Unmarshaler = (*Config)(nil)

// Unmarshal a confmap.Conf into the config struct.
func (cfg *Config) Unmarshal(conf *confmap.Conf) error {
	if err := conf.Unmarshal(cfg); err != nil {
		return err
	}

	// Handle optional protocols
	if !conf.IsSet("grpc") {
		cfg.GRPC = nil
	}
	if !conf.IsSet("http") {
		cfg.HTTP = nil
	}

	return nil
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	// At least HTTP must be configured for the gateway
	if cfg.HTTP == nil {
		return errors.New("http configuration is required for agent gateway receiver")
	}

	// Control plane requires HTTP
	if cfg.ControlPlane.Enabled && cfg.HTTP == nil {
		return errors.New("control_plane requires http to be enabled")
	}

	// Arthas tunnel requires HTTP
	if cfg.ArthasTunnel.Enabled && cfg.HTTP == nil {
		return errors.New("arthas_tunnel requires http to be enabled")
	}

	return nil
}

// GetTracesPath returns the URL path for traces.
func (cfg *Config) GetTracesPath() string {
	if cfg.OTLP.TracesPath != "" {
		return cfg.OTLP.TracesPath
	}
	return "/v1/traces"
}

// GetMetricsPath returns the URL path for metrics.
func (cfg *Config) GetMetricsPath() string {
	if cfg.OTLP.MetricsPath != "" {
		return cfg.OTLP.MetricsPath
	}
	return "/v1/metrics"
}

// GetLogsPath returns the URL path for logs.
func (cfg *Config) GetLogsPath() string {
	if cfg.OTLP.LogsPath != "" {
		return cfg.OTLP.LogsPath
	}
	return "/v1/logs"
}

// GetControlPlanePathPrefix returns the URL path prefix for control plane APIs.
func (cfg *Config) GetControlPlanePathPrefix() string {
	if cfg.ControlPlane.PathPrefix != "" {
		return cfg.ControlPlane.PathPrefix
	}
	return "/v1/control"
}

// GetArthasTunnelPath returns the WebSocket path for Arthas tunnel.
func (cfg *Config) GetArthasTunnelPath() string {
	if cfg.ArthasTunnel.Path != "" {
		return cfg.ArthasTunnel.Path
	}
	return "/v1/arthas/ws"
}

// GetTokenAuthHeaderName returns the header name for token authentication.
func (cfg *Config) GetTokenAuthHeaderName() string {
	if cfg.TokenAuth.HeaderName != "" {
		return cfg.TokenAuth.HeaderName
	}
	return "Authorization"
}

// GetTokenAuthHeaderPrefix returns the header prefix for token authentication.
func (cfg *Config) GetTokenAuthHeaderPrefix() string {
	if cfg.TokenAuth.HeaderPrefix != "" {
		return cfg.TokenAuth.HeaderPrefix
	}
	return "Bearer "
}

// IsPathSkipped checks if a path should skip authentication.
func (cfg *Config) IsPathSkipped(path string) bool {
	for _, skip := range cfg.TokenAuth.SkipPaths {
		if path == skip {
			return true
		}
	}
	return false
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		HTTP: &confighttp.ServerConfig{
			Endpoint: "0.0.0.0:4318",
		},
		GRPC: &configgrpc.ServerConfig{
			NetAddr: confignet.AddrConfig{
				Endpoint:  "0.0.0.0:4317",
				Transport: confignet.TransportTypeTCP,
			},
		},
		OTLP: OTLPConfig{
			Enabled:     true,
			TracesPath:  "/v1/traces",
			MetricsPath: "/v1/metrics",
			LogsPath:    "/v1/logs",
		},
		ControlPlane: ControlPlaneConfig{
			Enabled:    true,
			PathPrefix: "/v1/control",
		},
		ArthasTunnel: ArthasTunnelConfig{
			Enabled: false,
			Path:    "/v1/arthas/ws",
		},
		TokenAuth: TokenAuthConfig{
			Enabled:      false,
			HeaderName:   "Authorization",
			HeaderPrefix: "Bearer ",
			SkipPaths:    []string{"/health"},
		},
	}
}
