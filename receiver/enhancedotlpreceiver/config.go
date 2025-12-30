// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"errors"

	"go.opentelemetry.io/collector/config/configgrpc"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/confmap"
)

// Config defines the configuration for the enhanced OTLP receiver.
type Config struct {
	// Protocols is the configuration for the supported protocols.
	Protocols Protocols `mapstructure:"protocols"`

	// ControlPlane configuration for the control plane API.
	// Control plane APIs are served on the same HTTP port as OTLP when HTTP is enabled.
	ControlPlane ControlPlaneConfig `mapstructure:"control_plane"`
}

// Protocols is the configuration for the supported protocols.
type Protocols struct {
	GRPC *configgrpc.ServerConfig `mapstructure:"grpc"`
	HTTP *HTTPConfig              `mapstructure:"http"`
}

// HTTPConfig defines HTTP protocol configuration.
type HTTPConfig struct {
	*confighttp.ServerConfig `mapstructure:",squash"`

	// The URL path to receive traces on. If omitted "/v1/traces" will be used.
	TracesURLPath string `mapstructure:"traces_url_path,omitempty"`

	// The URL path to receive metrics on. If omitted "/v1/metrics" will be used.
	MetricsURLPath string `mapstructure:"metrics_url_path,omitempty"`

	// The URL path to receive logs on. If omitted "/v1/logs" will be used.
	LogsURLPath string `mapstructure:"logs_url_path,omitempty"`
}

// ControlPlaneConfig defines the control plane configuration.
// Control plane APIs are served on the same HTTP port as OTLP protocol.
type ControlPlaneConfig struct {
	// Enabled indicates whether the control plane APIs are enabled.
	// When enabled, control plane APIs will be available at /v1/control/* paths
	// on the same HTTP endpoint as OTLP receiver.
	Enabled bool `mapstructure:"enabled"`

	// ControlURLPathPrefix is the URL path prefix for control plane APIs.
	// Default is "/v1/control".
	ControlURLPathPrefix string `mapstructure:"control_url_path_prefix,omitempty"`
}

var _ confmap.Unmarshaler = (*Config)(nil)

// Unmarshal a confmap.Conf into the config struct.
func (cfg *Config) Unmarshal(conf *confmap.Conf) error {
	// Unmarshal the config
	if err := conf.Unmarshal(cfg); err != nil {
		return err
	}

	// Handle optional protocols
	if !conf.IsSet("protocols::grpc") {
		cfg.Protocols.GRPC = nil
	}

	if !conf.IsSet("protocols::http") {
		cfg.Protocols.HTTP = nil
	}

	return nil
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	// Validate protocols - at least one must be configured
	if cfg.Protocols.GRPC == nil && cfg.Protocols.HTTP == nil {
		return errors.New("must specify at least one protocol when using the enhanced OTLP receiver")
	}

	// Control plane requires HTTP protocol
	if cfg.ControlPlane.Enabled && cfg.Protocols.HTTP == nil {
		return errors.New("control_plane requires HTTP protocol to be enabled")
	}

	return nil
}

// GetTracesURLPath returns the URL path for traces.
func (cfg *Config) GetTracesURLPath() string {
	if cfg.Protocols.HTTP != nil && cfg.Protocols.HTTP.TracesURLPath != "" {
		return cfg.Protocols.HTTP.TracesURLPath
	}
	return "/v1/traces"
}

// GetMetricsURLPath returns the URL path for metrics.
func (cfg *Config) GetMetricsURLPath() string {
	if cfg.Protocols.HTTP != nil && cfg.Protocols.HTTP.MetricsURLPath != "" {
		return cfg.Protocols.HTTP.MetricsURLPath
	}
	return "/v1/metrics"
}

// GetLogsURLPath returns the URL path for logs.
func (cfg *Config) GetLogsURLPath() string {
	if cfg.Protocols.HTTP != nil && cfg.Protocols.HTTP.LogsURLPath != "" {
		return cfg.Protocols.HTTP.LogsURLPath
	}
	return "/v1/logs"
}

// GetControlURLPathPrefix returns the URL path prefix for control plane APIs.
func (cfg *Config) GetControlURLPathPrefix() string {
	if cfg.ControlPlane.ControlURLPathPrefix != "" {
		return cfg.ControlPlane.ControlURLPathPrefix
	}
	return "/v1/control"
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		Protocols: Protocols{
			GRPC: &configgrpc.ServerConfig{
				NetAddr: confignet.AddrConfig{
					Endpoint:  "0.0.0.0:4317",
					Transport: confignet.TransportTypeTCP,
				},
			},
			HTTP: &HTTPConfig{
				ServerConfig: &confighttp.ServerConfig{
					Endpoint: "0.0.0.0:4318",
				},
				TracesURLPath:  "/v1/traces",
				MetricsURLPath: "/v1/metrics",
				LogsURLPath:    "/v1/logs",
			},
		},
		ControlPlane: ControlPlaneConfig{
			Enabled:              true,
			ControlURLPathPrefix: "/v1/control",
		},
	}
}
