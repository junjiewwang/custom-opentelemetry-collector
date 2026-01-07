// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// WSTokenManager manages short-lived tokens for WebSocket authentication.
// This provides a secure way to authenticate WebSocket connections without
// exposing long-lived API keys in URLs.
//
// Flow:
// 1. Client requests a WS token via POST /api/v1/auth/ws-token (with API key in header)
// 2. Server generates a short-lived token (default 30s TTL, single-use)
// 3. Client connects to WebSocket with the token in URL: /api/v1/arthas/ws?token=xxx
// 4. Server validates and consumes the token (one-time use)
type WSTokenManager interface {
	// GenerateToken creates a new short-lived token.
	GenerateToken(ctx context.Context, userID, purpose string) (*WSToken, error)

	// ValidateAndConsume validates a token and removes it (single-use).
	// Returns the token info if valid, nil otherwise.
	ValidateAndConsume(ctx context.Context, tokenStr, purpose string) *WSToken

	// Count returns the number of active tokens (for monitoring).
	Count(ctx context.Context) int

	// Close releases any resources.
	Close() error
}

// WSToken represents a short-lived WebSocket authentication token.
type WSToken struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id,omitempty"`
	Purpose   string    `json:"purpose"` // e.g., "arthas_terminal"
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// generateRandomToken generates a random token string.
func generateRandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// memoryWSTokenManager is an in-memory implementation of WSTokenManager.
// Suitable for single-node deployments.
type memoryWSTokenManager struct {
	mu      sync.RWMutex
	tokens  map[string]*WSToken
	ttl     time.Duration
	closeCh chan struct{}
}

// newMemoryWSTokenManager creates a new in-memory WebSocket token manager.
func newMemoryWSTokenManager(ttl time.Duration) *memoryWSTokenManager {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}

	m := &memoryWSTokenManager{
		tokens:  make(map[string]*WSToken),
		ttl:     ttl,
		closeCh: make(chan struct{}),
	}

	// Start cleanup goroutine
	go m.cleanupLoop()

	return m
}

// GenerateToken creates a new short-lived token.
func (m *memoryWSTokenManager) GenerateToken(_ context.Context, userID, purpose string) (*WSToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokenStr := generateRandomToken()
	now := time.Now()
	token := &WSToken{
		Token:     tokenStr,
		UserID:    userID,
		Purpose:   purpose,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}

	m.tokens[tokenStr] = token
	return token, nil
}

// ValidateAndConsume validates a token and removes it (single-use).
func (m *memoryWSTokenManager) ValidateAndConsume(_ context.Context, tokenStr, purpose string) *WSToken {
	m.mu.Lock()
	defer m.mu.Unlock()

	token, exists := m.tokens[tokenStr]
	if !exists {
		return nil
	}

	// Remove token (single-use)
	delete(m.tokens, tokenStr)

	// Check expiration
	if time.Now().After(token.ExpiresAt) {
		return nil
	}

	// Check purpose if specified
	if purpose != "" && token.Purpose != purpose {
		return nil
	}

	return token
}

// Count returns the number of active tokens.
func (m *memoryWSTokenManager) Count(_ context.Context) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tokens)
}

// Close stops the cleanup goroutine.
func (m *memoryWSTokenManager) Close() error {
	close(m.closeCh)
	return nil
}

// cleanupLoop periodically removes expired tokens.
func (m *memoryWSTokenManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.closeCh:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

// cleanup removes expired tokens.
func (m *memoryWSTokenManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for tokenStr, token := range m.tokens {
		if now.After(token.ExpiresAt) {
			delete(m.tokens, tokenStr)
		}
	}
}
