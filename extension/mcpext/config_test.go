// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "valid config with api_key auth",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.APIKeys = []string{"test-key"}
				return c
			}(),
			wantErr: "",
		},
		{
			name: "valid config with no auth",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.Type = "none"
				return c
			}(),
			wantErr: "",
		},
		{
			name: "empty endpoint",
			config: func() *Config {
				c := createDefaultConfig()
				c.Endpoint.Endpoint = ""
				c.Auth.APIKeys = []string{"test-key"}
				return c
			}(),
			wantErr: "endpoint must not be empty",
		},
		{
			name: "api_key auth without keys",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.Type = "api_key"
				c.Auth.APIKeys = nil
				return c
			}(),
			wantErr: "at least one API key must be configured",
		},
		{
			name: "unsupported auth type",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.Type = "jwt"
				return c
			}(),
			wantErr: "unsupported auth type: jwt",
		},
		{
			name: "zero max concurrent sessions",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.APIKeys = []string{"test-key"}
				c.MaxConcurrentSessions = 0
				return c
			}(),
			wantErr: "max_concurrent_sessions must be positive",
		},
		{
			name: "zero tool timeout",
			config: func() *Config {
				c := createDefaultConfig()
				c.Auth.APIKeys = []string{"test-key"}
				c.ToolTimeout = 0
				return c
			}(),
			wantErr: "tool_timeout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()

	assert.Equal(t, "0.0.0.0:8686", cfg.Endpoint.Endpoint)
	assert.Equal(t, "api_key", cfg.Auth.Type)
	assert.Equal(t, "controlplane", cfg.ControlPlaneExtension)
	assert.Equal(t, "arthas_tunnel", cfg.ArthasTunnelExtension)
	assert.Equal(t, 10, cfg.MaxConcurrentSessions)
	assert.Equal(t, 30, cfg.ToolTimeout)
}
