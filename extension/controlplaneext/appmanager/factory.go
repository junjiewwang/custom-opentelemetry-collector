// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// NewTokenManager creates a TokenManager based on configuration.
// Returns an AppService backed by the appropriate AppRepository implementation.
// AppService implements TokenManager, AppRetentionProvider, and all consumer interfaces.
func NewTokenManager(logger *zap.Logger, config Config, redisClient redis.UniversalClient) (TokenManager, error) {
	var repo AppRepository

	switch config.Type {
	case "memory":
		repo = NewMemoryAppRepository()

	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis token manager")
		}
		repo = NewRedisAppRepository(redisClient, config.KeyPrefix)

	default:
		return nil, errors.New("unknown token manager type: " + config.Type)
	}

	svc := NewAppService(
		repo,
		NewIDGenerator(),
		NewTokenGenerator(),
		DefaultRetentionLimits(),
		config.SeedApps,
		logger,
	)

	return svc, nil
}
