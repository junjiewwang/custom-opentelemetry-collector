// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// CachingRegistry wraps an AgentRegistry and provides caching for GetAgentStats.
// It implements the AgentRegistry interface using the decorator pattern.
//
// Design principles:
//   - Decorator pattern: wraps any AgentRegistry implementation transparently
//   - Singleflight: prevents cache stampede under high concurrency
//   - Write-through invalidation: cache is invalidated on write operations
//   - Thread-safe: all operations are safe for concurrent use
type CachingRegistry struct {
	AgentRegistry // Embed the underlying registry (delegation)

	statsCacheTTL time.Duration

	// Stats cache using atomic for lock-free reads
	statsCache     atomic.Pointer[AgentStats]
	statsCacheTime atomic.Int64 // Unix nano timestamp

	// Singleflight for stats refresh to prevent cache stampede
	statsRefreshMu   sync.Mutex
	statsRefreshing  bool
	statsRefreshCond *sync.Cond
}

// NewCachingRegistry creates a new CachingRegistry wrapping the given registry.
// If cacheTTL is 0 or negative, caching is disabled and all calls pass through directly.
func NewCachingRegistry(registry AgentRegistry, cacheTTL time.Duration) *CachingRegistry {
	c := &CachingRegistry{
		AgentRegistry: registry,
		statsCacheTTL: cacheTTL,
	}
	c.statsRefreshCond = sync.NewCond(&c.statsRefreshMu)
	return c
}

// GetAgentStats returns cached stats if valid, otherwise refreshes from underlying registry.
// Uses singleflight semantics to prevent multiple concurrent refreshes.
func (c *CachingRegistry) GetAgentStats(ctx context.Context) (*AgentStats, error) {
	// If caching is disabled, pass through directly
	if c.statsCacheTTL <= 0 {
		return c.AgentRegistry.GetAgentStats(ctx)
	}

	// Fast path: check cache (lock-free read)
	if stats := c.getCachedStats(); stats != nil {
		return stats, nil
	}

	// Slow path: refresh with singleflight semantics
	return c.refreshStats(ctx)
}

// getCachedStats returns cached stats if still valid, nil otherwise.
func (c *CachingRegistry) getCachedStats() *AgentStats {
	cacheTime := c.statsCacheTime.Load()
	if cacheTime == 0 {
		return nil
	}

	elapsed := time.Since(time.Unix(0, cacheTime))
	if elapsed >= c.statsCacheTTL {
		return nil
	}

	return c.statsCache.Load()
}

// refreshStats refreshes stats with singleflight semantics.
// Only one goroutine refreshes at a time; others wait and reuse the result.
func (c *CachingRegistry) refreshStats(ctx context.Context) (*AgentStats, error) {
	c.statsRefreshMu.Lock()

	// Double-check cache after acquiring lock
	if stats := c.getCachedStats(); stats != nil {
		c.statsRefreshMu.Unlock()
		return stats, nil
	}

	// If another goroutine is refreshing, wait for it
	for c.statsRefreshing {
		c.statsRefreshCond.Wait()
		// After waking up, check if cache is now valid
		if stats := c.getCachedStats(); stats != nil {
			c.statsRefreshMu.Unlock()
			return stats, nil
		}
	}

	// Mark as refreshing
	c.statsRefreshing = true
	c.statsRefreshMu.Unlock()

	// Fetch from underlying registry (outside lock)
	stats, err := c.AgentRegistry.GetAgentStats(ctx)

	c.statsRefreshMu.Lock()
	c.statsRefreshing = false

	if err == nil && stats != nil {
		c.statsCache.Store(stats)
		c.statsCacheTime.Store(time.Now().UnixNano())
	}

	// Wake up all waiting goroutines
	c.statsRefreshCond.Broadcast()
	c.statsRefreshMu.Unlock()

	return stats, err
}

// InvalidateStatsCache invalidates the stats cache.
// This is called internally after write operations that affect stats.
func (c *CachingRegistry) InvalidateStatsCache() {
	c.statsCacheTime.Store(0)
}

// ============================================================================
// Override write methods to invalidate cache
// ============================================================================

// Register registers a new agent and invalidates the stats cache.
func (c *CachingRegistry) Register(ctx context.Context, agent *AgentInfo) error {
	err := c.AgentRegistry.Register(ctx, agent)
	if err == nil {
		c.InvalidateStatsCache()
	}
	return err
}

// Unregister removes an agent and invalidates the stats cache.
func (c *CachingRegistry) Unregister(ctx context.Context, agentID string) error {
	err := c.AgentRegistry.Unregister(ctx, agentID)
	if err == nil {
		c.InvalidateStatsCache()
	}
	return err
}

// Heartbeat updates heartbeat and may change online/offline status, so invalidate cache.
func (c *CachingRegistry) Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error {
	err := c.AgentRegistry.Heartbeat(ctx, agentID, status)
	if err == nil {
		c.InvalidateStatsCache()
	}
	return err
}

// RegisterOrHeartbeat may register new agent or update status, so invalidate cache.
func (c *CachingRegistry) RegisterOrHeartbeat(ctx context.Context, agent *AgentInfo) error {
	err := c.AgentRegistry.RegisterOrHeartbeat(ctx, agent)
	if err == nil {
		c.InvalidateStatsCache()
	}
	return err
}

// UpdateHealth updates health status which may affect unhealthy count, so invalidate cache.
func (c *CachingRegistry) UpdateHealth(ctx context.Context, agentID string, health *controlplanev1.HealthStatus) error {
	err := c.AgentRegistry.UpdateHealth(ctx, agentID, health)
	if err == nil {
		c.InvalidateStatsCache()
	}
	return err
}
