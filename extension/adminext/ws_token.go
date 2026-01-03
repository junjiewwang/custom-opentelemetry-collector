// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// wsTokenManager manages short-lived tokens for WebSocket authentication.
// This provides a secure way to authenticate WebSocket connections without
// exposing long-lived API keys in URLs.
//
// Flow:
// 1. Client requests a WS token via POST /api/v1/auth/ws-token (with API key in header)
// 2. Server generates a short-lived token (default 30s TTL, single-use)
// 3. Client connects to WebSocket with the token in URL: /api/v1/arthas/ws?token=xxx
// 4. Server validates and consumes the token (one-time use)
type wsTokenManager struct {
	mu     sync.RWMutex
	tokens map[string]*wsToken
	ttl    time.Duration
}

// wsToken represents a short-lived WebSocket authentication token.
type wsToken struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id,omitempty"`
	Purpose   string    `json:"purpose"` // e.g., "arthas_terminal"
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// newWSTokenManager creates a new WebSocket token manager.
func newWSTokenManager(ttl time.Duration) *wsTokenManager {
	if ttl <= 0 {
		ttl = 30 * time.Second // Default 30 seconds
	}
	
	m := &wsTokenManager{
		tokens: make(map[string]*wsToken),
		ttl:    ttl,
	}
	
	// Start cleanup goroutine
	go m.cleanupLoop()
	
	return m
}

// GenerateToken creates a new short-lived token.
func (m *wsTokenManager) GenerateToken(userID, purpose string) *wsToken {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Generate random token
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tokenStr := hex.EncodeToString(b)
	
	now := time.Now()
	token := &wsToken{
		Token:     tokenStr,
		UserID:    userID,
		Purpose:   purpose,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}
	
	m.tokens[tokenStr] = token
	return token
}

// ValidateAndConsume validates a token and removes it (single-use).
// Returns the token info if valid, nil otherwise.
func (m *wsTokenManager) ValidateAndConsume(tokenStr, purpose string) *wsToken {
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

// Validate validates a token without consuming it.
// Useful for checking if a token is valid before establishing connection.
func (m *wsTokenManager) Validate(tokenStr, purpose string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	token, exists := m.tokens[tokenStr]
	if !exists {
		return false
	}
	
	// Check expiration
	if time.Now().After(token.ExpiresAt) {
		return false
	}
	
	// Check purpose if specified
	if purpose != "" && token.Purpose != purpose {
		return false
	}
	
	return true
}

// cleanupLoop periodically removes expired tokens.
func (m *wsTokenManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		m.cleanup()
	}
}

// cleanup removes expired tokens.
func (m *wsTokenManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	now := time.Now()
	for tokenStr, token := range m.tokens {
		if now.After(token.ExpiresAt) {
			delete(m.tokens, tokenStr)
		}
	}
}

// Count returns the number of active tokens (for monitoring).
func (m *wsTokenManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tokens)
}
