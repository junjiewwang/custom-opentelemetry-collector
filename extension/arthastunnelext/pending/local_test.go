// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pending

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestStore() *LocalPendingStore {
	return NewLocalPendingStore(zap.NewNop(), "node-1", "node-1:4318")
}

func sampleInfo(id string) *PendingInfo {
	return &PendingInfo{ClientConnID: id, AgentID: "agent-1", SessionID: "s1"}
}

func TestLocalPendingStore_CreateAndGet(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	got, err := s.Get(ctx, "c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "c1", got.ClientConnID)
	assert.Equal(t, "node-1", got.NodeID, "Create stamps node info")
	assert.Equal(t, "node-1:4318", got.NodeAddr)

	// Get returns a copy.
	got.AgentID = "mutated"
	again, _ := s.Get(ctx, "c1")
	assert.Equal(t, "agent-1", again.AgentID)
}

func TestLocalPendingStore_Create_Duplicate(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))
	err := s.Create(ctx, sampleInfo("c1"))
	assert.ErrorIs(t, err, ErrPendingAlreadyExists)
}

func TestLocalPendingStore_GetNotFound(t *testing.T) {
	s := newTestStore()
	got, err := s.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLocalPendingStore_Delete(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))
	assert.True(t, s.IsLocal("c1"))

	require.NoError(t, s.Delete(ctx, "c1"))
	assert.False(t, s.IsLocal("c1"))
	got, _ := s.Get(ctx, "c1")
	assert.Nil(t, got)

	// Deleting a missing pending is a no-op.
	require.NoError(t, s.Delete(ctx, "never"))
}

func TestLocalPendingStore_IsLocal(t *testing.T) {
	s := newTestStore()
	require.NoError(t, s.Create(context.Background(), sampleInfo("c1")))
	assert.True(t, s.IsLocal("c1"))
	assert.False(t, s.IsLocal("missing"))
}

func TestLocalPendingStore_DeliverAndWaitForTunnel(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	// Deliver a tunnel from one goroutine; WaitForTunnel receives it.
	done := make(chan struct{})
	waitErr := make(chan error, 1)
	waitConn := make(chan interface{}, 1)
	go func() {
		c, err := s.WaitForTunnel(ctx, "c1", 2*time.Second)
		waitErr <- err
		waitConn <- c
		close(done)
	}()

	// Give the waiter a moment to start, then deliver.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, s.DeliverTunnel("c1", nil)) // nil conn is fine for the channel path

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForTunnel did not return after delivery")
	}
	require.NoError(t, <-waitErr)
	// nil conn was delivered; the channel returns nil.
	c := <-waitConn
	assert.Nil(t, c)
}

func TestLocalPendingStore_DeliverTunnel_NotFound(t *testing.T) {
	s := newTestStore()
	err := s.DeliverTunnel("missing", nil)
	assert.ErrorIs(t, err, ErrPendingNotFound)
}

func TestLocalPendingStore_DeliverTunnel_AlreadyDelivered(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	// First delivery succeeds (buffered channel, capacity 1).
	require.NoError(t, s.DeliverTunnel("c1", nil))
	// Second delivery without a consumer → ErrTunnelAlreadyDelivered.
	err := s.DeliverTunnel("c1", nil)
	assert.ErrorIs(t, err, ErrTunnelAlreadyDelivered)
}

func TestLocalPendingStore_WaitForTunnel_Timeout(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	c, err := s.WaitForTunnel(ctx, "c1", 50*time.Millisecond)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, c)
}

func TestLocalPendingStore_WaitForTunnel_NotFound(t *testing.T) {
	s := newTestStore()
	_, err := s.WaitForTunnel(context.Background(), "missing", 50*time.Millisecond)
	assert.ErrorIs(t, err, ErrPendingNotFound)
}

func TestLocalPendingStore_WaitForTunnel_ContextCancel(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := s.WaitForTunnel(cctx, "c1", 5*time.Second)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("WaitForTunnel did not return on ctx cancel")
	}
}

func TestLocalPendingStore_CreateWithBrowserConn(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	// nil browser conn is acceptable for the store (it just stores the pointer).
	require.NoError(t, s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil))
	assert.Nil(t, s.GetBrowserConn("c1"))

	// Duplicate still errors.
	err := s.CreateWithBrowserConn(ctx, sampleInfo("c1"), nil)
	assert.ErrorIs(t, err, ErrPendingAlreadyExists)

	// Missing → nil.
	assert.Nil(t, s.GetBrowserConn("missing"))
}

func TestLocalPendingStore_CountAndListIDs(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))
	require.NoError(t, s.Create(ctx, sampleInfo("c2")))

	assert.Equal(t, 2, s.Count())
	assert.ElementsMatch(t, []string{"c1", "c2"}, s.ListIDs())
}

func TestLocalPendingStore_Close(t *testing.T) {
	s := newTestStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleInfo("c1")))
	require.NoError(t, s.Close())

	// After Close, the store is empty.
	assert.Equal(t, 0, s.Count())
	// WaitForTunnel after close returns not-found (pending deleted).
	_, err := s.WaitForTunnel(ctx, "c1", 50*time.Millisecond)
	assert.ErrorIs(t, err, ErrPendingNotFound)
}
