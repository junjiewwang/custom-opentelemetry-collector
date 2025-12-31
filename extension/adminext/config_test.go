// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "default config is valid",
			config:  createDefaultConfig(),
			wantErr: false,
		},
		{
			name: "missing http endpoint",
			config: &Config{
				HTTP: HTTPConfig{},
			},
			wantErr: true,
			errMsg:  "http.endpoint is required",
		},
		{
			name: "valid config with basic auth",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "basic",
					Basic: BasicAuthConfig{
						Username: "admin",
						Password: "admin123",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "basic auth without username",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "basic",
					Basic: BasicAuthConfig{
						Password: "admin123",
					},
				},
			},
			wantErr: true,
			errMsg:  "auth.basic.username and auth.basic.password are required",
		},
		{
			name: "basic auth without password",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "basic",
					Basic: BasicAuthConfig{
						Username: "admin",
					},
				},
			},
			wantErr: true,
			errMsg:  "auth.basic.username and auth.basic.password are required",
		},
		{
			name: "valid config with jwt auth",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "jwt",
					JWT: JWTAuthConfig{
						Secret: "my-secret-key",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "jwt auth without secret",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "jwt",
				},
			},
			wantErr: true,
			errMsg:  "auth.jwt.secret is required",
		},
		{
			name: "valid config with api_key auth",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "api_key",
					APIKey: APIKeyAuthConfig{
						Keys: []string{"key1", "key2"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "api_key auth without keys",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "api_key",
				},
			},
			wantErr: true,
			errMsg:  "auth.api_key.keys is required",
		},
		{
			name: "auth enabled without type",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
				},
			},
			wantErr: true,
			errMsg:  "auth.type is required when auth.enabled is true",
		},
		{
			name: "invalid auth type",
			config: &Config{
				HTTP: HTTPConfig{Endpoint: "0.0.0.0:8080"},
				Auth: AuthConfig{
					Enabled: true,
					Type:    "invalid",
				},
			},
			wantErr: true,
			errMsg:  "auth.type must be 'basic', 'jwt', or 'api_key'",
		},
		{
			name: "invalid config_manager type",
			config: &Config{
				HTTP:          HTTPConfig{Endpoint: "0.0.0.0:8080"},
				ConfigManager: configmanager.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "config_manager.type must be 'memory', 'nacos', 'multi_agent_nacos', or 'on_demand'",
		},
		{
			name: "nacos config without storage extension",
			config: &Config{
				HTTP:          HTTPConfig{Endpoint: "0.0.0.0:8080"},
				ConfigManager: configmanager.Config{Type: "nacos"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required when config_manager.type is 'nacos'",
		},
		{
			name: "invalid task_manager type",
			config: &Config{
				HTTP:        HTTPConfig{Endpoint: "0.0.0.0:8080"},
				TaskManager: taskmanager.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "task_manager.type must be 'memory' or 'redis'",
		},
		{
			name: "redis task_manager without storage extension",
			config: &Config{
				HTTP:        HTTPConfig{Endpoint: "0.0.0.0:8080"},
				TaskManager: taskmanager.Config{Type: "redis"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required when task_manager.type is 'redis'",
		},
		{
			name: "invalid agent_registry type",
			config: &Config{
				HTTP:          HTTPConfig{Endpoint: "0.0.0.0:8080"},
				AgentRegistry: agentregistry.Config{Type: "invalid"},
			},
			wantErr: true,
			errMsg:  "agent_registry.type must be 'memory' or 'redis'",
		},
		{
			name: "redis agent_registry without storage extension",
			config: &Config{
				HTTP:          HTTPConfig{Endpoint: "0.0.0.0:8080"},
				AgentRegistry: agentregistry.Config{Type: "redis"},
			},
			wantErr: true,
			errMsg:  "storage_extension is required when agent_registry.type is 'redis'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()

	require.NotNil(t, cfg)

	// HTTP defaults
	assert.Equal(t, "0.0.0.0:8080", cfg.HTTP.Endpoint)
	assert.Equal(t, 30*time.Second, cfg.HTTP.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.HTTP.WriteTimeout)
	assert.Equal(t, 60*time.Second, cfg.HTTP.IdleTimeout)

	// CORS defaults
	assert.False(t, cfg.CORS.Enabled)
	assert.Contains(t, cfg.CORS.AllowedOrigins, "*")
	assert.Contains(t, cfg.CORS.AllowedMethods, "GET")
	assert.Contains(t, cfg.CORS.AllowedMethods, "POST")
	assert.Contains(t, cfg.CORS.AllowedHeaders, "Authorization")
	assert.Contains(t, cfg.CORS.AllowedHeaders, "Content-Type")
	assert.Equal(t, 86400, cfg.CORS.MaxAge)

	// Auth defaults
	assert.False(t, cfg.Auth.Enabled)
	assert.Equal(t, "X-API-Key", cfg.Auth.APIKey.Header)
}

func TestHTTPConfig(t *testing.T) {
	cfg := HTTPConfig{
		Endpoint:     "localhost:9090",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	assert.Equal(t, "localhost:9090", cfg.Endpoint)
	assert.Equal(t, 10*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 20*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 30*time.Second, cfg.IdleTimeout)
}

func TestCORSConfig(t *testing.T) {
	cfg := CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Authorization"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	assert.True(t, cfg.Enabled)
	assert.Contains(t, cfg.AllowedOrigins, "http://localhost:3000")
	assert.True(t, cfg.AllowCredentials)
	assert.Equal(t, 3600, cfg.MaxAge)
}

func TestAuthConfig(t *testing.T) {
	// Basic auth
	basicCfg := AuthConfig{
		Enabled: true,
		Type:    "basic",
		Basic: BasicAuthConfig{
			Username: "admin",
			Password: "secret",
		},
	}
	assert.Equal(t, "admin", basicCfg.Basic.Username)
	assert.Equal(t, "secret", basicCfg.Basic.Password)

	// JWT auth
	jwtCfg := AuthConfig{
		Enabled: true,
		Type:    "jwt",
		JWT: JWTAuthConfig{
			Secret:   "my-secret",
			Issuer:   "my-issuer",
			Audience: "my-audience",
		},
	}
	assert.Equal(t, "my-secret", jwtCfg.JWT.Secret)
	assert.Equal(t, "my-issuer", jwtCfg.JWT.Issuer)
	assert.Equal(t, "my-audience", jwtCfg.JWT.Audience)

	// API key auth
	apiKeyCfg := AuthConfig{
		Enabled: true,
		Type:    "api_key",
		APIKey: APIKeyAuthConfig{
			Header: "X-Custom-Key",
			Keys:   []string{"key1", "key2"},
		},
	}
	assert.Equal(t, "X-Custom-Key", apiKeyCfg.APIKey.Header)
	assert.Len(t, apiKeyCfg.APIKey.Keys, 2)
}
