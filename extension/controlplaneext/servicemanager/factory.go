// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package servicemanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager/store"
)

// RedisClient is a type alias for redis.UniversalClient.
// This allows external packages to reference the Redis client type without importing go-redis directly.
type RedisClient = redis.UniversalClient

// NewServiceManager creates a ServiceManager based on the configuration.
// This is the main factory function that should be used to create service managers.
func NewServiceManager(logger *zap.Logger, config Config, redisClient RedisClient) (ServiceManager, error) {
	var serviceStore store.ServiceStore

	switch config.Type {
	case "memory", "":
		serviceStore = store.NewMemoryServiceStore(logger.Named("store"))

	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis service manager")
		}
		serviceStore = store.NewRedisServiceStore(logger.Named("store"), redisClient, config.KeyPrefix)

	default:
		return nil, errors.New("unsupported service manager type: " + config.Type)
	}

	return NewServiceService(logger.Named("service"), config, serviceStore), nil
}
