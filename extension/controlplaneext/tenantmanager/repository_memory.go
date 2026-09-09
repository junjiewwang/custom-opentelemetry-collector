// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"sync"
)

// MemoryTenantRepository implements TenantRepository using in-memory maps.
// Pure data access — no business logic. Suitable for testing and single-node
// deployments.
type MemoryTenantRepository struct {
	mu      sync.RWMutex
	tenants map[string]*Tenant // id → tenant
	byName  map[string]string  // name → id
}

// NewMemoryTenantRepository creates a new in-memory TenantRepository.
func NewMemoryTenantRepository() *MemoryTenantRepository {
	return &MemoryTenantRepository{
		tenants: make(map[string]*Tenant),
		byName:  make(map[string]string),
	}
}

var _ TenantRepository = (*MemoryTenantRepository)(nil)

// Insert stores a new tenant. Returns ErrNotFound if the ID already exists.
func (r *MemoryTenantRepository) Insert(_ context.Context, tenant *Tenant) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tenants[tenant.ID]; exists {
		return ErrNotFound
	}

	clone := *tenant
	r.tenants[tenant.ID] = &clone
	r.byName[tenant.Name] = tenant.ID
	return nil
}

// FindByID returns the tenant for the given ID, or ErrNotFound.
func (r *MemoryTenantRepository) FindByID(_ context.Context, id string) (*Tenant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tenant, ok := r.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *tenant
	return &clone, nil
}

// FindByName returns the tenant with the given name, or ErrNotFound.
func (r *MemoryTenantRepository) FindByName(_ context.Context, name string) (*Tenant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, ok := r.byName[name]
	if !ok {
		return nil, ErrNotFound
	}
	tenant, ok := r.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *tenant
	return &clone, nil
}

// Save overwrites the stored tenant, updating the name index if the name changed.
func (r *MemoryTenantRepository) Save(_ context.Context, tenant *Tenant) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.tenants[tenant.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.Name != tenant.Name {
		delete(r.byName, existing.Name)
	}

	clone := *tenant
	r.tenants[tenant.ID] = &clone
	r.byName[tenant.Name] = tenant.ID
	return nil
}

// Delete removes the tenant. Returns ErrNotFound if absent.
func (r *MemoryTenantRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tenant, ok := r.tenants[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.byName, tenant.Name)
	delete(r.tenants, id)
	return nil
}

// List returns all stored tenants.
func (r *MemoryTenantRepository) List(_ context.Context) ([]*Tenant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tenants := make([]*Tenant, 0, len(r.tenants))
	for _, tenant := range r.tenants {
		clone := *tenant
		tenants = append(tenants, &clone)
	}
	return tenants, nil
}
