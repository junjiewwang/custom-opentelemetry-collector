// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/registry"
)

// newMiniRedis starts a fresh miniredis and returns a connected client. The
// miniredis + client are cleaned up automatically.
func newMiniRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newTestDistributedConfig returns a distributed-enabled config with short
// intervals and a static advertised address so nodeID/nodeAddr are deterministic.
func newTestDistributedConfig() *Config {
	cfg := createDefaultConfig()
	cfg.Distributed.Enabled = true
	cfg.Distributed.NodeID = "test-node-1"
	cfg.Distributed.InternalAuth.Token = "test-token"
	cfg.Distributed.Advertise.Mode = "static"
	cfg.Distributed.Advertise.StaticAddr = "test-host:9000"
	cfg.Distributed.KeyPrefix = "arthas:tunnel:"
	cfg.Distributed.NodeHeartbeatInterval = 100 * time.Millisecond
	cfg.Distributed.LivenessUpdateInterval = 50 * time.Millisecond
	cfg.Distributed.IndexTTL = 5 * time.Minute
	cfg.Distributed.PendingTTL = 5 * time.Minute
	return cfg
}

// ── defaultLivenessChecker ─────────────────────────────────────────────

func TestDefaultLivenessChecker(t *testing.T) {
	c := &defaultLivenessChecker{timeout: 1500 * time.Millisecond}
	assert.Equal(t, 1500*time.Millisecond, c.LivenessTimeout())

	// Recent pong → alive.
	assert.False(t, c.IsTimeout(time.Now().UnixMilli()))

	// Old pong → timed out.
	assert.True(t, c.IsTimeout(time.Now().Add(-10*time.Second).UnixMilli()))
}

// ── LivenessUpdater ────────────────────────────────────────────────────

// newTestComposite builds a CompositeRegistry over miniredis using the
// distributed manager's default liveness checker, for LivenessUpdater tests.
func newTestComposite(t *testing.T) *registry.CompositeRegistry {
	t.Helper()
	client := newMiniRedis(t)
	checker := &defaultLivenessChecker{timeout: 1500 * time.Millisecond}
	local := registry.NewLocalRegistry(zap.NewNop(), "test-node-1", "test-host:9000", checker)
	redisReg := registry.NewRedisRegistry(zap.NewNop(), client, "arthas:tunnel:", 5*time.Minute, checker)
	return registry.NewCompositeRegistry(zap.NewNop(), local, redisReg, "test-node-1")
}

func TestLivenessUpdater_RecordPongMonotonic(t *testing.T) {
	u := NewLivenessUpdater(zap.NewNop(), nil, time.Minute)
	old := time.UnixMilli(1000)
	newer := time.UnixMilli(5000)

	u.RecordPong("a1", old)
	u.RecordPong("a1", newer)                // newer wins
	u.RecordPong("a1", time.UnixMilli(2000)) // older ignored

	u.mu.Lock()
	got := u.pending["a1"]
	u.mu.Unlock()
	assert.Equal(t, newer, got, "RecordPong must keep the newest timestamp")
}

func TestLivenessUpdater_FlushWritesToRedis(t *testing.T) {
	comp := newTestComposite(t)
	ctx := context.Background()

	// Register an agent so UpdateLivenessRedis has a record to update.
	require.NoError(t, comp.Register(ctx, &registry.AgentInfo{
		AgentID: "a1", NodeID: "test-node-1", LastPongAt: 1000,
	}))

	u := NewLivenessUpdater(zap.NewNop(), comp, time.Minute)
	pongAt := time.UnixMilli(9000)
	u.RecordPong("a1", pongAt)

	u.flush(context.Background())

	// Flush writes to the Redis registry; read it back from there directly.
	got, err := comp.GetRedisRegistry().Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(9000), got.LastPongAt, "flush must update last_pong_at in Redis")
}

func TestLivenessUpdater_FlushEmptyNoop(t *testing.T) {
	comp := newTestComposite(t)
	u := NewLivenessUpdater(zap.NewNop(), comp, time.Minute)

	// No RecordPong calls → flush is a no-op (must not error or block).
	u.flush(context.Background())
	assert.Empty(t, u.pending)
}

func TestLivenessUpdater_RunFlushesOnCancel(t *testing.T) {
	comp := newTestComposite(t)
	ctx := context.Background()
	require.NoError(t, comp.Register(ctx, &registry.AgentInfo{
		AgentID: "a1", NodeID: "test-node-1", LastPongAt: 1000,
	}))

	u := NewLivenessUpdater(zap.NewNop(), comp, time.Hour) // long ticker; only cancel-driven flush fires
	u.RecordPong("a1", time.UnixMilli(7000))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		u.Run(runCtx)
		close(done)
	}()
	cancel() // triggers the final flush + return

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	got, err := comp.GetRedisRegistry().Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(7000), got.LastPongAt, "Run must flush pending pongs on ctx cancel")
}

// ── NewDistributedManager ──────────────────────────────────────────────

