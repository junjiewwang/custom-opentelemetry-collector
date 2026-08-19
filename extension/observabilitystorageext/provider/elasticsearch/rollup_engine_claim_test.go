// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

// newLock builds a RollupLock backed by miniredis.
func newLock(t *testing.T, mr *miniredis.Miniredis) *lifecycle.RollupLock {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return lifecycle.NewRollupLock(client, "node-a", zap.NewNop())
}

// TestAcquireWithLease_ClaimAndRelease verifies the lease is acquired, then
// released (idempotently) and the key is gone.
func TestAcquireWithLease_ClaimAndRelease(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	lock := newLock(t, mr)

	lease, err := lock.AcquireWithLease(context.Background(), "5m", "app", "2026.08.13:00", 5*time.Minute, 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, lease)

	// The key exists and is owned by node-a.
	require.True(t, mr.Exists("otel:rollup:claim:5m:app:2026.08.13:00"))

	// Release removes the key and is idempotent.
	lease.Release()
	lease.Release() // must not panic
	assert.False(t, mr.Exists("otel:rollup:claim:5m:app:2026.08.13:00"))
}

// TestAcquireWithLease_AlreadyHeld verifies a second node cannot claim a slice
// already held by another node.
func TestAcquireWithLease_AlreadyHeld(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	lockA := newLock(t, mr)

	_, err = lockA.AcquireWithLease(context.Background(), "5m", "app", "2026.08.13:00", 5*time.Minute, 100*time.Millisecond)
	require.NoError(t, err)

	// node-b tries to claim the same slice.
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	lockB := lifecycle.NewRollupLock(client, "node-b", zap.NewNop())
	leaseB, err := lockB.AcquireWithLease(context.Background(), "5m", "app", "2026.08.13:00", 5*time.Minute, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, leaseB, "second node must not acquire an already-held slice")
}

// TestLease_HeartbeatRefreshes verifies the heartbeat extends the TTL so a live
// holder keeps the lock beyond the initial TTL.
func TestLease_HeartbeatRefreshes(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	lock := newLock(t, mr)

	// Short TTL and interval so the test runs fast, but with interval < TTL so
	// the heartbeat fires before the TTL lapses.
	ttl := 150 * time.Millisecond
	interval := 50 * time.Millisecond
	lease, err := lock.AcquireWithLease(context.Background(), "5m", "app", "2026.08.13:00", ttl, interval)
	require.NoError(t, err)
	require.NotNil(t, lease)
	defer lease.Release()

	key := "otel:rollup:claim:5m:app:2026.08.13:00"

	// Wait for several heartbeat intervals (well past the 150ms TTL). The
	// heartbeat (every 50ms) must keep refreshing the TTL so the key survives.
	time.Sleep(400 * time.Millisecond)
	assert.True(t, mr.Exists(key), "heartbeat must keep the key alive past the TTL")
}

// TestLease_ReleaseStopsHeartbeat verifies Release stops the heartbeat goroutine
// and the key is removed, so no goroutine leaks and no stale lock remains.
func TestLease_ReleaseStopsHeartbeat(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	lock := newLock(t, mr)

	lease, err := lock.AcquireWithLease(context.Background(), "5m", "app", "2026.08.13:00", 5*time.Minute, 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, lease)

	lease.Release()
	assert.False(t, mr.Exists("otel:rollup:claim:5m:app:2026.08.13:00"), "release must remove the key")
}

// TestClaimTTLAndHeartbeat verifies the fixed TTL and derived heartbeat interval.
func TestClaimTTLAndHeartbeat(t *testing.T) {
	assert.Equal(t, 5*time.Minute, claimTTL)
	assert.Equal(t, claimTTL/3, claimHeartbeatInterval)
}
