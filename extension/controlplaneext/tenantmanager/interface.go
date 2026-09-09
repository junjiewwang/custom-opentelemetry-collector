// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
)

// TenantManager is the composite interface for tenant lifecycle management.
// Implemented by TenantService, which centralizes all business rules.
type TenantManager interface {
	// CreateTenant creates a tenant (ID generated).
	CreateTenant(ctx context.Context, req *CreateTenantRequest) (*Tenant, error)

	// GetTenant returns a tenant by ID.
	GetTenant(ctx context.Context, id string) (*Tenant, error)

	// ListTenants returns all tenants.
	ListTenants(ctx context.Context) ([]*Tenant, error)

	// UpdateTenant updates name/description/status.
	UpdateTenant(ctx context.Context, id string, req *UpdateTenantRequest) (*Tenant, error)

	// DeleteTenant removes an empty tenant.
	DeleteTenant(ctx context.Context, id string) error

	// ListTenantApps returns the app IDs owned by a tenant.
	ListTenantApps(ctx context.Context, tenantID string) ([]string, error)

	// EnsureDefaultTenant idempotently creates the built-in "admin" tenant.
	EnsureDefaultTenant(ctx context.Context) error

	// Start initializes the manager (creates the default tenant).
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// AppLister is the narrow, consumer-side interface tenantmanager needs to
// resolve a tenant's apps (ISP). It is satisfied by appmanager.AppService's
// existing ListApps method, so no new app-side code is required and
// tenantmanager stays decoupled from app CRUD.
type AppLister interface {
	ListApps(ctx context.Context) ([]*appmanager.AppInfo, error)
}

// Config holds configuration for TenantManager.
type Config struct {
	// Type specifies the backend type: "memory" or "redis".
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from the storage extension.
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for Redis keys.
	KeyPrefix string `mapstructure:"key_prefix"`
}

// DefaultConfig returns the default configuration. Tenant management is
// Redis-backed by default: it must be shared across collector replicas and
// survive restarts, so a memory backend is not a safe production default.
func DefaultConfig() Config {
	return Config{
		Type:      "redis",
		RedisName: "default",
		KeyPrefix: "otel:tenants",
	}
}
