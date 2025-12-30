// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func newTestMemoryConfigManager(t *testing.T) *MemoryConfigManager {
	logger := zap.NewNop()
	return NewMemoryConfigManager(logger)
}

func TestMemoryConfigManager_GetConfig_Empty(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	config, err := cm.GetConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestMemoryConfigManager_UpdateConfig(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	config := &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.5,
		},
	}

	err = cm.UpdateConfig(ctx, config)
	require.NoError(t, err)

	// Verify
	retrieved, err := cm.GetConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "v1.0", retrieved.ConfigVersion)
	assert.Equal(t, 0.5, retrieved.Sampler.Ratio)
}

func TestMemoryConfigManager_UpdateConfig_Validation(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	// Nil config
	err = cm.UpdateConfig(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")

	// Empty version
	err = cm.UpdateConfig(ctx, &controlplanev1.AgentConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config_version is required")

	// Invalid sampler ratio
	err = cm.UpdateConfig(ctx, &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 1.5, // Invalid
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ratio must be between 0 and 1")

	// Invalid batch config
	err = cm.UpdateConfig(ctx, &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
		Batch: &controlplanev1.BatchConfig{
			MaxExportBatchSize: -1,
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be non-negative")
}

func TestMemoryConfigManager_Subscribe(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	var mu sync.Mutex
	var callCount int
	var lastOld, lastNew *controlplanev1.AgentConfig

	cm.Subscribe(func(old, new *controlplanev1.AgentConfig) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		lastOld = old
		lastNew = new
	})

	// First update
	config1 := &controlplanev1.AgentConfig{ConfigVersion: "v1.0"}
	_ = cm.UpdateConfig(ctx, config1)

	mu.Lock()
	assert.Equal(t, 1, callCount)
	assert.Nil(t, lastOld)
	assert.Equal(t, "v1.0", lastNew.ConfigVersion)
	mu.Unlock()

	// Second update
	config2 := &controlplanev1.AgentConfig{ConfigVersion: "v2.0"}
	_ = cm.UpdateConfig(ctx, config2)

	mu.Lock()
	assert.Equal(t, 2, callCount)
	assert.Equal(t, "v1.0", lastOld.ConfigVersion)
	assert.Equal(t, "v2.0", lastNew.ConfigVersion)
	mu.Unlock()
}

func TestMemoryConfigManager_Watch(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	var called bool
	err = cm.Watch(ctx, func(old, new *controlplanev1.AgentConfig) {
		called = true
	})
	require.NoError(t, err)

	// Update should trigger callback
	_ = cm.UpdateConfig(ctx, &controlplanev1.AgentConfig{ConfigVersion: "v1.0"})
	assert.True(t, called)
}

func TestMemoryConfigManager_StopWatch(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	// StopWatch is a no-op for memory implementation
	err = cm.StopWatch()
	require.NoError(t, err)
}

func TestMemoryConfigManager_StartClose(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	// Start
	err := cm.Start(ctx)
	require.NoError(t, err)

	// Double start
	err = cm.Start(ctx)
	require.NoError(t, err)

	// Close
	err = cm.Close()
	require.NoError(t, err)

	// Verify subscribers are cleared
	assert.Nil(t, cm.subscribers)
}

func TestMemoryConfigManager_ConcurrentAccess(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	// Add subscriber
	cm.Subscribe(func(old, new *controlplanev1.AgentConfig) {})

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			config := &controlplanev1.AgentConfig{
				ConfigVersion: "v" + string(rune('0'+id)),
			}
			_ = cm.UpdateConfig(ctx, config)
			_, _ = cm.GetConfig(ctx)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMemoryConfigManager_MultipleSubscribers(t *testing.T) {
	cm := newTestMemoryConfigManager(t)
	ctx := context.Background()

	err := cm.Start(ctx)
	require.NoError(t, err)
	defer cm.Close()

	var mu sync.Mutex
	counts := make([]int, 3)

	for i := 0; i < 3; i++ {
		idx := i
		cm.Subscribe(func(old, new *controlplanev1.AgentConfig) {
			mu.Lock()
			counts[idx]++
			mu.Unlock()
		})
	}

	// Update config
	_ = cm.UpdateConfig(ctx, &controlplanev1.AgentConfig{ConfigVersion: "v1.0"})

	mu.Lock()
	for i := 0; i < 3; i++ {
		assert.Equal(t, 1, counts[i], "subscriber %d should be called once", i)
	}
	mu.Unlock()
}
