// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty config is valid",
			config:  createDefaultConfig(),
			wantErr: false,
		},
		{
			name: "valid standalone redis config",
			config: &Config{
				Redis: map[string]RedisConfig{
					"default": {
						Addr: "localhost:6379",
					},
				},
				Nacos: make(map[string]NacosConfig),
			},
			wantErr: false,
		},
		{
			name: "valid cluster redis config",
			config: &Config{
				Redis: map[string]RedisConfig{
					"cluster": {
						Addrs: []string{"node1:6379", "node2:6379", "node3:6379"},
					},
				},
				Nacos: make(map[string]NacosConfig),
			},
			wantErr: false,
		},
		{
			name: "valid sentinel redis config",
			config: &Config{
				Redis: map[string]RedisConfig{
					"sentinel": {
						MasterName:    "mymaster",
						SentinelAddrs: []string{"sentinel1:26379", "sentinel2:26379"},
					},
				},
				Nacos: make(map[string]NacosConfig),
			},
			wantErr: false,
		},
		{
			name: "invalid redis config - no address",
			config: &Config{
				Redis: map[string]RedisConfig{
					"invalid": {},
				},
				Nacos: make(map[string]NacosConfig),
			},
			wantErr: true,
			errMsg:  "redis.invalid: one of addr, addrs, or sentinel configuration is required",
		},
		{
			name: "invalid redis config - multiple modes",
			config: &Config{
				Redis: map[string]RedisConfig{
					"invalid": {
						Addr:  "localhost:6379",
						Addrs: []string{"node1:6379"},
					},
				},
				Nacos: make(map[string]NacosConfig),
			},
			wantErr: true,
			errMsg:  "redis.invalid: only one of addr, addrs, or sentinel configuration should be specified",
		},
		{
			name: "valid nacos config",
			config: &Config{
				Redis: make(map[string]RedisConfig),
				Nacos: map[string]NacosConfig{
					"default": {
						ServerAddr: "nacos:8848",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid nacos config - no server address",
			config: &Config{
				Redis: make(map[string]RedisConfig),
				Nacos: map[string]NacosConfig{
					"invalid": {},
				},
			},
			wantErr: true,
			errMsg:  "nacos.invalid: server_addr is required",
		},
		{
			name: "valid mixed config",
			config: &Config{
				Redis: map[string]RedisConfig{
					"default": {Addr: "localhost:6379"},
					"backup":  {Addr: "localhost:6380"},
				},
				Nacos: map[string]NacosConfig{
					"default": {ServerAddr: "nacos:8848"},
				},
			},
			wantErr: false,
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

func TestRedisConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  RedisConfig
		wantErr bool
	}{
		{
			name:    "standalone mode",
			config:  RedisConfig{Addr: "localhost:6379"},
			wantErr: false,
		},
		{
			name:    "cluster mode",
			config:  RedisConfig{Addrs: []string{"node1:6379", "node2:6379"}},
			wantErr: false,
		},
		{
			name: "sentinel mode",
			config: RedisConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"sentinel:26379"},
			},
			wantErr: false,
		},
		{
			name:    "empty config",
			config:  RedisConfig{},
			wantErr: true,
		},
		{
			name: "sentinel without master name",
			config: RedisConfig{
				SentinelAddrs: []string{"sentinel:26379"},
			},
			wantErr: true,
		},
		{
			name: "sentinel without sentinel addrs",
			config: RedisConfig{
				MasterName: "mymaster",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNacosConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  NacosConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			config:  NacosConfig{ServerAddr: "nacos:8848"},
			wantErr: false,
		},
		{
			name: "valid config with all fields",
			config: NacosConfig{
				ServerAddr: "nacos:8848",
				Namespace:  "test-ns",
				Username:   "admin",
				Password:   "admin123",
				Timeout:    5 * time.Second,
				LogDir:     "/tmp/nacos/log",
				CacheDir:   "/tmp/nacos/cache",
				LogLevel:   "info",
			},
			wantErr: false,
		},
		{
			name:    "empty server address",
			config:  NacosConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()

	require.NotNil(t, cfg)
	assert.NotNil(t, cfg.Redis)
	assert.NotNil(t, cfg.Nacos)
	assert.Empty(t, cfg.Redis)
	assert.Empty(t, cfg.Nacos)
}
