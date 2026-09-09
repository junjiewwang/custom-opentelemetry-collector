// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"errors"
)

// Sentinel errors returned by TenantService and TenantRepository.
var (
	// ErrNotFound is returned when a tenant does not exist.
	ErrNotFound = errors.New("tenant not found")

	// ErrTenantNameExists is returned when creating/updating a tenant with a
	// duplicate name.
	ErrTenantNameExists = errors.New("tenant name already exists")

	// ErrTenantNotEmpty is returned when deleting a tenant that still owns apps.
	ErrTenantNotEmpty = errors.New("tenant still owns apps and cannot be deleted")

	// ErrDefaultTenantImmutable is returned when attempting to delete the
	// built-in "admin" tenant.
	ErrDefaultTenantImmutable = errors.New("default tenant cannot be deleted")
)

// TenantRepository is a narrow, storage-agnostic persistence abstraction for
// Tenant. It contains NO business rules (no name-uniqueness, no delete-guard)
// — those belong to TenantService. This keeps implementations trivial and
// interchangeable (OCP), mirroring appmanager.AppRepository.
type TenantRepository interface {
	// Insert stores a new tenant. Returns an error if the ID already exists.
	Insert(ctx context.Context, tenant *Tenant) error

	// FindByID returns the tenant for the given ID, or ErrNotFound.
	FindByID(ctx context.Context, id string) (*Tenant, error)

	// FindByName returns the tenant with the given name, or ErrNotFound.
	FindByName(ctx context.Context, name string) (*Tenant, error)

	// Save fully overwrites the stored tenant. Returns ErrNotFound if absent.
	Save(ctx context.Context, tenant *Tenant) error

	// Delete removes the tenant. Returns ErrNotFound if absent.
	Delete(ctx context.Context, id string) error

	// List returns all stored tenants. Returns an empty slice if none exist.
	List(ctx context.Context) ([]*Tenant, error)
}
