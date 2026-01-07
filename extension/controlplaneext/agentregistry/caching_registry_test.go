// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCachingRegistry_GetAgentStats_CacheHit(t *testing.T) {
	// Create underlying memory registry
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	// Wrap with caching registry (5 second TTL)
	cacheTTL := 5 * time.Second
	cached := NewCachingRegistry(underlying, cacheTTL)

	ctx := context.Background()

	// Register an agent
	agent := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}
	require.NoError(t, cached.Register(ctx, agent))

	// First call should populate cache
	stats1, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats1.TotalAgents)
	assert.Equal(t, 1, stats1.OnlineAgents)

	// Register another agent directly to underlying (bypassing cache invalidation)
	agent2 := &AgentInfo{
		AgentID:     "agent-2",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-2",
	}
	require.NoError(t, underlying.Register(ctx, agent2))

	// Second call should return cached result (still showing 1 agent)
	stats2, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats2.TotalAgents) // Cache hit, doesn't see new agent
}

func TestCachingRegistry_GetAgentStats_CacheInvalidation(t *testing.T) {
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	cacheTTL := 5 * time.Second
	cached := NewCachingRegistry(underlying, cacheTTL)

	ctx := context.Background()

	// Register first agent
	agent1 := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}
	require.NoError(t, cached.Register(ctx, agent1))

	// Get stats (populates cache)
	stats1, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats1.TotalAgents)

	// Register second agent through cached registry (should invalidate cache)
	agent2 := &AgentInfo{
		AgentID:     "agent-2",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-2",
	}
	require.NoError(t, cached.Register(ctx, agent2))

	// Get stats again (should see both agents due to cache invalidation)
	stats2, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats2.TotalAgents)
}

func TestCachingRegistry_GetAgentStats_CacheExpiration(t *testing.T) {
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	// Very short TTL for testing
	cacheTTL := 50 * time.Millisecond
	cached := NewCachingRegistry(underlying, cacheTTL)

	ctx := context.Background()

	// Register first agent
	agent1 := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}
	require.NoError(t, cached.Register(ctx, agent1))

	// Get stats (populates cache)
	stats1, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats1.TotalAgents)

	// Register directly to underlying (bypassing cache invalidation)
	agent2 := &AgentInfo{
		AgentID:     "agent-2",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-2",
	}
	require.NoError(t, underlying.Register(ctx, agent2))

	// Wait for cache to expire
	time.Sleep(100 * time.Millisecond)

	// Get stats again (should refresh and see both agents)
	stats2, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats2.TotalAgents)
}

func TestCachingRegistry_GetAgentStats_DisabledCache(t *testing.T) {
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	// Zero TTL disables caching
	cached := NewCachingRegistry(underlying, 0)

	ctx := context.Background()

	// Register first agent
	agent1 := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}
	require.NoError(t, cached.Register(ctx, agent1))

	stats1, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats1.TotalAgents)

	// Register directly to underlying
	agent2 := &AgentInfo{
		AgentID:     "agent-2",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-2",
	}
	require.NoError(t, underlying.Register(ctx, agent2))

	// Should see both agents immediately (no caching)
	stats2, err := cached.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats2.TotalAgents)
}

func TestCachingRegistry_Singleflight(t *testing.T) {
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	cacheTTL := 5 * time.Second
	cached := NewCachingRegistry(underlying, cacheTTL)

	ctx := context.Background()

	// Register an agent
	agent := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}
	require.NoError(t, cached.Register(ctx, agent))

	// Invalidate cache to force refresh
	cached.InvalidateStatsCache()

	// Launch multiple concurrent requests
	const numGoroutines = 10
	var wg sync.WaitGroup
	var successCount atomic.Int32

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stats, err := cached.GetAgentStats(ctx)
			if err == nil && stats.TotalAgents == 1 {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// All goroutines should succeed
	assert.Equal(t, int32(numGoroutines), successCount.Load())
}

func TestCachingRegistry_WriteOperationsInvalidateCache(t *testing.T) {
	cfg := Config{
		Type:         "memory",
		HeartbeatTTL: 30 * time.Second,
	}
	underlying := NewMemoryAgentRegistry(zap.NewNop(), cfg)
	require.NoError(t, underlying.Start(context.Background()))
	defer underlying.Close()

	cacheTTL := 1 * time.Hour // Long TTL to ensure cache doesn't expire naturally
	cached := NewCachingRegistry(underlying, cacheTTL)

	ctx := context.Background()

	agent := &AgentInfo{
		AgentID:     "agent-1",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-1",
	}

	// Test Register invalidates cache
	require.NoError(t, cached.Register(ctx, agent))
	stats, _ := cached.GetAgentStats(ctx)
	assert.Equal(t, 1, stats.TotalAgents)

	// Test Unregister invalidates cache
	require.NoError(t, cached.Unregister(ctx, "agent-1"))
	stats, _ = cached.GetAgentStats(ctx)
	assert.Equal(t, 0, stats.TotalAgents)

	// Re-register for further tests
	require.NoError(t, cached.Register(ctx, agent))

	// Test Heartbeat invalidates cache
	require.NoError(t, cached.Heartbeat(ctx, "agent-1", nil))
	stats, _ = cached.GetAgentStats(ctx)
	assert.Equal(t, 1, stats.TotalAgents)

	// Test RegisterOrHeartbeat invalidates cache
	agent2 := &AgentInfo{
		AgentID:     "agent-2",
		AppID:       "app-1",
		ServiceName: "service-1",
		Hostname:    "host-2",
	}
	require.NoError(t, cached.RegisterOrHeartbeat(ctx, agent2))
	stats, _ = cached.GetAgentStats(ctx)
	assert.Equal(t, 2, stats.TotalAgents)
}
