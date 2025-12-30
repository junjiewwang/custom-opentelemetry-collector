// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenauthprocessor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
)

// tokenAuthProcessor implements the token authentication processor.
type tokenAuthProcessor struct {
	config   *Config
	logger   *zap.Logger
	settings processor.Settings

	// Control plane for token validation
	controlPlane controlplaneext.ControlPlane

	// Next consumers
	tracesConsumer  consumer.Traces
	metricsConsumer consumer.Metrics
	logsConsumer    consumer.Logs

	// Token cache with TTL support
	cache *tokenCache

	// Lifecycle
	started    bool
	stopCh     chan struct{}
	cleanupWg  sync.WaitGroup
}

// cacheEntry holds cached token validation result with expiration.
type cacheEntry struct {
	valid     bool
	appID     string
	appName   string
	expiresAt time.Time
	createdAt time.Time
}

// isExpired checks if the cache entry has expired.
func (e *cacheEntry) isExpired() bool {
	return time.Now().After(e.expiresAt)
}

// tokenCache is a thread-safe cache with TTL and size limit.
type tokenCache struct {
	entries    map[string]*cacheEntry
	mu         sync.RWMutex
	maxSize    int
	validTTL   time.Duration
	invalidTTL time.Duration

	// Metrics
	hits   atomic.Int64
	misses atomic.Int64
	evicts atomic.Int64
}

// newTokenCache creates a new token cache with the given configuration.
func newTokenCache(cfg CacheConfig) *tokenCache {
	return &tokenCache{
		entries:    make(map[string]*cacheEntry),
		maxSize:    cfg.MaxSize,
		validTTL:   time.Duration(cfg.ValidTTL) * time.Second,
		invalidTTL: time.Duration(cfg.InvalidTTL) * time.Second,
	}
}

// get retrieves a cache entry if it exists and is not expired.
func (c *tokenCache) get(token string) (*cacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[token]
	c.mu.RUnlock()

	if !ok {
		c.misses.Add(1)
		return nil, false
	}

	if entry.isExpired() {
		c.misses.Add(1)
		// Don't delete here to avoid write lock contention, cleanup goroutine will handle it
		return nil, false
	}

	c.hits.Add(1)
	return entry, true
}

// set stores a cache entry with appropriate TTL based on validity.
func (c *tokenCache) set(token string, valid bool, appID, appName string) {
	ttl := c.invalidTTL
	if valid {
		ttl = c.validTTL
	}

	now := time.Now()
	entry := &cacheEntry{
		valid:     valid,
		appID:     appID,
		appName:   appName,
		expiresAt: now.Add(ttl),
		createdAt: now,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check size limit and evict oldest if necessary
	if len(c.entries) >= c.maxSize {
		c.evictOldest()
	}

	c.entries[token] = entry
}

// evictOldest removes the oldest entry from the cache.
// Must be called with write lock held.
func (c *tokenCache) evictOldest() {
	var oldestToken string
	var oldestTime time.Time

	for token, entry := range c.entries {
		if oldestToken == "" || entry.createdAt.Before(oldestTime) {
			oldestToken = token
			oldestTime = entry.createdAt
		}
	}

	if oldestToken != "" {
		delete(c.entries, oldestToken)
		c.evicts.Add(1)
	}
}

// cleanup removes all expired entries from the cache.
func (c *tokenCache) cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for token, entry := range c.entries {
		if entry.isExpired() {
			delete(c.entries, token)
			count++
		}
	}
	return count
}

// invalidate removes a specific token from the cache.
func (c *tokenCache) invalidate(token string) {
	c.mu.Lock()
	delete(c.entries, token)
	c.mu.Unlock()
}

// clear removes all entries from the cache.
func (c *tokenCache) clear() {
	c.mu.Lock()
	c.entries = make(map[string]*cacheEntry)
	c.mu.Unlock()
}

// stats returns cache statistics.
func (c *tokenCache) stats() (hits, misses, evicts int64, size int) {
	c.mu.RLock()
	size = len(c.entries)
	c.mu.RUnlock()
	return c.hits.Load(), c.misses.Load(), c.evicts.Load(), size
}

