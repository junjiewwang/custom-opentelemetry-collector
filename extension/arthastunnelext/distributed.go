// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/pending"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/proxy"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/registry"
)

// DistributedManager coordinates distributed Arthas tunnel operations.
// It manages agent registry, pending connections, and cross-node proxy.
type DistributedManager struct {
	logger *zap.Logger
	cfg    *Config

	nodeID   string
	nodeAddr string

	// Components
	registry        *registry.CompositeRegistry
	pendingStore    *pending.RedisPendingStore
	proxy           *proxy.WSProxy
	livenessUpdater *LivenessUpdater

	// Redis client (shared from storageext)
	redisClient redis.UniversalClient

	// Context for background goroutines
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewDistributedManager creates a new distributed manager.
func NewDistributedManager(
	ctx context.Context,
	logger *zap.Logger,
	cfg *Config,
	redisClient redis.UniversalClient,
	listenerPort int,
) *DistributedManager {
	nodeID := cfg.Distributed.ResolveNodeID()
	nodeAddr := cfg.Distributed.ResolveNodeAddr(listenerPort)

	logger.Info("Initializing distributed manager",
		zap.String("node_id", nodeID),
		zap.String("node_addr", nodeAddr),
		zap.String("redis_key_prefix", cfg.Distributed.GetKeyPrefix()),
	)

	dmCtx, cancel := context.WithCancel(ctx)

	dm := &DistributedManager{
		logger:      logger,
		cfg:         cfg,
		nodeID:      nodeID,
		nodeAddr:    nodeAddr,
		redisClient: redisClient,
		ctx:         dmCtx,
		cancel:      cancel,
	}

	// Create liveness checker
	livenessChecker := &defaultLivenessChecker{
		timeout: cfg.PongTimeout + cfg.LivenessGrace,
	}

	// Create local registry
	localRegistry := registry.NewLocalRegistry(logger, nodeID, nodeAddr, livenessChecker)

	// Create Redis registry
	redisRegistry := registry.NewRedisRegistry(
		logger,
		redisClient,
		cfg.Distributed.GetKeyPrefix(),
		cfg.Distributed.IndexTTL,
		livenessChecker,
	)

	// Create composite registry
	dm.registry = registry.NewCompositeRegistry(logger, localRegistry, redisRegistry, nodeID)

	// Create local pending store
	localPendingStore := pending.NewLocalPendingStore(logger, nodeID, nodeAddr)

	// Create Redis pending store
	dm.pendingStore = pending.NewRedisPendingStore(
		logger,
		redisClient,
		cfg.Distributed.GetKeyPrefix(),
		cfg.Distributed.PendingTTL,
		nodeID,
		nodeAddr,
		localPendingStore,
	)

	// Create cross-node proxy
	proxyConfig := &proxy.ProxyConfig{
		InternalPathPrefix:  cfg.Distributed.InternalPathPrefix,
		InternalToken:       cfg.Distributed.InternalAuth.Token,
		InternalTokenHeader: cfg.Distributed.InternalAuth.HeaderName,
		WriteTimeout:        int(cfg.Distributed.ProxyWriteTimeout.Seconds()),
		MaxProxySessions:    cfg.Distributed.MaxProxySessions,
	}
	// Use dm.pendingStore (RedisPendingStore) as the tunnel deliverer
	// This ensures that internal openTunnel requests can deliver to local pending
	dm.proxy = proxy.NewWSProxy(logger, proxyConfig, dm.pendingStore)

	// Create liveness updater for batched Redis updates
	dm.livenessUpdater = NewLivenessUpdater(
		logger,
		dm.registry,
		cfg.Distributed.LivenessUpdateInterval,
	)

	return dm
}

// Start starts background goroutines.
func (dm *DistributedManager) Start() {
	// Start liveness updater
	dm.wg.Add(1)
	go func() {
		defer dm.wg.Done()
		dm.livenessUpdater.Run(dm.ctx)
	}()

	// Start node heartbeat
	dm.wg.Add(1)
	go func() {
		defer dm.wg.Done()
		dm.runNodeHeartbeat()
	}()

	// Start stale agent cleanup
	dm.wg.Add(1)
	go func() {
		defer dm.wg.Done()
		dm.runStaleAgentCleanup()
	}()

	dm.logger.Info("Distributed manager started",
		zap.String("node_id", dm.nodeID),
		zap.String("node_addr", dm.nodeAddr),
	)
}

// Shutdown stops the distributed manager.
func (dm *DistributedManager) Shutdown(ctx context.Context) {
	dm.logger.Info("Shutting down distributed manager")

	// Cancel background goroutines
	dm.cancel()

	// Wait for goroutines to finish
	done := make(chan struct{})
	go func() {
		dm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		dm.logger.Warn("Distributed manager shutdown timed out")
	}

	// Unregister this node
	dm.unregisterNode()

	// Close components
	_ = dm.registry.Close()
	_ = dm.pendingStore.Close()
	_ = dm.proxy.Close()

	dm.logger.Info("Distributed manager stopped")
}

// Registry returns the agent registry.
func (dm *DistributedManager) Registry() *registry.CompositeRegistry {
	return dm.registry
}

// PendingStore returns the pending store.
func (dm *DistributedManager) PendingStore() *pending.RedisPendingStore {
	return dm.pendingStore
}

// Proxy returns the cross-node proxy.
func (dm *DistributedManager) Proxy() *proxy.WSProxy {
	return dm.proxy
}

// NodeID returns this node's ID.
func (dm *DistributedManager) NodeID() string {
	return dm.nodeID
}

// NodeAddr returns this node's address.
func (dm *DistributedManager) NodeAddr() string {
	return dm.nodeAddr
}

// RecordPong records a pong for batched liveness updates.
func (dm *DistributedManager) RecordPong(agentID string, t time.Time) {
	dm.livenessUpdater.RecordPong(agentID, t)
}

// runNodeHeartbeat periodically registers this node in Redis.
func (dm *DistributedManager) runNodeHeartbeat() {
	ticker := time.NewTicker(dm.cfg.Distributed.NodeHeartbeatInterval)
	defer ticker.Stop()

	// Register immediately
	dm.registerNode()

	for {
		select {
		case <-dm.ctx.Done():
			return
		case <-ticker.C:
			dm.registerNode()
		}
	}
}

func (dm *DistributedManager) registerNode() {
	key := dm.cfg.Distributed.GetKeyPrefix() + "nodes:" + dm.nodeID
	ttl := dm.cfg.Distributed.NodeHeartbeatInterval * 3 // 3x heartbeat interval

	err := dm.redisClient.Set(dm.ctx, key, dm.nodeAddr, ttl).Err()
	if err != nil {
		dm.logger.Warn("Failed to register node heartbeat",
			zap.String("node_id", dm.nodeID),
			zap.Error(err),
		)
	}
}

func (dm *DistributedManager) unregisterNode() {
	key := dm.cfg.Distributed.GetKeyPrefix() + "nodes:" + dm.nodeID
	_ = dm.redisClient.Del(context.Background(), key).Err()
}

// runStaleAgentCleanup periodically cleans up agents from dead nodes.
func (dm *DistributedManager) runStaleAgentCleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-dm.ctx.Done():
			return
		case <-ticker.C:
			dm.cleanupStaleAgents()
		}
	}
}

