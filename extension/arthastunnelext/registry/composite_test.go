// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newComposite builds a CompositeRegistry backed by a real LocalRegistry +
// a RedisRegistry over miniredis. Returns the composite + raw client.
func newComposite(t *testing.T, checker LivenessChecker) (*CompositeRegistry, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	local := NewLocalRegistry(zap.NewNop(), "node-1", "node-1:4318", checker)
	redisReg := NewRedisRegistry(zap.NewNop(), client, "tunnel:", 5*time.Minute, checker)
	return NewCompositeRegistry(zap.NewNop(), local, redisReg, "node-1"), client
}

func TestComposite_RegisterWritesBoth(t *testing.T) {
	c, client := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))

	// Local has it.
	assert.True(t, c.IsLocal("a1"))
	// Redis has it too (cross-node visible).
	exists, err := client.Exists(ctx, "tunnel:agents:a1").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

func TestComposite_GetLocalFirst(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))

	got, err := c.Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "a1", got.AgentID)
}

func TestComposite_GetFallsBackToRedis(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()

	// Register an agent directly in Redis only (simulating another node), not local.
	require.NoError(t, c.redis.Register(ctx, &AgentInfo{AgentID: "remote-1", NodeID: "node-2", NodeAddr: "node-2:4318", LastPongAt: 1}))
	assert.False(t, c.IsLocal("remote-1"))

	// Get finds it via Redis fallback.
	got, err := c.Get(ctx, "remote-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "remote-1", got.AgentID)
	assert.Equal(t, "node-2:4318", got.NodeAddr)
}

func TestComposite_GetNotFound(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	got, err := c.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestComposite_UnregisterRemovesBoth(t *testing.T) {
	c, client := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))

	require.NoError(t, c.Unregister(ctx, "a1"))
	assert.False(t, c.IsLocal("a1"))
	exists, _ := client.Exists(ctx, "tunnel:agents:a1").Result()
	assert.Equal(t, int64(0), exists)
}

func TestComposite_UpdateLiveness_LocalOnly(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", LastPongAt: 1}))

	// UpdateLiveness updates local immediately (Redis is batched separately).
	require.NoError(t, c.UpdateLiveness(ctx, "a1", time.UnixMilli(2000)))
	got := c.local.GetLocalAgent("a1")
	require.NotNil(t, got)
	assert.Equal(t, int64(2000), got.LastPongAt)
}

func TestComposite_UpdateLivenessRedis(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", LastPongAt: 1}))

	require.NoError(t, c.UpdateLivenessRedis(ctx, "a1", time.UnixMilli(3000)))
	got, _ := c.redis.Get(ctx, "a1")
	require.NotNil(t, got)
	assert.Equal(t, int64(3000), got.LastPongAt)
}

func TestComposite_List_FromRedis(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	now := time.Now().UnixMilli()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: now}))

	list, err := c.List(ctx)
	require.NoError(t, err)
	assert.Contains(t, agentIDs(list), "a1")
}

func TestComposite_ListByAppID(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a2", AppID: "app-2", LastPongAt: 1}))

	got, err := c.ListByAppID(ctx, "app-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a1"}, agentIDs(got))
}

func TestComposite_GetNodeAddr(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", NodeAddr: "node-1:4318", LastPongAt: 1}))

	addr, err := c.GetNodeAddr(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "node-1:4318", addr)

	// Remote agent via Redis fallback.
	require.NoError(t, c.redis.Register(ctx, &AgentInfo{AgentID: "remote", NodeAddr: "node-2:4318", LastPongAt: 1}))
	addr, err = c.GetNodeAddr(ctx, "remote")
	require.NoError(t, err)
	assert.Equal(t, "node-2:4318", addr)
}

func TestComposite_IsAgentOnThisNode(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	ctx := context.Background()
	// Local agent (node-1).
	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", NodeID: "node-1", LastPongAt: 1}))
	assert.True(t, c.IsAgentOnThisNode(ctx, "a1"))

	// Remote agent (node-2).
	require.NoError(t, c.redis.Register(ctx, &AgentInfo{AgentID: "remote", NodeID: "node-2", LastPongAt: 1}))
	assert.False(t, c.IsAgentOnThisNode(ctx, "remote"))

	// Missing.
	assert.False(t, c.IsAgentOnThisNode(ctx, "missing"))
}

func TestComposite_NilRedis_DegradedToLocal(t *testing.T) {
	// Composite with nil Redis → all operations degrade to local only.
	checker := alwaysAlive()
	local := NewLocalRegistry(zap.NewNop(), "node-1", "node-1:4318", checker)
	c := NewCompositeRegistry(zap.NewNop(), local, nil, "node-1")
	ctx := context.Background()

	require.NoError(t, c.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))
	got, err := c.Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)

	list, err := c.List(ctx)
	require.NoError(t, err)
	assert.Contains(t, agentIDs(list), "a1")

	// UpdateLivenessRedis on nil redis is a no-op (no error).
	require.NoError(t, c.UpdateLivenessRedis(ctx, "a1", time.UnixMilli(2000)))

	// GetNodeAddr with empty local + nil redis → empty.
	addr, err := c.GetNodeAddr(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, addr)

	assert.NoError(t, c.Close())
}

func TestComposite_Getters(t *testing.T) {
	c, _ := newComposite(t, alwaysAlive())
	assert.NotNil(t, c.GetLocalRegistry())
	assert.NotNil(t, c.GetRedisRegistry())
}
