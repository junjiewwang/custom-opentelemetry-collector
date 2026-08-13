// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RollupLock provides distributed coordination for metric rollup:
//   - per-slice claim locks (SETNX) so only one replica aggregates a given
//     (appID, date) slice
//   - a shared watermark (HSET) tracking the last processed bucket per app so a
//     restarted replica resumes from real progress instead of re-running or
//     skipping work
//
// It mirrors the locking pattern in runtime_snapshot_store_redis.go
// (TryAcquireRefreshLease) and the leader CAS script in leader_elector.go.
type RollupLock struct {
	client redis.UniversalClient
	nodeID string
	logger *zap.Logger
}

// NewRollupLock creates a RollupLock backed by Redis.
func NewRollupLock(client redis.UniversalClient, nodeID string, logger *zap.Logger) *RollupLock {
	return &RollupLock{client: client, nodeID: nodeID, logger: logger}
}

// claimKey builds the Redis key for a per-slice claim lock.
func (l *RollupLock) claimKey(tier, appID, date string) string {
	return fmt.Sprintf("otel:rollup:claim:%s:%s:%s", tier, appID, date)
}

// watermarkKey builds the Redis key holding the per-app watermark hash for a tier.
func (l *RollupLock) watermarkKey(tier string) string {
	return fmt.Sprintf("otel:rollup:watermark:%s", tier)
}

// releaseScript atomically releases a claim lock only if this node still holds
// it (CAS), preventing one node from releasing another's lock.
var releaseClaimScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// TryClaimSlice attempts to acquire the claim lock for one (tier, appID, date)
// slice. Returns true if this node owns the lock (newly acquired or re-acquired
// after a restart where it was already the holder).
func (l *RollupLock) TryClaimSlice(ctx context.Context, tier, appID, date string, ttl time.Duration) (bool, error) {
	key := l.claimKey(tier, appID, date)
	ok, err := l.client.SetNX(ctx, key, l.nodeID, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("rollup claim SET NX: %w", err)
	}
	if ok {
		return true, nil
	}
	// Not acquired — check whether we already hold it (restart-safe).
	holder, err := l.client.Get(ctx, key).Result()
	if err == nil && holder == l.nodeID {
		_ = l.client.PExpire(ctx, key, ttl)
		return true, nil
	}
	return false, nil
}

// ReleaseClaimSlice releases a claim lock (only if this node holds it).
func (l *RollupLock) ReleaseClaimSlice(ctx context.Context, tier, appID, date string) error {
	key := l.claimKey(tier, appID, date)
	_, err := releaseClaimScript.Run(ctx, l.client, []string{key}, l.nodeID).Int()
	if err != nil {
		return fmt.Errorf("rollup release claim: %w", err)
	}
	return nil
}

// GetWatermark returns the last processed bucket timestamp (unix ms) for
// (tier, appID). Returns 0 if no watermark exists yet.
func (l *RollupLock) GetWatermark(ctx context.Context, tier, appID string) (int64, error) {
	val, err := l.client.HGet(ctx, l.watermarkKey(tier), appID).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("rollup get watermark: %w", err)
	}
	ms, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse watermark %q: %w", val, err)
	}
	return ms, nil
}

// SetWatermark advances the watermark for (tier, appID) to lastBucketMs.
// Uses HSET (not SETNX) because the watermark always moves forward.
func (l *RollupLock) SetWatermark(ctx context.Context, tier, appID string, lastBucketMs int64) error {
	return l.client.HSet(ctx, l.watermarkKey(tier), appID, strconv.FormatInt(lastBucketMs, 10)).Err()
}

// GetAllWatermarks returns the full {appID -> lastBucketMs} map for a tier,
// used by the leader during planning to skip already-processed slices.
func (l *RollupLock) GetAllWatermarks(ctx context.Context, tier string) (map[string]int64, error) {
	raw, err := l.client.HGetAll(ctx, l.watermarkKey(tier)).Result()
	if err != nil {
		return nil, fmt.Errorf("rollup get all watermarks: %w", err)
	}
	out := make(map[string]int64, len(raw))
	for appID, v := range raw {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			l.logger.Warn("skip invalid watermark", zap.String("appID", appID), zap.String("value", v), zap.Error(err))
			continue
		}
		out[appID] = ms
	}
	return out, nil
}
