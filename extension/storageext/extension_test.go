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

func newExtensionTestSettings() extension.Settings {
	return extension.Settings{
		ID:                component.NewIDWithName(Type, "test"),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}
}

func TestExtension_NewStorageExtension(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)

	assert.Equal(t, cfg, ext.config)
	assert.NotNil(t, ext.redisClients)
	assert.NotNil(t, ext.nacosConfigClients)
	assert.NotNil(t, ext.nacosNamingClients)
	assert.False(t, ext.started)
}

func TestExtension_StartShutdown_EmptyConfig(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	// Start with empty config should succeed
	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	assert.True(t, ext.started)

	// Double start should be idempotent
	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	// Shutdown should succeed
	err = ext.Shutdown(context.Background())
	require.NoError(t, err)
	assert.False(t, ext.started)

	// Double shutdown should be idempotent
	err = ext.Shutdown(context.Background())
	require.NoError(t, err)
}

func TestExtension_GetRedis_NotFound(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	// Get non-existent client
	client, err := ext.GetRedis("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "not found")

	// GetDefaultRedis should also fail
	client, err = ext.GetDefaultRedis()
	assert.Error(t, err)
	assert.Nil(t, client)

	_ = ext.Shutdown(context.Background())
}

func TestExtension_GetNacosConfigClient_NotFound(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	// Get non-existent client
	client, err := ext.GetNacosConfigClient("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "not found")

	// GetDefaultNacosConfigClient should also fail
	client, err = ext.GetDefaultNacosConfigClient()
	assert.Error(t, err)
	assert.Nil(t, client)

	_ = ext.Shutdown(context.Background())
}

func TestExtension_GetNacosNamingClient_NotFound(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	// Get non-existent client
	client, err := ext.GetNacosNamingClient("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "not found")

	// GetDefaultNacosNamingClient should also fail
	client, err = ext.GetDefaultNacosNamingClient()
	assert.Error(t, err)
	assert.Nil(t, client)

	_ = ext.Shutdown(context.Background())
}

func TestExtension_HasRedis(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	assert.False(t, ext.HasRedis("default"))
	assert.False(t, ext.HasRedis("nonexistent"))

	_ = ext.Shutdown(context.Background())
}

func TestExtension_HasNacos(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	assert.False(t, ext.HasNacos("default"))
	assert.False(t, ext.HasNacos("nonexistent"))

	_ = ext.Shutdown(context.Background())
}

func TestExtension_ListRedisNames(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	names := ext.ListRedisNames()
	assert.Empty(t, names)

	_ = ext.Shutdown(context.Background())
}

func TestExtension_ListNacosNames(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	names := ext.ListNacosNames()
	assert.Empty(t, names)

	_ = ext.Shutdown(context.Background())
}

func TestExtension_ConcurrentAccess(t *testing.T) {
	cfg := createDefaultConfig()
	set := newExtensionTestSettings()

	ext, err := newStorageExtension(context.Background(), set, cfg)
	require.NoError(t, err)

	err = ext.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	// Concurrent reads should be safe
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = ext.HasRedis("default")
			_ = ext.HasNacos("default")
			_ = ext.ListRedisNames()
			_ = ext.ListNacosNames()
			_, _ = ext.GetRedis("default")
			_, _ = ext.GetNacosConfigClient("default")
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	_ = ext.Shutdown(context.Background())
}
