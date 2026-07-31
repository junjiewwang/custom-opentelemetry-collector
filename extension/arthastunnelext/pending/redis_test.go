// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pending

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

// newTestRedisPendingStore builds a RedisPendingStore over miniredis, wrapping
// a real LocalPendingStore for tunnel delivery. Returns the store + raw client.
func newTestRedisPendingStore(t *testing.T) (*RedisPendingStore, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	local := NewLocalPendingStore(zap.NewNop(), "node-1", "node-1:4318")
	s := NewRedisPendingStore(zap.NewNop(), client, "tunnel:", 5*time.Minute, "node-1", "node-1:4318", local)
	return s, client
}

func TestRedisPendingStore_CreateAndGet(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	// Get reads from Redis (Create does NOT store locally).
	got, err := s.Get(ctx, "c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "c1", got.ClientConnID)
	assert.Equal(t, "node-1", got.NodeID, "Create stamps node info")
	assert.Equal(t, "node-1:4318", got.NodeAddr)
}

func TestRedisPendingStore_Create_Duplicate(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))
	err := s.Create(ctx, sampleInfo("c1"))
	assert.ErrorIs(t, err, ErrPendingAlreadyExists)
}

func TestRedisPendingStore_GetNotFound(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	got, err := s.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRedisPendingStore_GetLocalFirst(t *testing.T) {
	// CreateWithBrowserConn stores locally; Get should hit local first.
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))

	got, err := s.Get(ctx, "c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "c1", got.ClientConnID)
	assert.True(t, s.IsLocal("c1"))
}

func TestRedisPendingStore_CreateWithBrowserConn_RollbackOnRedisDup(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	// Pre-create in Redis so the Create inside CreateWithBrowserConn fails →
	// local must be rolled back.
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	err := s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil)
	assert.ErrorIs(t, err, ErrPendingAlreadyExists)
	// Local entry rolled back.
	assert.False(t, s.IsLocal("c1"))
}

func TestRedisPendingStore_Delete(t *testing.T) {
	s, client := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))

	require.NoError(t, s.Delete(ctx, "c1"))
	assert.False(t, s.IsLocal("c1"))
	exists, _ := client.Exists(ctx, "tunnel:pending:c1").Result()
	assert.Equal(t, int64(0), exists)
}

func TestRedisPendingStore_IsLocal(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	assert.False(t, s.IsLocal("c1"))
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))
	assert.True(t, s.IsLocal("c1"))
}

func TestRedisPendingStore_DeliverAndWaitForTunnel(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))

	done := make(chan struct{})
	waitErr := make(chan error, 1)
	waitConn := make(chan interface{}, 1)
	go func() {
		c, err := s.WaitForTunnel(ctx, "c1", 2*time.Second)
		waitErr <- err
		waitConn <- c
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, s.DeliverTunnel("c1", nil))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForTunnel did not return after delivery")
	}
	require.NoError(t, <-waitErr)
	assert.Nil(t, <-waitConn)
}

func TestRedisPendingStore_ClaimPending(t *testing.T) {
	s, client := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	// Claim returns the info and deletes the key atomically.
	info, err := s.ClaimPending(ctx, "c1")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "c1", info.ClientConnID)

	// Second claim returns nil (key gone).
	info, err = s.ClaimPending(ctx, "c1")
	require.NoError(t, err)
	assert.Nil(t, info)

	exists, _ := client.Exists(ctx, "tunnel:pending:c1").Result()
	assert.Equal(t, int64(0), exists)
}

func TestRedisPendingStore_ClaimPending_NotFound(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	info, err := s.ClaimPending(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, info)
}

func TestRedisPendingStore_GetNodeAddr(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	addr, err := s.GetNodeAddr(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "node-1:4318", addr)

	addr, err = s.GetNodeAddr(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, addr)
}

func TestRedisPendingStore_GetBrowserConn(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))
	assert.Nil(t, s.GetBrowserConn("c1")) // nil conn stored
	assert.Nil(t, s.GetBrowserConn("missing"))
}

func TestRedisPendingStore_Close(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))
	require.NoError(t, s.Close())
	// After close, local is cleared → WaitForTunnel not found.
	_, err := s.WaitForTunnel(ctx, "c1", 50*time.Millisecond)
	assert.ErrorIs(t, err, ErrPendingNotFound)
}

func TestRedisPendingStore_GetLocalStore(t *testing.T) {
	s, _ := newTestRedisPendingStore(t)
	assert.NotNil(t, s.GetLocalStore())
}
