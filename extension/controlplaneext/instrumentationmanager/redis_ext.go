// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// instrPipelineCmd extends the shared redisclient.PipelineCmd with Del and
// Set, used by RedisRuleStore.PhysicalDeleteRule and SaveTargetStatuses via
// Pipeline.
type instrPipelineCmd interface {
	redisclient.PipelineCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
}

// instrRedisCmd is the Redis interface used by RedisRuleStore and
// redisRuntimeSnapshotStore. It is a standalone interface (it does not embed
// redisclient.RedisCmd) because this package needs Pipeline to return
// instrPipelineCmd (with Del/Set), whereas the base interface returns
// redisclient.PipelineCmd. The base key/hash methods are listed here; the
// pipeline base is reused via instrPipelineCmd embedding
// redisclient.PipelineCmd.
//
// Watch/Subscribe carry callbacks/return types tied to concrete go-redis
// types (*redis.Tx, *redis.PubSub) that cannot be constructed by a mock. They
// are included with their original signatures so the business logic is
// unchanged, but code paths that use them (SaveRule, SaveTargetStatuses,
// runtime snapshot Upsert/MarkDirty, the pub/sub Start path) are not
// unit-testable with a mock and remain covered by the integration tests.
type instrRedisCmd interface {
	// Key/value
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	PExpire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd

	// Hash
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd
	HExists(ctx context.Context, key, field string) *redis.BoolCmd

	// Scan
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd

	// Pipeline
	Pipeline() instrPipelineCmd

	// Transaction & Pub/Sub — kept with their original go-redis signatures so
	// business logic is unchanged. These are NOT mockable (a mock cannot
	// construct *redis.Tx or *redis.PubSub); code using them is covered by
	// integration tests instead.
	Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error
	Subscribe(ctx context.Context, channels ...string) *redis.PubSub
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd

	// Health
	Ping(ctx context.Context) *redis.StatusCmd
}

// instrRedisCmdAdapter wraps *redis.Client to satisfy instrRedisCmd. All
// methods (Set/SetNX/Get/Del/PExpire/H*/Scan/Watch/Subscribe/Publish/Ping)
// are promoted from *redis.Client; only Pipeline is overridden to return the
// local instrPipelineCmd.
type instrRedisCmdAdapter struct{ *redis.Client }

func (a instrRedisCmdAdapter) Pipeline() instrPipelineCmd {
	return instrPipelineAdapter{a.Client.Pipeline()}
}

// instrPipelineAdapter wraps redis.Pipeliner into instrPipelineCmd. HDel, HSet,
// Del, Set and Exec are promoted from redis.Pipeliner; Exec is re-declared to
// match the redisclient.PipelineCmd signature.
type instrPipelineAdapter struct{ redis.Pipeliner }

func (a instrPipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

// NewRedisCmd wraps a *redis.Client as an instrRedisCmd for the
// instrumentationmanager package. Used by the factory layer.
func NewRedisCmd(c *redis.Client) instrRedisCmd { return instrRedisCmdAdapter{c} }
