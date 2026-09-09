// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisTenantRepository implements TenantRepository using Redis Hashes.
// Pure data access — no business logic.
//
// Storage layout:
//
//	{prefix}        → Hash: tenantID → Tenant JSON
//	{prefix}:names  → Hash: name → tenantID (secondary index for uniqueness)
type RedisTenantRepository struct {
	client     redis.UniversalClient
	tenantsKey string
	namesKey   string
}

// NewRedisTenantRepository creates a new Redis-backed TenantRepository.
func NewRedisTenantRepository(client redis.UniversalClient, keyPrefix string) *RedisTenantRepository {
	if keyPrefix == "" {
		keyPrefix = "otel:tenants"
	}
	return &RedisTenantRepository{
		client:     client,
		tenantsKey: keyPrefix,
		namesKey:   fmt.Sprintf("%s:names", keyPrefix),
	}
}

var _ TenantRepository = (*RedisTenantRepository)(nil)

// Insert stores a new tenant atomically (ID + name index).
func (r *RedisTenantRepository) Insert(ctx context.Context, tenant *Tenant) error {
	data, err := json.Marshal(tenant)
	if err != nil {
		return fmt.Errorf("marshal tenant: %w", err)
	}

	pipe := r.client.TxPipeline()
	pipe.HSetNX(ctx, r.tenantsKey, tenant.ID, string(data))
	pipe.HSetNX(ctx, r.namesKey, tenant.Name, tenant.ID)

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}
	if !cmds[0].(*redis.BoolCmd).Val() {
		return ErrNotFound
	}
	if !cmds[1].(*redis.BoolCmd).Val() {
		// Name already taken — roll back the ID insert.
		_ = r.client.HDel(ctx, r.tenantsKey, tenant.ID).Err()
		return ErrNotFound
	}
	return nil
}

// FindByID returns the tenant for the given ID, or ErrNotFound.
func (r *RedisTenantRepository) FindByID(ctx context.Context, id string) (*Tenant, error) {
	data, err := r.client.HGet(ctx, r.tenantsKey, id).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find tenant by id: %w", err)
	}
	return unmarshalTenant(data)
}

// FindByName returns the tenant with the given name, or ErrNotFound.
func (r *RedisTenantRepository) FindByName(ctx context.Context, name string) (*Tenant, error) {
	id, err := r.client.HGet(ctx, r.namesKey, name).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find tenant name index: %w", err)
	}
	return r.FindByID(ctx, id)
}

// Save overwrites the stored tenant, updating the name index if the name changed.
func (r *RedisTenantRepository) Save(ctx context.Context, tenant *Tenant) error {
	existing, err := r.FindByID(ctx, tenant.ID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(tenant)
	if err != nil {
		return fmt.Errorf("marshal tenant: %w", err)
	}

	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, r.tenantsKey, tenant.ID, string(data))
	if existing.Name != tenant.Name {
		pipe.HDel(ctx, r.namesKey, existing.Name)
		pipe.HSet(ctx, r.namesKey, tenant.Name, tenant.ID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("save tenant: %w", err)
	}
	return nil
}

// Delete removes the tenant and its name index.
func (r *RedisTenantRepository) Delete(ctx context.Context, id string) error {
	tenant, err := r.FindByID(ctx, id)
	if err != nil {
		return err
	}

	pipe := r.client.TxPipeline()
	pipe.HDel(ctx, r.tenantsKey, id)
	pipe.HDel(ctx, r.namesKey, tenant.Name)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	return nil
}

// List returns all stored tenants.
func (r *RedisTenantRepository) List(ctx context.Context) ([]*Tenant, error) {
	result, err := r.client.HGetAll(ctx, r.tenantsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}

	tenants := make([]*Tenant, 0, len(result))
	for _, data := range result {
		tenant, err := unmarshalTenant(data)
		if err != nil {
			continue // skip invalid entries (backward compatible)
		}
		tenants = append(tenants, tenant)
	}
	return tenants, nil
}

func unmarshalTenant(data string) (*Tenant, error) {
	var tenant Tenant
	if err := json.Unmarshal([]byte(data), &tenant); err != nil {
		return nil, fmt.Errorf("unmarshal tenant: %w", err)
	}
	return &tenant, nil
}
