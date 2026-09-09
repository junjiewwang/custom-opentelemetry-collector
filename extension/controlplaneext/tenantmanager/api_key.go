// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// API key type prefixes (read-path credentials). The super key (sk_) is static
// config, NOT stored here — only ok_ (operator) and tk_ (tenant) keys live in
// the repository. The prefix enables the middleware to route cheaply and lets
// logs show masked prefixes without exposing the full key.
const (
	KeyPrefixSuper    = "sk_"
	KeyPrefixOperator = "ok_"
	KeyPrefixTenant   = "tk_"
)

// Key types stored in the repository (excludes "sk", which is static).
const (
	KeyTypeOperator = "ok"
	KeyTypeTenant   = "tk"
)

// Key status values.
const (
	KeyStatusActive  = "active"
	KeyStatusRevoked = "revoked"
)

// apiKeyRandomLength is the length of the random part of a generated key.
// 32 chars of Base62 provides ~190 bits of entropy.
const apiKeyRandomLength = 32

// TenantAPIKey is a read-path credential scoped to a tenant (or the admin
// tenant for operator keys). Only the SHA-256 hash is persisted — the plaintext
// is returned once at creation and never stored.
type TenantAPIKey struct {
	// ID is the unique key identifier (Base62).
	ID string `json:"id"`

	// TenantID is the tenant this key authenticates as.
	TenantID string `json:"tenant_id"`

	// KeyType is "ok" (operator/admin) or "tk" (tenant).
	KeyType string `json:"key_type"`

	// KeyHash is the SHA-256 hex of the plaintext key.
	KeyHash string `json:"key_hash"`

	// KeyPrefix is the display prefix (type prefix + first 8 chars), e.g. "tk_xUXCbjcS".
	KeyPrefix string `json:"key_prefix"`

	// Name is a human-readable label (e.g. "Grafana Tempo").
	Name string `json:"name"`

	// Scopes limits signal access (e.g. ["trace:read","metric:read"]). Empty = all.
	Scopes []string `json:"scopes,omitempty"`

	// Status is "active" or "revoked".
	Status string `json:"status"`

	CreatedAt time.Time `json:"created_at"`
}

// CreateAPIKeyRequest is the input for creating a key. KeyType must be
// "ok" (operator, admin tenant) or "tk" (tenant).
type CreateAPIKeyRequest struct {
	Name    string   `json:"name"`
	KeyType string   `json:"key_type"`
	Scopes  []string `json:"scopes,omitempty"`
}

// Validate checks the request. Name and KeyType are required.
func (r *CreateAPIKeyRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.KeyType != KeyTypeOperator && r.KeyType != KeyTypeTenant {
		return fmt.Errorf("invalid key_type %q, must be 'ok' or 'tk'", r.KeyType)
	}
	return nil
}

// CreateAPIKeyResponse embeds the key metadata plus the one-time plaintext key.
type CreateAPIKeyResponse struct {
	TenantAPIKey
	// PlainKey is the full plaintext key, returned exactly once at creation.
	PlainKey string `json:"key"`
}

// KeyValidation is the result of validating a plaintext API key.
type KeyValidation struct {
	Valid    bool     `json:"valid"`
	TenantID string   `json:"tenant_id,omitempty"`
	KeyType  string   `json:"key_type,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// GenerateAPIKey produces a plaintext key with the type prefix, its SHA-256
// hash, and a short display prefix.
func GenerateAPIKey(keyType string) (plainKey, keyHash, keyPrefix string, err error) {
	var prefix string
	switch keyType {
	case KeyTypeOperator:
		prefix = KeyPrefixOperator
	case KeyTypeTenant:
		prefix = KeyPrefixTenant
	default:
		return "", "", "", fmt.Errorf("unsupported key type %q", keyType)
	}

	random, err := generateBase62String(apiKeyRandomLength)
	if err != nil {
		return "", "", "", err
	}

	plainKey = prefix + random
	return plainKey, sha256Hex(plainKey), prefix + random[:8], nil
}

// sha256Hex returns the SHA-256 hex digest of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
