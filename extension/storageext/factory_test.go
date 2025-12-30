// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension"
)

func newTestSettings() extension.Settings {
	return extension.Settings{
		ID:                component.NewIDWithName(Type, "test"),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}
}

func TestNewFactory(t *testing.T) {
	factory := NewFactory()

	require.NotNil(t, factory)
	assert.Equal(t, Type, factory.Type())
}

func TestFactory_CreateDefaultConfig(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()

	require.NotNil(t, cfg)
	storageConfig, ok := cfg.(*Config)
	require.True(t, ok)

	assert.NotNil(t, storageConfig.Redis)
	assert.NotNil(t, storageConfig.Nacos)
}

func TestFactory_CreateExtension(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()
	set := newTestSettings()

	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)

	// Verify it implements Storage interface
	_, ok := ext.(Storage)
	assert.True(t, ok)
}

func TestFactory_CreateExtension_InvalidConfig(t *testing.T) {
	factory := NewFactory()
	set := newTestSettings()

	// Create config with invalid redis settings
	cfg := &Config{
		Redis: map[string]RedisConfig{
			"invalid": {}, // Missing address
		},
		Nacos: make(map[string]NacosConfig),
	}

	// Factory should still create the extension (validation happens at Start)
	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)
}

func TestFactory_Type(t *testing.T) {
	factory := NewFactory()
	assert.Equal(t, Type, factory.Type())
}

func TestFactory_CreateExtension_WithValidConfig(t *testing.T) {
	factory := NewFactory()
	set := newTestSettings()

	cfg := &Config{
		Redis: map[string]RedisConfig{
			"test": {
				Addr: "localhost:6379",
			},
		},
		Nacos: map[string]NacosConfig{
			"test": {
				ServerAddr: "localhost:8848",
			},
		},
	}

	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)

	// Cast to Extension to verify internal state
	storageExt, ok := ext.(*Extension)
	require.True(t, ok)
	assert.Equal(t, cfg, storageExt.config)
}

func TestFactory_CreateExtension_StartFailsWithInvalidRedis(t *testing.T) {
	set := extension.Settings{
		ID:                component.NewIDWithName(Type, "test"),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}

	cfg := &Config{
		Redis: map[string]RedisConfig{
			"test": {
				Addr: "invalid-host:6379", // Will fail to connect
			},
		},
		Nacos: make(map[string]NacosConfig),
	}

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	// Start should fail because Redis connection will fail
	err = ext.Start(context.Background(), componenttest.NewNopHost())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to redis")
}
