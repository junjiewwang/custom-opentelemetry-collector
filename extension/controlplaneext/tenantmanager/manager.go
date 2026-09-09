// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// MultiTenantManager bundles tenant lifecycle (TenantManager) and API key
// management (APIKeyService). Both share a single TenantRepository so tenant
// status is consistent across the two — a tenant disabled via TenantService is
// immediately rejected by APIKeyService.ValidateAPIKey.
type MultiTenantManager struct {
	Tenants TenantManager
	Keys    *APIKeyService
}

// NewMultiTenantManager creates a full multi-tenancy manager backed by the
// configured repository (memory or redis). appLister resolves tenant→apps.
//
// A single idGen is shared so IDs are drawn from one sequence; the key
// repository uses a distinct prefix from the tenant repository.
func NewMultiTenantManager(logger *zap.Logger, config Config, redisClient redis.UniversalClient, appLister AppLister) (*MultiTenantManager, error) {
	var tenantRepo TenantRepository
	var keyRepo APIKeyRepository

	switch config.Type {
	case "memory":
		tenantRepo = NewMemoryTenantRepository()
		keyRepo = NewMemoryAPIKeyRepository()
	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis tenant manager")
		}
		tenantRepo = NewRedisTenantRepository(redisClient, config.KeyPrefix)
		keyRepo = NewRedisAPIKeyRepository(redisClient, "") // default prefix "otel:tenant_keys"
	default:
		return nil, errors.New("unknown tenant manager type: " + config.Type)
	}

	idGen := NewIDGenerator()
	return &MultiTenantManager{
		Tenants: NewTenantService(tenantRepo, idGen, appLister, logger),
		Keys:    NewAPIKeyService(keyRepo, tenantRepo, idGen, logger),
	}, nil
}
