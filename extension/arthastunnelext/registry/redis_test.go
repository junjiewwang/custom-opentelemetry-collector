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

// newTestRedisRegistry builds a RedisRegistry over a fresh miniredis instance.
// Returns the registry + the raw client (for direct state inspection).
func newTestRedisRegistry(t *testing.T, checker LivenessChecker) (*RedisRegistry, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	r := NewRedisRegistry(zap.NewNop(), client, "tunnel:", 5*time.Minute, checker)
	return r, client
}

// alwaysAlive checker: nothing is ever timed out. timeoutDur is set large so
// RedisRegistry.List's ZRangeByScore (minScore = now - LivenessTimeout) keeps
// all agents regardless of their LastPongAt.
func alwaysAlive() LivenessChecker {
	return &fakeLivenessChecker{timeoutMillis: -1 << 62, timeoutDur: 365 * 24 * time.Hour}
}

func TestRedisRegistry_RegisterAndGet(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()

	info := &AgentInfo{AgentID: "a1", AppID: "app-1", NodeID: "node-1", LastPongAt: 1000}
	require.NoError(t, r.Register(ctx, info))

	got, err := r.Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "a1", got.AgentID)
	assert.Equal(t, "app-1", got.AppID)
	assert.Equal(t, "node-1", got.NodeID)
}

func TestRedisRegistry_GetNotFound(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	got, err := r.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRedisRegistry_Unregister(t *testing.T) {
	r, client := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1000}))

	require.NoError(t, r.Unregister(ctx, "a1"))
	got, _ := r.Get(ctx, "a1")
	assert.Nil(t, got)

	// App index entry removed too.
	exists, err := client.SIsMember(ctx, r.appIndexKey("app-1"), "a1").Result()
	require.NoError(t, err)
	assert.False(t, exists)

	// Unregistering a missing agent is a no-op.
	require.NoError(t, r.Unregister(ctx, "never"))
}

func TestRedisRegistry_UpdateLiveness_Monotonic(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", LastPongAt: 1000}))

	// Newer pong updates.
	require.NoError(t, r.UpdateLiveness(ctx, "a1", time.UnixMilli(2000)))
	got, _ := r.Get(ctx, "a1")
	assert.Equal(t, int64(2000), got.LastPongAt)

	// Older pong is ignored (monotonic).
	require.NoError(t, r.UpdateLiveness(ctx, "a1", time.UnixMilli(500)))
	got, _ = r.Get(ctx, "a1")
	assert.Equal(t, int64(2000), got.LastPongAt, "older pong must not overwrite")

	// UpdateLiveness on a missing agent returns nil (script returns 0, no error).
	require.NoError(t, r.UpdateLiveness(ctx, "missing", time.UnixMilli(2000)))
}

func TestRedisRegistry_List_HealthyOnly(t *testing.T) {
	// Liveness timeout = 1500ms: agents with LastPongAt < (now-1500ms) excluded.
	checker := &fakeLivenessChecker{timeoutMillis: -1, timeoutDur: 1500 * time.Millisecond}
	r, _ := newTestRedisRegistry(t, checker)
	ctx := context.Background()

	now := time.Now().UnixMilli()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "alive", LastPongAt: now}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "dead", LastPongAt: now - 60_000}))

	list, err := r.List(ctx)
	require.NoError(t, err)
	ids := agentIDs(list)
	assert.Contains(t, ids, "alive")
	assert.NotContains(t, ids, "dead")
}

func TestRedisRegistry_List_Empty(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	list, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestRedisRegistry_ListByAppID(t *testing.T) {
	// Use alwaysAlive so the liveness filter in ListByAppID keeps everyone.
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 1}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a2", AppID: "app-1", LastPongAt: 1}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "b1", AppID: "app-2", LastPongAt: 1}))

	got, err := r.ListByAppID(ctx, "app-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a1", "a2"}, agentIDs(got))

	got, err = r.ListByAppID(ctx, "app-9")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRedisRegistry_IsLocal_AlwaysFalse(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	assert.False(t, r.IsLocal("anything"))
}

func TestRedisRegistry_GetNodeAddr(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", NodeAddr: "1.2.3.4:4318", LastPongAt: 1}))

	addr, err := r.GetNodeAddr(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4:4318", addr)

	addr, err = r.GetNodeAddr(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, addr)
}

func TestRedisRegistry_CleanupStaleAgents(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", NodeID: "node-alive", LastPongAt: 1}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a2", NodeID: "node-dead", LastPongAt: 1}))

	// Only node-alive is alive → a2 removed.
	require.NoError(t, r.CleanupStaleAgents(ctx, map[string]bool{"node-alive": true}))

	_, err := r.Get(ctx, "a1")
	require.NoError(t, err)
	got, _ := r.Get(ctx, "a2")
	assert.Nil(t, got, "agent on dead node must be cleaned up")
}

func TestRedisRegistry_CleanupExpiredAgents(t *testing.T) {
	checker := &fakeLivenessChecker{timeoutMillis: -1, timeoutDur: 1500 * time.Millisecond}
	r, client := newTestRedisRegistry(t, checker)
	ctx := context.Background()

	now := time.Now().UnixMilli()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "alive", LastPongAt: now}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "dead", LastPongAt: now - 60_000}))

	require.NoError(t, r.CleanupExpiredAgents(ctx))

	// "dead" removed from the online ZSET; "alive" remains.
	ids, err := client.ZRange(ctx, r.onlineZSetKey(), 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, ids, "alive")
	assert.NotContains(t, ids, "dead")
}

func TestRedisRegistry_Close(t *testing.T) {
	r, _ := newTestRedisRegistry(t, alwaysAlive())
	assert.NoError(t, r.Close(), "Close is a no-op (shared client)")
}

func agentIDs(agents []*AgentInfo) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.AgentID)
	}
	return out
}
