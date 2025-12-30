// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// NewTokenManager creates a TokenManager based on configuration.
func NewTokenManager(logger *zap.Logger, config Config, redisClient redis.UniversalClient) (TokenManager, error) {
	switch config.Type {
	case "memory":
		return NewMemoryTokenManager(logger, config), nil

	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis token manager")
		}
		return NewRedisTokenManager(logger, config, redisClient)

	default:
		return nil, errors.New("unknown token manager type: " + config.Type)
	}
}
