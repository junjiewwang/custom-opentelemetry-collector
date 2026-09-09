// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"errors"
)

// ErrKeyNotFound is returned when an API key does not exist.
var ErrKeyNotFound = errors.New("api key not found")

// APIKeyRepository is a narrow, storage-agnostic persistence abstraction for
// TenantAPIKey. It contains no business rules (no tenant-status check, no
// revocation logic) — those belong to APIKeyService.
type APIKeyRepository interface {
	// Insert stores a new key. Returns an error if the ID or hash already exists.
	Insert(ctx context.Context, key *TenantAPIKey) error

	// FindByHash returns the key whose SHA-256 hash matches, or ErrKeyNotFound.
	// This is the hot path for key validation.
	FindByHash(ctx context.Context, keyHash string) (*TenantAPIKey, error)

	// FindByID returns the key with the given ID, or ErrKeyNotFound.
	FindByID(ctx context.Context, keyID string) (*TenantAPIKey, error)

	// ListByTenant returns all keys (active and revoked) for a tenant.
	ListByTenant(ctx context.Context, tenantID string) ([]*TenantAPIKey, error)

	// Save overwrites the stored key (used to revoke by flipping Status).
	Save(ctx context.Context, key *TenantAPIKey) error

	// Delete removes the key and its hash index. Returns ErrKeyNotFound if absent.
	Delete(ctx context.Context, keyID string) error
}
