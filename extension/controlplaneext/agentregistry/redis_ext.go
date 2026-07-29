// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// agentPipelineCmd extends the shared redisclient.PipelineCmd with the extra
// pipeline operations used by RedisAgentRegistry and RedisLoader: Set, Del,
// ZAdd, ZRem, SAdd, SRem, Exists, Publish, Get.
type agentPipelineCmd interface {
	redisclient.PipelineCmd
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd
	ZRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	SRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd
	Get(ctx context.Context, key string) *redis.StringCmd
}

// agentRedisCmd is the Redis interface used by RedisAgentRegistry and
// RedisLoader. It is a standalone interface (it does not embed
// redisclient.RedisCmd) because this package needs Pipeline/TxPipeline to
// return agentPipelineCmd (with the extra pipeline ops), whereas the base
// interface returns redisclient.PipelineCmd. The base key/hash methods and
// the non-pipeline extras (Exists/TTL/SCard/SMembers/ZRange/ZRem/Publish) are
// listed here; the pipeline base is reused via agentPipelineCmd embedding
// redisclient.PipelineCmd.
type agentRedisCmd interface {
	// Key/value
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	TTL(ctx context.Context, key string) *redis.DurationCmd

	// Hash
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd

	// Set
	SCard(ctx context.Context, key string) *redis.IntCmd
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd

	// Sorted Set
	ZRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd
	ZRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd

	// Pipeline / Transaction
	Pipeline() agentPipelineCmd
	TxPipeline() agentPipelineCmd

	// Pub/Sub
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd

	// Health
	Ping(ctx context.Context) *redis.StatusCmd
}

// agentRedisCmdAdapter wraps *redis.Client to satisfy agentRedisCmd. All
// methods (Set/Get/Del/Exists/TTL/H*/SCard/SMembers/ZRange/ZRem/Publish/Ping)
// are promoted from *redis.Client; only Pipeline/TxPipeline are overridden to
// return the local agentPipelineCmd.
type agentRedisCmdAdapter struct{ *redis.Client }

func (a agentRedisCmdAdapter) Pipeline() agentPipelineCmd {
	return agentPipelineAdapter{a.Client.Pipeline()}
}
func (a agentRedisCmdAdapter) TxPipeline() agentPipelineCmd {
	return agentPipelineAdapter{a.Client.TxPipeline()}
}

// agentPipelineAdapter wraps redis.Pipeliner into agentPipelineCmd. All
// pipeline ops (Set/Del/HSet/HDel/ZAdd/ZRem/SAdd/SRem/Exists/Publish/Get) are
// promoted from redis.Pipeliner; Exec is re-declared to match the
// redisclient.PipelineCmd signature.
type agentPipelineAdapter struct{ redis.Pipeliner }

func (a agentPipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

// NewRedisCmd wraps a *redis.Client as an agentRedisCmd for the agentregistry
// package. Used by the factory layer.
func NewRedisCmd(c *redis.Client) agentRedisCmd { return agentRedisCmdAdapter{c} }
