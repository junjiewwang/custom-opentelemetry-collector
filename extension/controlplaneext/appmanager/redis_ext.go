// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// appPipelineCmd extends the shared redisclient.PipelineCmd with HSetNX,
// used atomically via TxPipeline in Insert/Save.
type appPipelineCmd interface {
	redisclient.PipelineCmd
	HSetNX(ctx context.Context, key, field string, value interface{}) *redis.BoolCmd
}

// appRedisCmd is the Redis interface used by RedisAppRepository. It is a
// standalone interface (it does not embed redisclient.RedisCmd) because
// appmanager needs TxPipeline to return appPipelineCmd (with HSetNX), whereas
// the base interface returns redisclient.PipelineCmd. The base key/hash
// methods are listed here; the pipeline base is reused via appPipelineCmd
// embedding redisclient.PipelineCmd.
type appRedisCmd interface {
	HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd
	HSetNX(ctx context.Context, key, field string, value interface{}) *redis.BoolCmd
	TxPipeline() appPipelineCmd
}

// appRedisCmdAdapter wraps *redis.Client to satisfy appRedisCmd. All hash
// methods (HSet/HGet/HGetAll/HDel/HSetNX) are promoted from *redis.Client.
// Only TxPipeline is overridden to return the local appPipelineCmd.
type appRedisCmdAdapter struct{ *redis.Client }

func (a appRedisCmdAdapter) TxPipeline() appPipelineCmd {
	return appPipelineAdapter{a.Client.TxPipeline()}
}

// appPipelineAdapter wraps redis.Pipeliner into appPipelineCmd. HSetNX, HDel,
// HSet and Exec are promoted from redis.Pipeliner; Exec is re-declared to
// match the redisclient.PipelineCmd signature.
type appPipelineAdapter struct{ redis.Pipeliner }

func (a appPipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

// NewRedisCmd wraps a *redis.Client as an appRedisCmd for the appmanager
// package. Used by the factory layer.
func NewRedisCmd(c *redis.Client) appRedisCmd { return appRedisCmdAdapter{c} }
