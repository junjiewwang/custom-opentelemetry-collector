// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"time"
)

// Config defines the configuration for the Arthas tunnel extension.
type Config struct {
	// MaxSessionsPerAgent is the maximum number of terminal sessions per agent.
	// Default: 5
	MaxSessionsPerAgent int `mapstructure:"max_sessions_per_agent"`

	// SessionIdleTimeout is the duration after which an idle session is closed.
	// Default: 30m
	SessionIdleTimeout time.Duration `mapstructure:"session_idle_timeout"`

	// SessionMaxDuration is the maximum duration of a session.
	// Default: 4h
	SessionMaxDuration time.Duration `mapstructure:"session_max_duration"`

	// PingInterval is the interval between ping messages to agents.
	// Default: 30s
	PingInterval time.Duration `mapstructure:"ping_interval"`

	// PongTimeout is the timeout for pong response.
	// Default: 10s
	PongTimeout time.Duration `mapstructure:"pong_timeout"`

	// OutputBufferSize is the size of the output buffer for terminal output.
	// Default: 65536 (64KB)
	OutputBufferSize int `mapstructure:"output_buffer_size"`

	// OutputFlushInterval is the interval for flushing output buffer.
	// Default: 50ms
	OutputFlushInterval time.Duration `mapstructure:"output_flush_interval"`

	// MaxReconnectAttempts is the maximum number of reconnect attempts for agents.
	// 0 means unlimited.
	// Default: 0
	MaxReconnectAttempts int `mapstructure:"max_reconnect_attempts"`
}

// Validate validates the configuration.
func (cfg *Config) Validate() error {
	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		MaxSessionsPerAgent:  5,
		SessionIdleTimeout:   30 * time.Minute,
		SessionMaxDuration:   4 * time.Hour,
		PingInterval:         30 * time.Second,
		PongTimeout:          10 * time.Second,
		OutputBufferSize:     65536,
		OutputFlushInterval:  50 * time.Millisecond,
		MaxReconnectAttempts: 0,
	}
}
