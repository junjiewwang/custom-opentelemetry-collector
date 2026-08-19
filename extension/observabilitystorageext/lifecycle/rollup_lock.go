// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
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

// claimKey builds the Redis key for a per-slice claim lock. The `slice`
// parameter is an opaque string identifying the work unit — for hour-granular
// rollup it is "{date}:{hour}", so different replicas can claim different
// hours of the same app/day in parallel.
func (l *RollupLock) claimKey(tier, appID, slice string) string {
	return fmt.Sprintf("otel:rollup:claim:%s:%s:%s", tier, appID, slice)
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

// setWatermarkIfGreaterScript atomically advances a watermark hash field to the
// proposed value ONLY when it is strictly greater than the current value (or the
// field is absent). This makes the watermark monotonic at the Redis layer, so a
// stale in-memory snapshot (or a slower replica) cannot overwrite a newer value
// already written by an operator or a faster replica. Returns the value that
// ends up stored (the proposed value on a successful advance, or the existing
// value when the write is blocked).
var setWatermarkIfGreaterScript = redis.NewScript(`
local current = redis.call("HGET", KEYS[1], ARGV[1])
if current == false then
    redis.call("HSET", KEYS[1], ARGV[1], ARGV[2])
    return ARGV[2]
end
if tonumber(ARGV[2]) > tonumber(current) then
    redis.call("HSET", KEYS[1], ARGV[1], ARGV[2])
    return ARGV[2]
end
return current
`)

// TryClaimSlice attempts to acquire the claim lock for one (tier, appID, slice)
// work unit. Returns true if this node owns the lock (newly acquired or
// re-acquired after a restart where it was already the holder).
func (l *RollupLock) TryClaimSlice(ctx context.Context, tier, appID, slice string, ttl time.Duration) (bool, error) {
	key := l.claimKey(tier, appID, slice)
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
func (l *RollupLock) ReleaseClaimSlice(ctx context.Context, tier, appID, slice string) error {
	key := l.claimKey(tier, appID, slice)
	_, err := releaseClaimScript.Run(ctx, l.client, []string{key}, l.nodeID).Int()
	if err != nil {
		return fmt.Errorf("rollup release claim: %w", err)
	}
	return nil
}

// Lease is a held distributed claim lock with a background heartbeat goroutine
// that periodically refreshes the TTL, so a live holder never loses the lock to
// a slow-but-alive aggregation. The caller MUST call Release when the work
// completes (defer lease.Release() is the recommended pattern) — it is
// idempotent and stops the heartbeat goroutine before CAS-releasing the key.
//
// Lease is deliberately tied to a single (tier, appID, slice) claim; the key and
// owner are opaque to the caller, so the same mechanism serves 5m rollup today
// and a future 1h rollup (or any other tier) unchanged.
type Lease struct {
	key    string
	owner  string
	client redis.UniversalClient
	logger *zap.Logger

	cancel context.CancelFunc // stops the heartbeat goroutine
	done   chan struct{}      // closed when the heartbeat goroutine exits
	stolen atomic.Int32       // 1 = heartbeat detected the lock was lost
	once   sync.Once         // makes Release idempotent
}

// AcquireWithLease attempts to claim a slice and, on success, starts a background
// heartbeat goroutine that refreshes the TTL every heartbeatInterval. Returns
// (nil, nil) when another node already holds the slice; a non-nil error when the
// claim could not be performed (e.g. Redis unreachable).
//
// The heartbeat goroutine runs on its own context (independent of ctx) so a
// cancelled request context cannot silently kill the heartbeat and let the lock
// expire mid-aggregation. Its lifetime is controlled entirely by Lease.Release.
func (l *RollupLock) AcquireWithLease(ctx context.Context, tier, appID, slice string, ttl, heartbeatInterval time.Duration) (*Lease, error) {
	key := l.claimKey(tier, appID, slice)

	claimed, err := l.TryClaimSlice(ctx, tier, appID, slice, ttl)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, nil
	}

	hbCtx, cancel := context.WithCancel(context.Background())
	lease := &Lease{
		key:    key,
		owner:  l.nodeID,
		client: l.client,
		logger: l.logger,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go lease.heartbeatLoop(hbCtx, ttl, heartbeatInterval)
	return lease, nil
}

// heartbeatLoop periodically refreshes the claim TTL until the lease is released
// (hbCtx cancelled) or the lock is lost. If PExpire reports the key no longer
// exists (another node stole it after our TTL lapsed), it marks the lease stolen
// and stops — there is no point continuing to refresh a key we no longer own.
func (lease *Lease) heartbeatLoop(hbCtx context.Context, ttl, interval time.Duration) {
	defer close(lease.done)
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-hbCtx.Done():
			return
		case <-ticker.C:
			if !lease.refresh(ttl) {
				lease.stolen.Store(1)
				lease.logger.Warn("rollup claim lease lost (stolen or expired)",
					zap.String("key", lease.key))
				return
			}
		}
	}
}

