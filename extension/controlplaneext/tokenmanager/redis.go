// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// Redis key suffixes
	keyApps   = "apps"   // Hash: appID -> AppInfo JSON
	keyTokens = "tokens" // Hash: token -> appID
)

// RedisTokenManager implements TokenManager using Redis.
type RedisTokenManager struct {
	logger *zap.Logger
	config Config
	client redis.UniversalClient

	started atomic.Bool
}

// NewRedisTokenManager creates a new Redis-backed token manager.
func NewRedisTokenManager(logger *zap.Logger, config Config, client redis.UniversalClient) (*RedisTokenManager, error) {
	if client == nil {
		return nil, errors.New("redis client is required")
	}

	if config.KeyPrefix == "" {
		config.KeyPrefix = "otel:apps"
	}

	return &RedisTokenManager{
		logger: logger,
		config: config,
		client: client,
	}, nil
}

// Ensure RedisTokenManager implements TokenManager.
var _ TokenManager = (*RedisTokenManager)(nil)

// Start initializes the token manager.
func (r *RedisTokenManager) Start(ctx context.Context) error {
	if r.started.Swap(true) {
		return nil
	}

	// Test Redis connection
	if err := r.client.Ping(ctx).Err(); err != nil {
		r.started.Store(false)
		return fmt.Errorf("redis connection failed: %w", err)
	}

	r.logger.Info("Starting Redis token manager",
		zap.String("key_prefix", r.config.KeyPrefix),
	)

	return nil
}

// Close releases resources.
func (r *RedisTokenManager) Close() error {
	r.started.Store(false)
	r.logger.Info("Redis token manager stopped")
	return nil
}

// appsKey returns the Redis key for apps hash.
func (r *RedisTokenManager) appsKey() string {
	return fmt.Sprintf("%s:%s", r.config.KeyPrefix, keyApps)
}

// tokensKey returns the Redis key for tokens hash.
func (r *RedisTokenManager) tokensKey() string {
	return fmt.Sprintf("%s:%s", r.config.KeyPrefix, keyTokens)
}

// CreateApp creates a new application group and generates a token.
func (r *RedisTokenManager) CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Check for duplicate name
	apps, err := r.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		if app.Name == req.Name {
			return nil, errors.New("app name already exists")
		}
	}

	// Generate ID
	id, err := GenerateID()
	if err != nil {
		return nil, err
	}

	// Use custom token if provided, otherwise generate one
	token := req.Token
	if token == "" {
		token, err = GenerateToken(0)
		if err != nil {
			return nil, err
		}
	} else {
		// Check for duplicate token
		exists, err := r.client.HExists(ctx, r.tokensKey(), token).Result()
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New("token already exists")
		}
	}

	now := time.Now()
	app := &AppInfo{
		ID:          id,
		Name:        req.Name,
		Token:       token,
		Description: req.Description,
		Metadata:    req.Metadata,
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Serialize app
	data, err := json.Marshal(app)
	if err != nil {
		return nil, err
	}

	// Use transaction to ensure atomicity
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, r.appsKey(), id, string(data))
	pipe.HSet(ctx, r.tokensKey(), token, id)

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("failed to create app: %w", err)
	}

	r.logger.Info("App created",
		zap.String("id", id),
		zap.String("name", req.Name),
	)

	return app, nil
}

// GetApp returns app info by ID.
func (r *RedisTokenManager) GetApp(ctx context.Context, appID string) (*AppInfo, error) {
	data, err := r.client.HGet(ctx, r.appsKey(), appID).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errors.New("app not found")
		}
		return nil, err
	}

	var app AppInfo
	if err := json.Unmarshal([]byte(data), &app); err != nil {
		return nil, err
	}

	return &app, nil
}

// GetAppByToken returns app info by token.
func (r *RedisTokenManager) GetAppByToken(ctx context.Context, token string) (*AppInfo, error) {
	appID, err := r.client.HGet(ctx, r.tokensKey(), token).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errors.New("invalid token")
		}
		return nil, err
	}

	return r.GetApp(ctx, appID)
}

