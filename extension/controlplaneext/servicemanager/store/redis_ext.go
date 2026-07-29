// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// storeRedisCmd extends the shared redisclient.RedisCmd with the extra methods
// used by RedisServiceStore: HExists, Scan, and the redis.Scripter methods
// (Eval/EvalSha/...) required by redis.Script.Run. The pipeline interface is
// the base redisclient.PipelineCmd (HDel + Exec), so no pipeline extension is
// needed.
type storeRedisCmd interface {
	redisclient.RedisCmd
	HExists(ctx context.Context, key, field string) *redis.BoolCmd
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd

	// Scripter — required by redis.Script.Run.
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd
	EvalRO(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
	EvalShaRO(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd
	ScriptExists(ctx context.Context, hashes ...string) *redis.BoolSliceCmd
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
}

// storeRedisCmdAdapter wraps *redis.Client to satisfy storeRedisCmd. The
// Scripter, HExists, Scan and base key/hash methods are promoted from
// *redis.Client; only Pipeline/TxPipeline are overridden to return the base
// redisclient.PipelineCmd (via a thin pipeliner wrapper).
type storeRedisCmdAdapter struct{ *redis.Client }

func (a storeRedisCmdAdapter) Pipeline() redisclient.PipelineCmd {
	return storePipelineAdapter{a.Client.Pipeline()}
}
func (a storeRedisCmdAdapter) TxPipeline() redisclient.PipelineCmd {
	return storePipelineAdapter{a.Client.TxPipeline()}
}

// storePipelineAdapter wraps redis.Pipeliner into redisclient.PipelineCmd.
// HSet/HDel are promoted from redis.Pipeliner; Exec is re-declared to match
// the redisclient.PipelineCmd signature.
type storePipelineAdapter struct{ redis.Pipeliner }

func (a storePipelineAdapter) Exec(ctx context.Context) ([]redis.Cmder, error) {
	return a.Pipeliner.Exec(ctx)
}

// NewRedisCmd wraps a *redis.Client as a storeRedisCmd for the
// servicemanager/store package. Used by the factory layer.
func NewRedisCmd(c *redis.Client) storeRedisCmd { return storeRedisCmdAdapter{c} }
