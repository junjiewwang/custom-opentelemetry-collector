// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// NewTenantManager creates a TenantManager based on configuration, backed by
// the appropriate TenantRepository implementation. appLister is the existing
// app manager (satisfies AppLister via its ListApps method) used to resolve a
// tenant's apps.
func NewTenantManager(logger *zap.Logger, config Config, redisClient redis.UniversalClient, appLister AppLister) (TenantManager, error) {
	var repo TenantRepository

	switch config.Type {
	case "memory":
		repo = NewMemoryTenantRepository()

	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis tenant manager")
		}
		repo = NewRedisTenantRepository(redisClient, config.KeyPrefix)

	default:
		return nil, errors.New("unknown tenant manager type: " + config.Type)
	}

	return NewTenantService(repo, NewIDGenerator(), appLister, logger), nil
}
