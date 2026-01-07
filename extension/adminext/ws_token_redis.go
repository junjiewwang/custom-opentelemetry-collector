// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// redisWSTokenManager is a Redis-based implementation of WSTokenManager.
// Suitable for distributed deployments with multiple collector replicas.
// Tokens are stored in Redis so they can be validated by any replica.
type redisWSTokenManager struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// newRedisWSTokenManager creates a new Redis-based WebSocket token manager.
func newRedisWSTokenManager(
	logger *zap.Logger,
	client redis.UniversalClient,
	keyPrefix string,
	ttl time.Duration,
) *redisWSTokenManager {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if keyPrefix == "" {
		keyPrefix = "otel:ws_token:"
	}

	return &redisWSTokenManager{
		logger:    logger,
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}
}

// tokenKey returns the Redis key for a token.
func (m *redisWSTokenManager) tokenKey(tokenStr string) string {
	return m.keyPrefix + tokenStr
}

// GenerateToken creates a new short-lived token and stores it in Redis.
func (m *redisWSTokenManager) GenerateToken(ctx context.Context, userID, purpose string) (*WSToken, error) {
	tokenStr := generateRandomToken()
	now := time.Now()
	token := &WSToken{
		Token:     tokenStr,
		UserID:    userID,
		Purpose:   purpose,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}

	// Serialize token to JSON
	data, err := json.Marshal(token)
	if err != nil {
		return nil, err
	}

	// Store in Redis with TTL
	key := m.tokenKey(tokenStr)
	if err := m.client.Set(ctx, key, data, m.ttl).Err(); err != nil {
		m.logger.Error("Failed to store WS token in Redis",
			zap.String("key", key),
			zap.Error(err),
		)
		return nil, err
	}

	m.logger.Debug("WS token generated and stored in Redis",
		zap.String("purpose", purpose),
		zap.Duration("ttl", m.ttl),
	)

	return token, nil
}

// ValidateAndConsume validates a token and removes it atomically (single-use).
// Uses GETDEL (Redis 6.2+) for atomic get-and-delete operation.
// Falls back to GET + DEL for older Redis versions.
func (m *redisWSTokenManager) ValidateAndConsume(ctx context.Context, tokenStr, purpose string) *WSToken {
	key := m.tokenKey(tokenStr)

	// Try GETDEL first (Redis 6.2+) for atomic operation
	data, err := m.client.GetDel(ctx, key).Bytes()
	if err == redis.Nil {
		// Token not found
		m.logger.Debug("WS token not found in Redis",
			zap.String("token_prefix", tokenStr[:min(8, len(tokenStr))]),
		)
		return nil
	}
	if err != nil {
		// GETDEL might not be supported, fallback to GET + DEL
		m.logger.Debug("GETDEL failed, falling back to GET+DEL", zap.Error(err))
		return m.validateAndConsumeFallback(ctx, key, tokenStr, purpose)
	}

	return m.parseAndValidateToken(data, purpose)
}

// validateAndConsumeFallback uses GET + DEL for Redis versions < 6.2.
func (m *redisWSTokenManager) validateAndConsumeFallback(ctx context.Context, key, tokenStr, purpose string) *WSToken {
	// GET the token
	data, err := m.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		m.logger.Warn("Failed to get WS token from Redis",
			zap.String("token_prefix", tokenStr[:min(8, len(tokenStr))]),
			zap.Error(err),
		)
		return nil
	}

	// DEL the token (consume it)
	// Note: There's a small race window here where another request could
	// also validate the same token. For most use cases this is acceptable.
	if err := m.client.Del(ctx, key).Err(); err != nil {
		m.logger.Warn("Failed to delete WS token from Redis",
			zap.String("token_prefix", tokenStr[:min(8, len(tokenStr))]),
			zap.Error(err),
		)
		// Continue anyway - token was validated
	}

	return m.parseAndValidateToken(data, purpose)
}

// parseAndValidateToken parses JSON data and validates the token.
func (m *redisWSTokenManager) parseAndValidateToken(data []byte, purpose string) *WSToken {
	var token WSToken
	if err := json.Unmarshal(data, &token); err != nil {
		m.logger.Warn("Failed to unmarshal WS token", zap.Error(err))
		return nil
	}

	// Check expiration (Redis TTL should handle this, but double-check)
	if time.Now().After(token.ExpiresAt) {
		m.logger.Debug("WS token expired")
		return nil
	}

	// Check purpose if specified
	if purpose != "" && token.Purpose != purpose {
		m.logger.Debug("WS token purpose mismatch",
			zap.String("expected", purpose),
			zap.String("actual", token.Purpose),
		)
		return nil
	}

	m.logger.Debug("WS token validated and consumed",
		zap.String("purpose", token.Purpose),
	)

	return &token
}

// Count returns the approximate number of active tokens.
// Uses SCAN to count keys matching the prefix.
func (m *redisWSTokenManager) Count(ctx context.Context) int {
	var count int
	var cursor uint64
	pattern := m.keyPrefix + "*"

	for {
		keys, nextCursor, err := m.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			m.logger.Warn("Failed to scan WS tokens in Redis", zap.Error(err))
			return count
		}
		count += len(keys)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return count
}

// Close releases resources. Redis client is shared, so we don't close it.
func (m *redisWSTokenManager) Close() error {
	return nil
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
