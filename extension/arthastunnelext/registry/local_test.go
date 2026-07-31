// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeLivenessChecker is a controllable LivenessChecker for LocalRegistry tests.
type fakeLivenessChecker struct {
	timeoutMillis int64
	timeoutDur    time.Duration
}

func (f *fakeLivenessChecker) IsTimeout(lastPongAtMilli int64) bool {
	return lastPongAtMilli < f.timeoutMillis // older than threshold → timed out
}
func (f *fakeLivenessChecker) LivenessTimeout() time.Duration { return f.timeoutDur }

func newTestLocalRegistry(checker LivenessChecker) *LocalRegistry {
	return NewLocalRegistry(zap.NewNop(), "node-1", "node-1:4318", checker)
}

func TestLocalRegistry_RegisterAndGet(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{timeoutMillis: 0})
	ctx := context.Background()

	info := &AgentInfo{AgentID: "a1", AppID: "app-1", AppName: "svc", IP: "1.2.3.4"}
	require.NoError(t, r.Register(ctx, info))

	// Register stamps node info.
	got, err := r.Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "a1", got.AgentID)
	assert.Equal(t, "node-1", got.NodeID, "Register must set NodeID")
	assert.Equal(t, "node-1:4318", got.NodeAddr)
	// Get returns a copy — mutating it must not affect the store.
	got.AppID = "mutated"
	again, _ := r.Get(ctx, "a1")
	assert.Equal(t, "app-1", again.AppID)
}

func TestLocalRegistry_GetNotFound(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	got, err := r.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLocalRegistry_Unregister(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1"}))
	assert.True(t, r.IsLocal("a1"))

	require.NoError(t, r.Unregister(ctx, "a1"))
	assert.False(t, r.IsLocal("a1"))
	got, _ := r.Get(ctx, "a1")
	assert.Nil(t, got)

	// Unregistering a missing agent is a no-op (no error).
	require.NoError(t, r.Unregister(ctx, "never"))
}

func TestLocalRegistry_UpdateLiveness(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1"}))

	now := time.Now()
	require.NoError(t, r.UpdateLiveness(ctx, "a1", now))
	got, _ := r.Get(ctx, "a1")
	assert.Equal(t, now.UnixMilli(), got.LastPongAt)

	// UpdateLiveness on a missing agent is a no-op (no error).
	require.NoError(t, r.UpdateLiveness(ctx, "missing", now))
}

func TestLocalRegistry_List_FiltersTimedOut(t *testing.T) {
	// checker: agents with LastPongAt < 1000 are timed out.
	r := newTestLocalRegistry(&fakeLivenessChecker{timeoutMillis: 1000})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "alive", LastPongAt: 2000}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "dead", LastPongAt: 500}))

	list, err := r.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "alive", list[0].AgentID)
}

func TestLocalRegistry_ListByAppID(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{timeoutMillis: 0})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1"}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a2", AppID: "app-1"}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "b1", AppID: "app-2"}))

	got, err := r.ListByAppID(ctx, "app-1")
	require.NoError(t, err)
	require.Len(t, got, 2)

	got, err = r.ListByAppID(ctx, "app-9")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLocalRegistry_ListByAppID_FiltersTimedOut(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{timeoutMillis: 1000})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1", AppID: "app-1", LastPongAt: 2000}))
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a2", AppID: "app-1", LastPongAt: 500}))

	got, err := r.ListByAppID(ctx, "app-1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a1", got[0].AgentID)
}

func TestLocalRegistry_IsLocal(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	require.NoError(t, r.Register(context.Background(), &AgentInfo{AgentID: "a1"}))
	assert.True(t, r.IsLocal("a1"))
	assert.False(t, r.IsLocal("missing"))
}

func TestLocalRegistry_GetNodeAddr(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	ctx := context.Background()
	require.NoError(t, r.Register(ctx, &AgentInfo{AgentID: "a1"}))

	addr, err := r.GetNodeAddr(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "node-1:4318", addr)

	addr, err = r.GetNodeAddr(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, addr)
}

func TestLocalRegistry_RangeLocal(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	require.NoError(t, r.Register(context.Background(), &AgentInfo{AgentID: "a1"}))
	require.NoError(t, r.Register(context.Background(), &AgentInfo{AgentID: "a2"}))

	seen := map[string]bool{}
	r.RangeLocal(func(id string, _ *AgentInfo) bool {
		seen[id] = true
		return true
	})
	assert.True(t, seen["a1"])
	assert.True(t, seen["a2"])

	// Early stop: returning false halts iteration.
	count := 0
	r.RangeLocal(func(_ string, _ *AgentInfo) bool {
		count++
		return false
	})
	assert.Equal(t, 1, count)
}

func TestLocalRegistry_GetLocalAgent(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	require.NoError(t, r.Register(context.Background(), &AgentInfo{AgentID: "a1", AppID: "app-1"}))

	got := r.GetLocalAgent("a1")
	require.NotNil(t, got)
	assert.Equal(t, "app-1", got.AppID)
	assert.Nil(t, r.GetLocalAgent("missing"))
}

func TestLocalRegistry_Close(t *testing.T) {
	r := newTestLocalRegistry(&fakeLivenessChecker{})
	assert.NoError(t, r.Close())
}