func TestNewDistributedManager(t *testing.T) {
	cfg := newTestDistributedConfig()
	client := newMiniRedis(t)
	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)
	t.Cleanup(func() { dm.Shutdown(context.Background()) })

	assert.Equal(t, "test-node-1", dm.NodeID())
	assert.Equal(t, "test-host:9000", dm.NodeAddr())
	require.NotNil(t, dm.Registry())
	require.NotNil(t, dm.PendingStore())
	require.NotNil(t, dm.Proxy())
}

// ── Start / Shutdown heartbeat ─────────────────────────────────────────

func TestDistributedManager_StartShutdownHeartbeat(t *testing.T) {
	cfg := newTestDistributedConfig()
	client := newMiniRedis(t)
	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)

	dm.Start()

	// runNodeHeartbeat registers immediately on Start; the node key should appear.
	nodeKey := cfg.Distributed.GetKeyPrefix() + "nodes:" + dm.NodeID()
	require.Eventually(t, func() bool {
		n, _ := client.Exists(context.Background(), nodeKey).Result()
		return n == 1
	}, 2*time.Second, 10*time.Millisecond, "node heartbeat key must be set on Start")

	// Shutdown unregisters the node and returns promptly.
	shutdownDone := make(chan struct{})
	go func() {
		dm.Shutdown(context.Background())
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return in time")
	}

	n, _ := client.Exists(context.Background(), nodeKey).Result()
	assert.Equal(t, int64(0), n, "node heartbeat key must be removed on Shutdown")
}

// ── cleanupStaleAgents ─────────────────────────────────────────────────

func TestDistributedManager_CleanupStaleAgents(t *testing.T) {
	cfg := newTestDistributedConfig()
	client := newMiniRedis(t)
	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)
	t.Cleanup(func() { dm.Shutdown(context.Background()) })

	ctx := context.Background()
	redisReg := dm.Registry().GetRedisRegistry()
	require.NotNil(t, redisReg)

	// Agent on a "dead" node (no heartbeat key) → must be cleaned up.
	dead := &registry.AgentInfo{AgentID: "dead-agent", NodeID: "dead-node", LastPongAt: time.Now().UnixMilli()}
	require.NoError(t, redisReg.Register(ctx, dead))

	// Agent on this node, whose heartbeat key we set manually → must be kept.
	require.NoError(t, client.Set(ctx, cfg.Distributed.GetKeyPrefix()+"nodes:"+dm.NodeID(), dm.NodeAddr(), time.Minute).Err())
	alive := &registry.AgentInfo{AgentID: "alive-agent", NodeID: dm.NodeID(), LastPongAt: time.Now().UnixMilli()}
	require.NoError(t, redisReg.Register(ctx, alive))

	dm.cleanupStaleAgents()

	got, err := redisReg.Get(ctx, "dead-agent")
	require.NoError(t, err)
	assert.Nil(t, got, "agent on a dead node must be removed")

	got, err = redisReg.Get(ctx, "alive-agent")
	require.NoError(t, err)
	require.NotNil(t, got, "agent on a live node must be kept")
	assert.Equal(t, "alive-agent", got.AgentID)
}

// ── IsAgentOnThisNode / GetAgentNodeAddr ───────────────────────────────

func TestDistributedManager_IsAgentOnThisNodeAndGetNodeAddr(t *testing.T) {
	cfg := newTestDistributedConfig()
	client := newMiniRedis(t)
	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)
	t.Cleanup(func() { dm.Shutdown(context.Background()) })

	ctx := context.Background()
	// Register an agent on this node via the composite registry (local + redis).
	require.NoError(t, dm.Registry().Register(ctx, &registry.AgentInfo{
		AgentID: "a1", NodeID: dm.NodeID(), NodeAddr: dm.NodeAddr(), LastPongAt: time.Now().UnixMilli(),
	}))

	assert.True(t, dm.IsAgentOnThisNode("a1"))
	assert.False(t, dm.IsAgentOnThisNode("nope"))

	addr, err := dm.GetAgentNodeAddr(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, dm.NodeAddr(), addr)

	// Unknown agent → empty addr, no error.
	addr, err = dm.GetAgentNodeAddr(ctx, "nope")
	require.NoError(t, err)
	assert.Empty(t, addr)
}

// ── RecordPong delegates to the liveness updater ──────────────────────

func TestDistributedManager_RecordPong(t *testing.T) {
	cfg := newTestDistributedConfig()
	client := newMiniRedis(t)
	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)
	t.Cleanup(func() { dm.Shutdown(context.Background()) })

	// Register the agent so a flush has something to update.
	ctx := context.Background()
	require.NoError(t, dm.Registry().Register(ctx, &registry.AgentInfo{
		AgentID: "a1", NodeID: dm.NodeID(), LastPongAt: 1000,
	}))

	pongAt := time.UnixMilli(12345)
	dm.RecordPong("a1", pongAt)
	// Flush the batched update to Redis.
	dm.livenessUpdater.flush(context.Background())

	got, err := dm.Registry().GetRedisRegistry().Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(12345), got.LastPongAt, "RecordPong must propagate to Redis via the liveness updater")
}
