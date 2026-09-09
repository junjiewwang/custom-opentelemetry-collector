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

// RedisAPIKeyRepository implements APIKeyRepository using Redis Hashes.
// Pure data access — no business logic.
//
// Storage layout (global, low key count — flat rather than per-tenant):
//
//	{prefix}     → Hash: keyID → TenantAPIKey JSON
//	{prefix}:idx → Hash: keyHash → keyID (O(1) lookup by plaintext hash)
type RedisAPIKeyRepository struct {
	client  redis.UniversalClient
	keysKey string
	idxKey  string
}

// NewRedisAPIKeyRepository creates a new Redis-backed APIKeyRepository.
func NewRedisAPIKeyRepository(client redis.UniversalClient, keyPrefix string) *RedisAPIKeyRepository {
	if keyPrefix == "" {
		keyPrefix = "otel:tenant_keys"
	}
	return &RedisAPIKeyRepository{
		client:  client,
		keysKey: keyPrefix,
		idxKey:  fmt.Sprintf("%s:idx", keyPrefix),
	}
}

var _ APIKeyRepository = (*RedisAPIKeyRepository)(nil)

// Insert stores a new key atomically (key + hash index).
func (r *RedisAPIKeyRepository) Insert(ctx context.Context, key *TenantAPIKey) error {
	data, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("marshal api key: %w", err)
	}

	pipe := r.client.TxPipeline()
	pipe.HSetNX(ctx, r.keysKey, key.ID, string(data))
	pipe.HSetNX(ctx, r.idxKey, key.KeyHash, key.ID)

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert api key: %w", err)
	}
	if !cmds[0].(*redis.BoolCmd).Val() {
		return ErrKeyNotFound
	}
	if !cmds[1].(*redis.BoolCmd).Val() {
		_ = r.client.HDel(ctx, r.keysKey, key.ID).Err()
		return ErrKeyNotFound
	}
	return nil
}

// FindByHash returns the key with the given hash, or ErrKeyNotFound.
func (r *RedisAPIKeyRepository) FindByHash(ctx context.Context, keyHash string) (*TenantAPIKey, error) {
	keyID, err := r.client.HGet(ctx, r.idxKey, keyHash).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("find api key index: %w", err)
	}
	return r.findByID(ctx, keyID)
}

// FindByID returns the key with the given ID, or ErrKeyNotFound.
func (r *RedisAPIKeyRepository) FindByID(ctx context.Context, keyID string) (*TenantAPIKey, error) {
	return r.findByID(ctx, keyID)
}

// ListByTenant returns all keys for a tenant (scan of the keys hash).
func (r *RedisAPIKeyRepository) ListByTenant(ctx context.Context, tenantID string) ([]*TenantAPIKey, error) {
	result, err := r.client.HGetAll(ctx, r.keysKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}

	var keys []*TenantAPIKey
	for _, data := range result {
		key, err := unmarshalAPIKey(data)
		if err != nil {
			continue
		}
		if key.TenantID == tenantID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// Save overwrites the stored key (revoke flips Status).
func (r *RedisAPIKeyRepository) Save(ctx context.Context, key *TenantAPIKey) error {
	data, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("marshal api key: %w", err)
	}
	if err := r.client.HSet(ctx, r.keysKey, key.ID, string(data)).Err(); err != nil {
		return fmt.Errorf("save api key: %w", err)
	}
	return nil
}

// Delete removes the key and its hash index.
func (r *RedisAPIKeyRepository) Delete(ctx context.Context, keyID string) error {
	key, err := r.findByID(ctx, keyID)
	if err != nil {
		return err
	}

	pipe := r.client.TxPipeline()
	pipe.HDel(ctx, r.keysKey, keyID)
	pipe.HDel(ctx, r.idxKey, key.KeyHash)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	return nil
}

func (r *RedisAPIKeyRepository) findByID(ctx context.Context, keyID string) (*TenantAPIKey, error) {
	data, err := r.client.HGet(ctx, r.keysKey, keyID).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("find api key by id: %w", err)
	}
	return unmarshalAPIKey(data)
}

func unmarshalAPIKey(data string) (*TenantAPIKey, error) {
	var key TenantAPIKey
	if err := json.Unmarshal([]byte(data), &key); err != nil {
		return nil, fmt.Errorf("unmarshal api key: %w", err)
	}
	return &key, nil
}
