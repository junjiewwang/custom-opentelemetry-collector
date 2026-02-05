// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"github.com/redis/go-redis/v9"
)

type defaultRedisProvider struct{}

func (defaultRedisProvider) Create(cfg RedisConfig) (redis.UniversalClient, error) {
	cfg.ApplyDefaults()

	opts := &redis.UniversalOptions{
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// Determine connection mode
	switch {
	case cfg.MasterName != "" && len(cfg.SentinelAddrs) > 0:
		// Sentinel mode
		opts.MasterName = cfg.MasterName
		opts.Addrs = cfg.SentinelAddrs
	case len(cfg.Addrs) > 0:
		// Cluster mode
		opts.Addrs = cfg.Addrs
	default:
		// Standalone mode
		opts.Addrs = []string{cfg.Addr}
	}

	return redis.NewUniversalClient(opts), nil
}