// newTokenAuthProcessor creates a new token auth processor.
func newTokenAuthProcessor(
	set processor.Settings,
	cfg *Config,
	tracesConsumer consumer.Traces,
	metricsConsumer consumer.Metrics,
	logsConsumer consumer.Logs,
) (*tokenAuthProcessor, error) {
	var cache *tokenCache
	if cfg.Cache.Enabled {
		cache = newTokenCache(cfg.Cache)
	}

	return &tokenAuthProcessor{
		config:          cfg,
		logger:          set.Logger,
		settings:        set,
		tracesConsumer:  tracesConsumer,
		metricsConsumer: metricsConsumer,
		logsConsumer:    logsConsumer,
		cache:           cache,
		stopCh:          make(chan struct{}),
	}, nil
}

// Start implements component.Component.
func (p *tokenAuthProcessor) Start(ctx context.Context, host component.Host) error {
	// Find control plane extension
	if err := p.findControlPlaneExtension(host); err != nil {
		p.logger.Warn("Control plane extension not found, token validation will fail all requests",
			zap.String("extension", p.config.ControlPlaneExtension),
			zap.Error(err))
	}

	// Start cache cleanup goroutine if cache is enabled
	if p.cache != nil && p.config.Cache.CleanupInterval > 0 {
		p.cleanupWg.Add(1)
		go p.runCacheCleanup()
	}

	p.started = true
	p.logger.Info("Token auth processor started",
		zap.String("attribute_key", p.config.AttributeKey),
		zap.String("action", p.config.Action),
		zap.Bool("cache_enabled", p.config.Cache.Enabled),
		zap.Int("cache_valid_ttl_seconds", p.config.Cache.ValidTTL),
		zap.Int("cache_invalid_ttl_seconds", p.config.Cache.InvalidTTL),
	)
	return nil
}

// runCacheCleanup runs the background cache cleanup goroutine.
func (p *tokenAuthProcessor) runCacheCleanup() {
	defer p.cleanupWg.Done()

	ticker := time.NewTicker(time.Duration(p.config.Cache.CleanupInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.cache != nil {
				cleaned := p.cache.cleanup()
				if cleaned > 0 {
					p.logger.Debug("Cache cleanup completed",
						zap.Int("entries_removed", cleaned))
				}
			}
		case <-p.stopCh:
			return
		}
	}
}

// findControlPlaneExtension finds the control plane extension from host.
func (p *tokenAuthProcessor) findControlPlaneExtension(host component.Host) error {
	extType := component.MustNewType(p.config.ControlPlaneExtension)

	for id, ext := range host.GetExtensions() {
		if id.Type() == extType {
			if cp, ok := ext.(controlplaneext.ControlPlane); ok {
				p.controlPlane = cp
				p.logger.Info("Found control plane extension", zap.String("id", id.String()))
				return nil
			}
		}
	}
	return nil
}

// Shutdown implements component.Component.
func (p *tokenAuthProcessor) Shutdown(ctx context.Context) error {
	// Stop cleanup goroutine
	close(p.stopCh)
	p.cleanupWg.Wait()

	// Log cache statistics
	if p.cache != nil {
		hits, misses, evicts, size := p.cache.stats()
		p.logger.Info("Token auth processor shutdown",
			zap.Int64("cache_hits", hits),
			zap.Int64("cache_misses", misses),
			zap.Int64("cache_evicts", evicts),
			zap.Int("cache_size", size),
		)
	} else {
		p.logger.Info("Token auth processor shutdown (cache disabled)")
	}
	return nil
}

// Capabilities implements processor.Traces/Metrics/Logs.
func (p *tokenAuthProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces implements processor.Traces.
func (p *tokenAuthProcessor) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if p.config.Action == "pass" {
		return p.tracesConsumer.ConsumeTraces(ctx, td)
	}

	// Group resources by token validation result
	validTraces := ptrace.NewTraces()
	invalidCount := 0

	resourceSpans := td.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		token := extractTokenFromAttributes(rs.Resource().Attributes(), p.config.AttributeKey)

		if p.validateToken(ctx, token) {
			// Copy valid resource spans to output
			newRS := validTraces.ResourceSpans().AppendEmpty()
			rs.CopyTo(newRS)
		} else {
			invalidCount += rs.ScopeSpans().Len()
		}
	}

	// Log dropped data if configured
	if p.config.LogDropped && invalidCount > 0 {
		p.logger.Warn("Dropped traces due to invalid token",
			zap.Int("dropped_scope_spans", invalidCount),
		)
	}

	// If no valid traces, return without calling next consumer
	if validTraces.ResourceSpans().Len() == 0 {
		return nil
	}

	return p.tracesConsumer.ConsumeTraces(ctx, validTraces)
}

