// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"time"
)

// Config defines the configuration for the Arthas tunnel extension.
type Config struct {
	// ===== Arthas-URI compat mode options =====

	// CompatConnectTimeout is the timeout for waiting agent openTunnel after connectArthas.
	// Default: 20s (aligned with official tunnel-server)
	CompatConnectTimeout time.Duration `mapstructure:"compat_connect_timeout"`

	// StrictIngressMethodAllowlist restricts which method is allowed on each ingress.
	// - agentgateway ingress: agentRegister/openTunnel
	// - admin ingress: connectArthas
	// Default: true
	StrictIngressMethodAllowlist bool `mapstructure:"strict_ingress_method_allowlist"`

	// MaxPendingConnections limits the number of pending connectArthas waiting for openTunnel.
	// 0 means unlimited.
	// Default: 10000
	MaxPendingConnections int `mapstructure:"max_pending_connections"`

	// ===== Keepalive options (mainly for agentRegister control connections) =====

	// PingInterval is the interval between ping control frames.
	// Default: 30s
	PingInterval time.Duration `mapstructure:"ping_interval"`

	// PongTimeout is the timeout for pong response.
	// Default: 60s
	PongTimeout time.Duration `mapstructure:"pong_timeout"`

	// ===== Legacy fields (kept for compatibility; unused in compat mode) =====

	MaxSessionsPerAgent       int           `mapstructure:"max_sessions_per_agent"`
	SessionIdleTimeout        time.Duration `mapstructure:"session_idle_timeout"`
	SessionMaxDuration        time.Duration `mapstructure:"session_max_duration"`
	OutputBufferSize          int           `mapstructure:"output_buffer_size"`
	OutputFlushInterval       time.Duration `mapstructure:"output_flush_interval"`
	TerminalOpenTimeout       time.Duration `mapstructure:"terminal_open_timeout"`
	TerminalOpenRetryInterval time.Duration `mapstructure:"terminal_open_retry_interval"`
	ArthasStartCooldown       time.Duration `mapstructure:"arthas_start_cooldown"`
	MaxReconnectAttempts      int           `mapstructure:"max_reconnect_attempts"`
}

// Validate validates the configuration.
func (cfg *Config) Validate() error {
	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		CompatConnectTimeout:         20 * time.Second,
		StrictIngressMethodAllowlist: true,
		MaxPendingConnections:        10000,

		PingInterval: 30 * time.Second,
		PongTimeout:  60 * time.Second,

		// Legacy defaults
		MaxSessionsPerAgent:       5,
		SessionIdleTimeout:        30 * time.Minute,
		SessionMaxDuration:        4 * time.Hour,
		OutputBufferSize:          65536,
		OutputFlushInterval:       50 * time.Millisecond,
		TerminalOpenTimeout:       60 * time.Second,
		TerminalOpenRetryInterval: 1 * time.Second,
		ArthasStartCooldown:       3 * time.Second,
		MaxReconnectAttempts:      0,
	}
}