func (dm *DistributedManager) cleanupStaleAgents() {
	// Get all alive nodes
	pattern := dm.cfg.Distributed.GetKeyPrefix() + "nodes:*"
	keys, err := dm.redisClient.Keys(dm.ctx, pattern).Result()
	if err != nil {
		dm.logger.Warn("Failed to get node keys", zap.Error(err))
		return
	}

	aliveNodes := make(map[string]bool)
	for _, key := range keys {
		// Extract node ID from key
		nodeID := key[len(dm.cfg.Distributed.GetKeyPrefix()+"nodes:"):]
		aliveNodes[nodeID] = true
	}

	// Cleanup agents from dead nodes
	redisRegistry := dm.registry.GetRedisRegistry()
	if redisRegistry != nil {
		if err := redisRegistry.CleanupStaleAgents(dm.ctx, aliveNodes); err != nil {
			dm.logger.Warn("Failed to cleanup stale agents", zap.Error(err))
		}

		// Also cleanup expired agents
		if err := redisRegistry.CleanupExpiredAgents(dm.ctx); err != nil {
			dm.logger.Warn("Failed to cleanup expired agents", zap.Error(err))
		}
	}
}

// IsAgentOnThisNode checks if an agent is connected to this node.
func (dm *DistributedManager) IsAgentOnThisNode(agentID string) bool {
	return dm.registry.IsLocal(agentID)
}

// GetAgentNodeAddr returns the node address where an agent is connected.
func (dm *DistributedManager) GetAgentNodeAddr(ctx context.Context, agentID string) (string, error) {
	return dm.registry.GetNodeAddr(ctx, agentID)
}

// defaultLivenessChecker implements registry.LivenessChecker.
type defaultLivenessChecker struct {
	timeout time.Duration
}

func (c *defaultLivenessChecker) IsTimeout(lastPongAtMilli int64) bool {
	return time.Since(time.UnixMilli(lastPongAtMilli)) > c.timeout
}

func (c *defaultLivenessChecker) LivenessTimeout() time.Duration {
	return c.timeout
}

// LivenessUpdater batches liveness updates to Redis.
type LivenessUpdater struct {
	logger   *zap.Logger
	registry *registry.CompositeRegistry
	interval time.Duration

	mu      sync.Mutex
	pending map[string]time.Time // agentID -> lastPongAt
}

// NewLivenessUpdater creates a new liveness updater.
func NewLivenessUpdater(
	logger *zap.Logger,
	registry *registry.CompositeRegistry,
	interval time.Duration,
) *LivenessUpdater {
	return &LivenessUpdater{
		logger:   logger,
		registry: registry,
		interval: interval,
		pending:  make(map[string]time.Time),
	}
}

// RecordPong records a pong for later batch update.
func (u *LivenessUpdater) RecordPong(agentID string, t time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Only update if newer (monotonic)
	if existing, ok := u.pending[agentID]; !ok || t.After(existing) {
		u.pending[agentID] = t
	}
}

// Run starts the periodic flush loop.
func (u *LivenessUpdater) Run(ctx context.Context) {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush
			u.flush(context.Background())
			return
		case <-ticker.C:
			u.flush(ctx)
		}
	}
}

func (u *LivenessUpdater) flush(ctx context.Context) {
	u.mu.Lock()
	batch := u.pending
	u.pending = make(map[string]time.Time)
	u.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	for agentID, lastPongAt := range batch {
		if err := u.registry.UpdateLivenessRedis(ctx, agentID, lastPongAt); err != nil {
			u.logger.Warn("Failed to update liveness in Redis",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}

	u.logger.Debug("Flushed liveness updates to Redis",
		zap.Int("count", len(batch)),
	)
}
