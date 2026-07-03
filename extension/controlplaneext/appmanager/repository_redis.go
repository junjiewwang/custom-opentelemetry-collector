// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisAppRepository implements AppRepository using Redis Hashes.
// Pure data access — no business logic.
//
// Storage layout:
//   {prefix}:apps   → Hash: appID → AppInfo JSON
//   {prefix}:tokens → Hash: token → appID
type RedisAppRepository struct {
	client    redis.UniversalClient
	appsKey   string
	tokensKey string
}

// NewRedisAppRepository creates a new Redis-backed AppRepository.
func NewRedisAppRepository(client redis.UniversalClient, keyPrefix string) *RedisAppRepository {
	if keyPrefix == "" {
		keyPrefix = "otel:apps"
	}
	return &RedisAppRepository{
		client:    client,
		appsKey:   fmt.Sprintf("%s:apps", keyPrefix),
		tokensKey: fmt.Sprintf("%s:tokens", keyPrefix),
	}
}

var _ AppRepository = (*RedisAppRepository)(nil)

// Insert stores a new app atomically.
func (r *RedisAppRepository) Insert(ctx context.Context, app *AppInfo) error {
	data, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshal app: %w", err)
	}

	// Use pipeline for atomicity
	pipe := r.client.TxPipeline()
	pipe.HSetNX(ctx, r.appsKey, app.ID, string(data))
	pipe.HSetNX(ctx, r.tokensKey, app.Token, app.ID)

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert app: %w", err)
	}

	// HSetNX returns false if the field already exists
	if !cmds[0].(*redis.BoolCmd).Val() {
		return ErrNotFound
	}
	if !cmds[1].(*redis.BoolCmd).Val() {
		// Rollback the first insert
		_ = r.client.HDel(ctx, r.appsKey, app.ID).Err()
		return ErrNotFound
	}

	return nil
}

// FindByID returns the app for the given ID, or ErrNotFound.
func (r *RedisAppRepository) FindByID(ctx context.Context, id string) (*AppInfo, error) {
	data, err := r.client.HGet(ctx, r.appsKey, id).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find app by id: %w", err)
	}

	var app AppInfo
	if err := json.Unmarshal([]byte(data), &app); err != nil {
		return nil, fmt.Errorf("unmarshal app: %w", err)
	}
	return &app, nil
}

// FindByToken returns the app associated with the given token, or ErrNotFound.
func (r *RedisAppRepository) FindByToken(ctx context.Context, token string) (*AppInfo, error) {
	appID, err := r.client.HGet(ctx, r.tokensKey, token).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find token mapping: %w", err)
	}

	return r.FindByID(ctx, appID)
}

// Save fully overwrites the stored app atomically (including token switch).
func (r *RedisAppRepository) Save(ctx context.Context, app *AppInfo) error {
	// Read existing to handle token change
	existing, err := r.FindByID(ctx, app.ID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshal app: %w", err)
	}

	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, r.appsKey, app.ID, string(data))

	// Token changed → update token mapping
	if existing != nil && existing.Token != app.Token {
		pipe.HDel(ctx, r.tokensKey, existing.Token)
		pipe.HSet(ctx, r.tokensKey, app.Token, app.ID)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("save app: %w", err)
	}
	return nil
}

// Delete removes the app and its token mapping.
func (r *RedisAppRepository) Delete(ctx context.Context, id string) error {
	app, err := r.FindByID(ctx, id)
	if err != nil {
		return err
	}

	pipe := r.client.TxPipeline()
	pipe.HDel(ctx, r.appsKey, id)
	pipe.HDel(ctx, r.tokensKey, app.Token)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	return nil
}

// List returns all stored apps.
func (r *RedisAppRepository) List(ctx context.Context) ([]*AppInfo, error) {
	result, err := r.client.HGetAll(ctx, r.appsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}

	apps := make([]*AppInfo, 0, len(result))
	for _, data := range result {
		var app AppInfo
		if err := json.Unmarshal([]byte(data), &app); err != nil {
			continue // Skip invalid entries (backward compatible)
		}
		apps = append(apps, &app)
	}
	return apps, nil
}