// ConsumeMetrics implements processor.Metrics.
func (p *tokenAuthProcessor) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if p.config.Action == "pass" {
		return p.metricsConsumer.ConsumeMetrics(ctx, md)
	}

	// Group resources by token validation result
	validMetrics := pmetric.NewMetrics()
	invalidCount := 0

	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		token := extractTokenFromAttributes(rm.Resource().Attributes(), p.config.AttributeKey)

		if p.validateToken(ctx, token) {
			// Copy valid resource metrics to output
			newRM := validMetrics.ResourceMetrics().AppendEmpty()
			rm.CopyTo(newRM)
		} else {
			invalidCount += rm.ScopeMetrics().Len()
		}
	}

	// Log dropped data if configured
	if p.config.LogDropped && invalidCount > 0 {
		p.logger.Warn("Dropped metrics due to invalid token",
			zap.Int("dropped_scope_metrics", invalidCount),
		)
	}

	// If no valid metrics, return without calling next consumer
	if validMetrics.ResourceMetrics().Len() == 0 {
		return nil
	}

	return p.metricsConsumer.ConsumeMetrics(ctx, validMetrics)
}

// ConsumeLogs implements processor.Logs.
func (p *tokenAuthProcessor) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if p.config.Action == "pass" {
		return p.logsConsumer.ConsumeLogs(ctx, ld)
	}

	// Group resources by token validation result
	validLogs := plog.NewLogs()
	invalidCount := 0

	resourceLogs := ld.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		rl := resourceLogs.At(i)
		token := extractTokenFromAttributes(rl.Resource().Attributes(), p.config.AttributeKey)

		if p.validateToken(ctx, token) {
			// Copy valid resource logs to output
			newRL := validLogs.ResourceLogs().AppendEmpty()
			rl.CopyTo(newRL)
		} else {
			invalidCount += rl.ScopeLogs().Len()
		}
	}

	// Log dropped data if configured
	if p.config.LogDropped && invalidCount > 0 {
		p.logger.Warn("Dropped logs due to invalid token",
			zap.Int("dropped_scope_logs", invalidCount),
		)
	}

	// If no valid logs, return without calling next consumer
	if validLogs.ResourceLogs().Len() == 0 {
		return nil
	}

	return p.logsConsumer.ConsumeLogs(ctx, validLogs)
}

// extractTokenFromAttributes extracts the token from resource attributes.
func extractTokenFromAttributes(attrs pcommon.Map, key string) string {
	if val, ok := attrs.Get(key); ok {
		return val.AsString()
	}
	return ""
}

// validateToken validates the token using the control plane extension.
// Uses a TTL-based cache to avoid repeated validation calls.
func (p *tokenAuthProcessor) validateToken(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}

	// Check cache first (if enabled)
	if p.cache != nil {
		if entry, ok := p.cache.get(token); ok {
			return entry.valid
		}
	}

	// Validate with control plane
	if p.controlPlane == nil {
		p.logger.Debug("Token validation failed: control plane not available")
		return false
	}

	result, err := p.controlPlane.ValidateToken(ctx, token)
	if err != nil {
		p.logger.Error("Token validation error", zap.Error(err))
		return false
	}

	// Cache the result (if enabled)
	if p.cache != nil {
		p.cache.set(token, result.Valid, result.AppID, result.AppName)
	}

	if !result.Valid {
		p.logger.Debug("Token validation failed",
			zap.String("reason", result.Reason),
		)
	}

	return result.Valid
}

// ClearCache clears the token validation cache.
// This can be called when tokens are regenerated or invalidated.
func (p *tokenAuthProcessor) ClearCache() {
	if p.cache != nil {
		p.cache.clear()
		p.logger.Info("Token cache cleared")
	}
}

// InvalidateToken removes a specific token from the cache.
func (p *tokenAuthProcessor) InvalidateToken(token string) {
	if p.cache != nil {
		p.cache.invalidate(token)
	}
}
