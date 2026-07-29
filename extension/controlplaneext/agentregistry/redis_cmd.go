// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCmd abstracts the subset of redis.UniversalClient used by
// RedisAgentRegistry and RedisLoader. Extracting this narrow interface
// (P1 refactoring) allows unit-testing the Redis implementation with a
// lightweight mock rather than a real Redis instance.
//
// The interface is intentionally small — only the methods actually called by
// the agent registry code appear here. New methods should be added when
// needed, not speculatively.
type RedisCmd interface {
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

	// Pipeline and Transaction — return a narrow interface instead of the
	// full redis.Pipeliner (which has 100+ methods and cannot be mocked).
	// Both PipelineCmd and TxPipelineCmd are identical narrow interfaces
	// covering only the operations actually called through a pipeline.
	Pipeline() PipelineCmd
	TxPipeline() PipelineCmd

	// Pub/Sub
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd

	// Health
	Ping(ctx context.Context) *redis.StatusCmd
}

// PipelineCmd is the subset of redis.Pipeliner actually used by the agent
// registry through pipeline/transaction calls. It is narrow enough to mock.
type PipelineCmd interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd
	ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd
	ZRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	SRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd
	Exec(ctx context.Context) ([]redis.Cmder, error)
	// Get is used by RedisLoader batch reads
	Get(ctx context.Context, key string) *redis.StringCmd
}

// redisPipelineAdapter wraps redis.Pipeliner into PipelineCmd so *redis.Client
// (which returns redis.Pipeliner from Pipeline/TxPipeline) satisfies RedisCmd.
type redisPipelineAdapter struct{ redis.Pipeliner }

func (a redisPipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

// redisCmdAdapter wraps *redis.Client to implement RedisCmd with Pipeline
// methods returning PipelineCmd.
type redisCmdAdapter struct{ *redis.Client }

func (a redisCmdAdapter) Pipeline() PipelineCmd   { return redisPipelineAdapter{a.Client.Pipeline()} }
func (a redisCmdAdapter) TxPipeline() PipelineCmd { return redisPipelineAdapter{a.Client.TxPipeline()} }

// Ensure redisCmdAdapter satisfies RedisCmd.
var _ RedisCmd = redisCmdAdapter{}

// NewRedisCmd wraps a *redis.Client as a RedisCmd.
func NewRedisCmd(c *redis.Client) RedisCmd { return redisCmdAdapter{c} }
