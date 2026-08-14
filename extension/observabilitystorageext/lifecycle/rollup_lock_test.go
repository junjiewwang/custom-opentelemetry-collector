// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestRollupLock(t *testing.T) (*RollupLock, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRollupLock(client, "node-1", zap.NewNop()), client
}

func TestSetWatermark_FirstWrite(t *testing.T) {
	lock, _ := newTestRollupLock(t)
	ctx := context.Background()

	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1786665600000))
	got, err := lock.GetWatermark(ctx, "5m", "app")
	require.NoError(t, err)
	assert.Equal(t, int64(1786665600000), got)
}

func TestSetWatermark_ForwardAdvance(t *testing.T) {
	lock, _ := newTestRollupLock(t)
	ctx := context.Background()

	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1000))
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 2000))
	got, _ := lock.GetWatermark(ctx, "5m", "app")
	assert.Equal(t, int64(2000), got, "forward advance must overwrite")
}

func TestSetWatermark_BackwardBlocked(t *testing.T) {
	lock, _ := newTestRollupLock(t)
	ctx := context.Background()

	// Simulate operator/another replica setting a NEWER watermark.
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 3000))
	// A stale write of an OLDER value must be blocked (monotonic).
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 2000))
	got, _ := lock.GetWatermark(ctx, "5m", "app")
	assert.Equal(t, int64(3000), got, "backward write must be blocked; newer value wins")
}

func TestSetWatermark_EqualBlocked(t *testing.T) {
	lock, _ := newTestRollupLock(t)
	ctx := context.Background()

	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1000))
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1000))
	got, _ := lock.GetWatermark(ctx, "5m", "app")
	assert.Equal(t, int64(1000), got, "equal write must not change value")
}

func TestSetWatermark_OperatorOverrideSurvivesStaleTick(t *testing.T) {
	lock, _ := newTestRollupLock(t)
	ctx := context.Background()

	// Operator sets a far-ahead watermark (skip already-rolled history).
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1786665600000)) // 08-14
	// A stale in-flight tick tries to write an older hour (08-10).
	require.NoError(t, lock.SetWatermark(ctx, "5m", "app", 1786356000000))
	got, _ := lock.GetWatermark(ctx, "5m", "app")
	assert.Equal(t, int64(1786665600000), got, "operator's ahead-of-schedule watermark must survive")
}
