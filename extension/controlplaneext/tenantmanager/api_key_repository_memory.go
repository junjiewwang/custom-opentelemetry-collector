// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"sync"
)

// MemoryAPIKeyRepository implements APIKeyRepository using in-memory maps.
// Pure data access — no business logic.
type MemoryAPIKeyRepository struct {
	mu      sync.RWMutex
	keys    map[string]*TenantAPIKey // keyID → key
	hashIdx map[string]string        // keyHash → keyID
}

// NewMemoryAPIKeyRepository creates a new in-memory APIKeyRepository.
func NewMemoryAPIKeyRepository() *MemoryAPIKeyRepository {
	return &MemoryAPIKeyRepository{
		keys:    make(map[string]*TenantAPIKey),
		hashIdx: make(map[string]string),
	}
}

var _ APIKeyRepository = (*MemoryAPIKeyRepository)(nil)

// Insert stores a new key. Returns ErrKeyNotFound if ID or hash already exists.
func (r *MemoryAPIKeyRepository) Insert(_ context.Context, key *TenantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.keys[key.ID]; exists {
		return ErrKeyNotFound
	}
	if _, exists := r.hashIdx[key.KeyHash]; exists {
		return ErrKeyNotFound
	}

	clone := *key
	r.keys[key.ID] = &clone
	r.hashIdx[key.KeyHash] = key.ID
	return nil
}

// FindByHash returns the key with the given hash, or ErrKeyNotFound.
func (r *MemoryAPIKeyRepository) FindByHash(_ context.Context, keyHash string) (*TenantAPIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyID, ok := r.hashIdx[keyHash]
	if !ok {
		return nil, ErrKeyNotFound
	}
	key, ok := r.keys[keyID]
	if !ok {
		return nil, ErrKeyNotFound
	}
	clone := *key
	return &clone, nil
}

// FindByID returns the key with the given ID, or ErrKeyNotFound.
func (r *MemoryAPIKeyRepository) FindByID(_ context.Context, keyID string) (*TenantAPIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key, ok := r.keys[keyID]
	if !ok {
		return nil, ErrKeyNotFound
	}
	clone := *key
	return &clone, nil
}

// ListByTenant returns all keys for a tenant.
func (r *MemoryAPIKeyRepository) ListByTenant(_ context.Context, tenantID string) ([]*TenantAPIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var keys []*TenantAPIKey
	for _, key := range r.keys {
		if key.TenantID == tenantID {
			clone := *key
			keys = append(keys, &clone)
		}
	}
	return keys, nil
}

// Save overwrites the stored key.
func (r *MemoryAPIKeyRepository) Save(_ context.Context, key *TenantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.keys[key.ID]; !ok {
		return ErrKeyNotFound
	}

	clone := *key
	r.keys[key.ID] = &clone
	r.hashIdx[key.KeyHash] = key.ID
	return nil
}

// Delete removes the key and its hash index.
func (r *MemoryAPIKeyRepository) Delete(_ context.Context, keyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key, ok := r.keys[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	delete(r.hashIdx, key.KeyHash)
	delete(r.keys, keyID)
	return nil
}