// UpdateApp updates an existing app.
func (r *RedisTokenManager) UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error) {
	if req == nil {
		return nil, errors.New("update request is required")
	}

	app, err := r.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	// Check for duplicate name if changing
	if req.Name != "" && req.Name != app.Name {
		apps, err := r.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		for _, other := range apps {
			if other.ID != appID && other.Name == req.Name {
				return nil, errors.New("app name already exists")
			}
		}
		app.Name = req.Name
	}

	if req.Description != "" {
		app.Description = req.Description
	}

	if req.Metadata != nil {
		if app.Metadata == nil {
			app.Metadata = make(map[string]string)
		}
		for k, v := range req.Metadata {
			app.Metadata[k] = v
		}
	}

	if req.Status != "" {
		if req.Status != "active" && req.Status != "disabled" {
			return nil, errors.New("invalid status, must be 'active' or 'disabled'")
		}
		app.Status = req.Status
	}

	// Update timestamp
	app.UpdatedAt = time.Now()

	// Serialize and save
	data, err := json.Marshal(app)
	if err != nil {
		return nil, err
	}

	if err := r.client.HSet(ctx, r.appsKey(), appID, string(data)).Err(); err != nil {
		return nil, err
	}

	r.logger.Info("App updated",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return app, nil
}

// DeleteApp deletes an app and invalidates its token.
func (r *RedisTokenManager) DeleteApp(ctx context.Context, appID string) error {
	app, err := r.GetApp(ctx, appID)
	if err != nil {
		return err
	}

	// Use transaction
	pipe := r.client.TxPipeline()
	pipe.HDel(ctx, r.appsKey(), appID)
	pipe.HDel(ctx, r.tokensKey(), app.Token)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete app: %w", err)
	}

	r.logger.Info("App deleted",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return nil
}

// ListApps returns all apps.
func (r *RedisTokenManager) ListApps(ctx context.Context) ([]*AppInfo, error) {
	result, err := r.client.HGetAll(ctx, r.appsKey()).Result()
	if err != nil {
		return nil, err
	}

	apps := make([]*AppInfo, 0, len(result))
	for _, data := range result {
		var app AppInfo
		if err := json.Unmarshal([]byte(data), &app); err != nil {
			continue // Skip invalid entries
		}
		apps = append(apps, &app)
	}

	return apps, nil
}

// ValidateToken validates a token and returns the associated app info.
func (r *RedisTokenManager) ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error) {
	if token == "" {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "token is empty",
		}, nil
	}

	appID, err := r.client.HGet(ctx, r.tokensKey(), token).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return &TokenValidationResult{
				Valid:  false,
				Reason: "token not found",
			}, nil
		}
		return nil, err
	}

	app, err := r.GetApp(ctx, appID)
	if err != nil {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "app not found",
		}, nil
	}

	if app.Status != "active" {
		return &TokenValidationResult{
			Valid:  false,
			AppID:  app.ID,
			Reason: "app is disabled",
		}, nil
	}

	return &TokenValidationResult{
		Valid:   true,
		AppID:   app.ID,
		AppName: app.Name,
	}, nil
}

// RegenerateToken generates a new token for an app.
func (r *RedisTokenManager) RegenerateToken(ctx context.Context, appID string) (*AppInfo, error) {
	app, err := r.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	oldToken := app.Token

	// Generate new token
	newToken, err := GenerateToken(0)
	if err != nil {
		return nil, err
	}

	app.Token = newToken
	app.UpdatedAt = time.Now()

	// Serialize
	data, err := json.Marshal(app)
	if err != nil {
		return nil, err
	}

	// Use transaction
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, r.appsKey(), appID, string(data))
	pipe.HDel(ctx, r.tokensKey(), oldToken)
	pipe.HSet(ctx, r.tokensKey(), newToken, appID)

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("failed to regenerate token: %w", err)
	}

	r.logger.Info("Token regenerated",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return app, nil
}

// SetToken sets a custom token for an app.
// Uses Redis WATCH for optimistic locking to prevent concurrent token conflicts.
func (r *RedisTokenManager) SetToken(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Use WATCH for optimistic locking on the tokens key
	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		app, err := r.setTokenWithWatch(ctx, appID, req)
		if err == nil {
			return app, nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			// Transaction failed due to concurrent modification, retry
			lastErr = err
			continue
		}
		// Other errors, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("failed to set token after %d retries due to concurrent modifications: %w", maxRetries, lastErr)
}

// setTokenWithWatch performs the token update with WATCH for optimistic locking.
func (r *RedisTokenManager) setTokenWithWatch(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error) {
	app, err := r.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	oldToken := app.Token

	// Use custom token if provided, otherwise generate one
	newToken := req.Token
	if newToken == "" {
		newToken, err = GenerateToken(0)
		if err != nil {
			return nil, err
		}
	} else {
		// Check for duplicate token (excluding current app's token)
		existingAppID, err := r.client.HGet(ctx, r.tokensKey(), newToken).Result()
		if err == nil && existingAppID != appID {
			// Get the app name for a more friendly error message
			existingApp, _ := r.GetApp(ctx, existingAppID)
			if existingApp != nil {
				return nil, fmt.Errorf("token already in use by application '%s'", existingApp.Name)
			}
			return nil, errors.New("token already in use by another application")
		}
	}

	// If token unchanged, return early
	if newToken == oldToken {
		return app, nil
	}

	app.Token = newToken
	app.UpdatedAt = time.Now()

	// Serialize
	data, err := json.Marshal(app)
	if err != nil {
		return nil, err
	}

	// Use WATCH + MULTI/EXEC for optimistic locking
	err = r.client.Watch(ctx, func(tx *redis.Tx) error {
		// Re-check token uniqueness within the transaction
		existingAppID, err := tx.HGet(ctx, r.tokensKey(), newToken).Result()
		if err == nil && existingAppID != appID {
			return errors.New("token already in use by another application")
		}

		// Execute transaction
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, r.appsKey(), appID, string(data))
			pipe.HDel(ctx, r.tokensKey(), oldToken)
			pipe.HSet(ctx, r.tokensKey(), newToken, appID)
			return nil
		})
		return err
	}, r.tokensKey())

	if err != nil {
		return nil, err
	}

	r.logger.Info("Token set",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return app, nil
}
