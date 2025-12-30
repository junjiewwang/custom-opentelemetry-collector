// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// createRedisClient creates a Redis client based on configuration.
func (e *Extension) createRedisClient(cfg RedisConfig) (redis.UniversalClient, error) {
	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 10
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}

	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 3 * time.Second
	}

	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 3 * time.Second
	}

	opts := &redis.UniversalOptions{
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     poolSize,
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	// Determine connection mode
	if cfg.MasterName != "" && len(cfg.SentinelAddrs) > 0 {
		// Sentinel mode
		opts.MasterName = cfg.MasterName
		opts.Addrs = cfg.SentinelAddrs
	} else if len(cfg.Addrs) > 0 {
		// Cluster mode
		opts.Addrs = cfg.Addrs
	} else {
		// Standalone mode
		opts.Addrs = []string{cfg.Addr}
	}

	return redis.NewUniversalClient(opts), nil
}
