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

// RedisLeaderElector implements LeaderElector using Redis SET NX for distributed lock.
// This is extracted from the old RedisCoordinator to separate orchestration concerns
// from task engine concerns.
type RedisLeaderElector struct {
	client    redis.UniversalClient
	nodeID    string
	leaderTTL time.Duration
	logger    *zap.Logger
}

// NewRedisLeaderElector creates a new Redis-based leader elector.
func NewRedisLeaderElector(client redis.UniversalClient, nodeID string, logger *zap.Logger) *RedisLeaderElector {
	return &RedisLeaderElector{
		client:    client,
		nodeID:    nodeID,
		leaderTTL: 30 * time.Second,
		logger:    logger,
	}
}

// leaderKey returns the Redis key for the leader lock.
func (e *RedisLeaderElector) leaderKey() string {
	return "lifecycle:leader"
}

// activeEpochKey returns the Redis key for the active epoch marker.
func (e *RedisLeaderElector) activeEpochKey() string {
	return "lifecycle:active_epoch"
}

// TryBecomeLeader attempts to acquire the leader lock using SET NX EX.
func (e *RedisLeaderElector) TryBecomeLeader(ctx context.Context) (bool, error) {
	ok, err := e.client.SetNX(ctx, e.leaderKey(), e.nodeID, e.leaderTTL).Result()
	if err != nil {
		return false, fmt.Errorf("leader election SET NX: %w", err)
	}
	if ok {
		e.logger.Info("Acquired leader lock", zap.String("nodeID", e.nodeID))
	}
	return ok, nil
}

// releaseLeaderScript ensures only the lock holder can release it (CAS).
var releaseLeaderScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// ReleaseLeader releases the leader lock (only if we hold it).
func (e *RedisLeaderElector) ReleaseLeader(ctx context.Context) error {
	result, err := releaseLeaderScript.Run(ctx, e.client, []string{e.leaderKey()}, e.nodeID).Int()
	if err != nil {
		return fmt.Errorf("release leader: %w", err)
	}
	if result == 0 {
		e.logger.Warn("Leader lock was not held by this node (already expired or stolen)")
	}
	return nil
}

// GetActiveEpoch returns the current active epoch, or 0 if none.
func (e *RedisLeaderElector) GetActiveEpoch(ctx context.Context) (int64, error) {
	val, err := e.client.Get(ctx, e.activeEpochKey()).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get active epoch: %w", err)
	}
	epoch, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse active epoch %q: %w", val, err)
	}
	return epoch, nil
}

// SetActiveEpoch sets the active epoch marker with a 2-hour TTL.
func (e *RedisLeaderElector) SetActiveEpoch(ctx context.Context, epoch int64) error {
	return e.client.Set(ctx, e.activeEpochKey(), strconv.FormatInt(epoch, 10), 2*time.Hour).Err()
}

// ClearActiveEpoch removes the active epoch marker.
func (e *RedisLeaderElector) ClearActiveEpoch(ctx context.Context) error {
	return e.client.Del(ctx, e.activeEpochKey()).Err()
}

// LocalLeaderElector implements LeaderElector for single-node deployments.
// It always wins election (there's only one node).
type LocalLeaderElector struct {
	epoch int64
}

// NewLocalLeaderElector creates a single-node leader elector.
func NewLocalLeaderElector() *LocalLeaderElector {
	return &LocalLeaderElector{}
}

func (e *LocalLeaderElector) TryBecomeLeader(_ context.Context) (bool, error) {
	return true, nil
}

func (e *LocalLeaderElector) ReleaseLeader(_ context.Context) error {
	return nil
}

func (e *LocalLeaderElector) GetActiveEpoch(_ context.Context) (int64, error) {
	return e.epoch, nil
}

func (e *LocalLeaderElector) SetActiveEpoch(_ context.Context, epoch int64) error {
	e.epoch = epoch
	return nil
}

func (e *LocalLeaderElector) ClearActiveEpoch(_ context.Context) error {
	e.epoch = 0
	return nil
}
