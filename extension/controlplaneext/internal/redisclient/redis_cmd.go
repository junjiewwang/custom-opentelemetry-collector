// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package redisclient provides a narrow Redis client interface shared across
// all controlplaneext sub-packages. It follows the Interface Segregation
// Principle: the base RedisCmd interface contains only methods used by ALL
// packages; packages needing extra methods define local extension interfaces
// that embed RedisCmd.
//
// This eliminates the per-package redis_cmd.go duplication (4 copies of
// adapter + mock) into a single shared definition + single mock.
package redisclient

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCmd is the base Redis client interface used by all controlplaneext
// sub-packages. It covers the common subset of Redis operations: key/value
// (Set/Get/Del), hash (HSet/HGet/HGetAll/HDel), pipeline, and health (Ping).
//
// Packages that need additional operations (e.g. ZRange, Scan, Watch) define
// a local extension interface that embeds RedisCmd:
//
//	type AgentRegistryRedisCmd interface {
//	    redisclient.RedisCmd
//	    ZRange(ctx, key, start, stop) *redis.StringSliceCmd
//	    // ...
//	}
type RedisCmd interface {
	// Key/value
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd

	// Hash
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd

	// Pipeline / Transaction
	Pipeline() PipelineCmd
	TxPipeline() PipelineCmd

	// Health
	Ping(ctx context.Context) *redis.StatusCmd
}

// PipelineCmd is the base pipeline interface. Sub-packages with additional
// pipeline operations (e.g. HSetNX, ZAdd) define local extensions.
type PipelineCmd interface {
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd
	Exec(ctx context.Context) ([]redis.Cmder, error)
}

// ── Adapter: wraps *redis.Client to satisfy RedisCmd ──────────────────

type redisCmdAdapter struct{ *redis.Client }

// pipelineAdapter wraps redis.Pipeliner into PipelineCmd.
type pipelineAdapter struct{ redis.Pipeliner }

func (a pipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

func (a redisCmdAdapter) Pipeline() PipelineCmd {
	return pipelineAdapter{a.Client.Pipeline()}
}

func (a redisCmdAdapter) TxPipeline() PipelineCmd {
	return pipelineAdapter{a.Client.TxPipeline()}
}

// NewRedisCmd wraps a *redis.Client as a RedisCmd. Use this at the factory
// layer (component_factory.go) when passing a raw *redis.Client to a
// sub-package constructor.
func NewRedisCmd(c *redis.Client) RedisCmd { return redisCmdAdapter{c} }