// refresh extends the claim TTL. Returns false when the key no longer exists
// (lost the lock), true otherwise.
func (lease *Lease) refresh(ttl time.Duration) bool {
	ok, err := lease.client.Expire(context.Background(), lease.key, ttl).Result()
	if err != nil {
		// Transient Redis error — do not mark stolen; the next tick retries.
		lease.logger.Warn("rollup claim lease refresh failed", zap.String("key", lease.key), zap.Error(err))
		return true
	}
	return ok
}

// Release stops the heartbeat goroutine and releases the lock (CAS: only if this
// node still holds it). It is idempotent and safe to call multiple times. It uses
// a detached context with a short timeout so a cancelled parent context (e.g.
// graceful shutdown) does not make the release a no-op and leak the lock.
func (lease *Lease) Release() {
	lease.once.Do(func() {
		lease.cancel()
		<-lease.done
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = releaseClaimScript.Run(rctx, lease.client, []string{lease.key}, lease.owner).Int()
	})
}

// IsStolen reports whether the heartbeat detected the lock was lost (stolen or
// expired) mid-aggregation. Callers that care about lock integrity may check it
// before a critical section; rollup treats it as advisory because writes are
// idempotent, so a stolen lease yields a duplicate aggregation at worst.
func (lease *Lease) IsStolen() bool {
	return lease.stolen.Load() == 1
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

// SetWatermark advances the watermark for (tier, appID) to lastBucketMs. It is
// monotonic: the write only succeeds when lastBucketMs is strictly greater than
// the current value (or the field is absent). A stale in-memory snapshot or a
// slower replica therefore cannot overwrite a newer value — the newer value
// wins. The watermark always moves forward.
func (l *RollupLock) SetWatermark(ctx context.Context, tier, appID string, lastBucketMs int64) error {
	res, err := setWatermarkIfGreaterScript.Run(ctx, l.client,
		[]string{l.watermarkKey(tier)}, appID, strconv.FormatInt(lastBucketMs, 10)).Text()
	if err != nil {
		return fmt.Errorf("rollup set watermark: %w", err)
	}
	stored, err := strconv.ParseInt(res, 10, 64)
	if err != nil {
		return fmt.Errorf("parse set-watermark result %q: %w", res, err)
	}
	if stored > lastBucketMs {
		l.logger.Debug("rollup watermark advance blocked by newer value",
			zap.String("appID", appID),
			zap.Int64("proposed_ms", lastBucketMs),
			zap.Int64("existing_ms", stored),
		)
	}
	return nil
}

// GetAllWatermarks returns the full {appID -> lastBucketMs} map for a tier,
// used by the leader during planning to skip already-processed slices.
//
// It defensively rejects values that are NOT whole-hour boundaries (i.e.
// ms % 3600000 != 0). The hour-granular engine stores watermarks at whole-hour
// edges (e.g. 1785891600000); a value ending in 999999 is a stale/legacy
// "end-of-day minus 1ms" seed that would otherwise deadlock the contiguity
// check in advanceWatermark (strict equality against a whole-hour boundary
// fails by 1ms, so the watermark never advances). Treating such values as
// absent (0) lets the engine restart cleanly and self-heal.
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
		if ms%int64(time.Hour/time.Millisecond) != 0 {
			l.logger.Warn("skip non-hour-aligned watermark (stale/legacy seed)",
				zap.String("appID", appID), zap.Int64("value_ms", ms))
			continue
		}
		out[appID] = ms
	}
	return out, nil
}
