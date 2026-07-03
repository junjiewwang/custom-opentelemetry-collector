// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"
)

// ═══════════════════════════════════════════════════
// Constants
// ═══════════════════════════════════════════════════

const (
	// base62Chars is the character set for Base62 encoding (URL-safe, human-friendly).
	base62Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// DefaultTokenLength is the default length for generated tokens.
	// 20 characters of Base62 provides ~119 bits of entropy, sufficient for uniqueness.
	DefaultTokenLength = 20

	// MaxTokenLength is the maximum allowed token length.
	MaxTokenLength = 64

	// DefaultIDLength is the default length for generated IDs.
	DefaultIDLength = 16
)

// ═══════════════════════════════════════════════════
// ID & Token Generation
// ═══════════════════════════════════════════════════

// GenerateToken generates a secure token using Base62 encoding.
// If length is 0, uses DefaultTokenLength. If length > MaxTokenLength, uses MaxTokenLength.
// The result is a human-friendly string containing A-Z, a-z, 0-9.
func GenerateToken(length int) (string, error) {
	if length <= 0 {
		length = DefaultTokenLength
	}
	if length > MaxTokenLength {
		length = MaxTokenLength
	}
	return generateBase62String(length)
}

// GenerateID generates a unique ID using Base62 encoding.
func GenerateID() (string, error) {
	return generateBase62String(DefaultIDLength)
}

// generateBase62String generates a random Base62 string of the specified length.
func generateBase62String(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	result := make([]byte, length)
	for i, b := range bytes {
		result[i] = base62Chars[int(b)%len(base62Chars)]
	}
	return string(result), nil
}

// ═══════════════════════════════════════════════════
// RetentionPolicy — per-signal retention overrides
// ═══════════════════════════════════════════════════

// RetentionPolicy holds per-signal retention duration overrides.
// A zero value for a signal means "no override, use platform default".
//
// JSON serialization uses Go duration strings (e.g. "720h0m0s") instead of
// raw nanosecond integers, making it human-readable and frontend-friendly.
type RetentionPolicy struct {
	Trace  time.Duration
	Metric time.Duration
	Log    time.Duration
}

// IsZero returns true if all signal durations are zero (no overrides).
func (p RetentionPolicy) IsZero() bool {
	return p.Trace == 0 && p.Metric == 0 && p.Log == 0
}

// MarshalJSON implements json.Marshaler. Durations are serialized as Go duration strings.
func (p RetentionPolicy) MarshalJSON() ([]byte, error) {
	m := make(map[string]string, 3)
	if p.Trace > 0 {
		m["trace"] = p.Trace.String()
	}
	if p.Metric > 0 {
		m["metric"] = p.Metric.String()
	}
	if p.Log > 0 {
		m["log"] = p.Log.String()
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler. Accepts duration strings like "720h0m0s".
func (p *RetentionPolicy) UnmarshalJSON(data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if v, ok := m["trace"]; ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		p.Trace = d
	}
	if v, ok := m["metric"]; ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		p.Metric = d
	}
	if v, ok := m["log"]; ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		p.Log = d
	}
	return nil
}

// ═══════════════════════════════════════════════════
// AppInfo — application aggregate root
// ═══════════════════════════════════════════════════

// AppInfo represents an application group with its identity, token, and retention configuration.
type AppInfo struct {
	// ID is the unique identifier for the app.
	ID string `json:"id"`

	// Name is the display name of the app.
	Name string `json:"name"`

	// Token is the authentication token for agents.
	Token string `json:"token"`

	// Description is an optional description.
	Description string `json:"description,omitempty"`

	// Status is the app status: "active", "disabled".
	Status string `json:"status"`

	// Metadata holds additional key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`

	// Retention holds per-signal data retention overrides.
	// Persisted together with App identity (same Redis key), no separate store needed.
	Retention RetentionPolicy `json:"retention,omitempty"`

	// CreatedAt is when the app was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the app was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// AgentCount is the number of registered agents (computed, not persisted).
	AgentCount int `json:"agent_count,omitempty"`
}

// ═══════════════════════════════════════════════════
// Request Types
// ═══════════════════════════════════════════════════

// CreateAppRequest is the request to create an app.
type CreateAppRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Token is optional. If provided, uses this token instead of generating one.
	// Must be unique and no longer than MaxTokenLength.
	Token string `json:"token,omitempty"`
}

// Validate validates the create app request.
func (r *CreateAppRequest) Validate() error {
	if r == nil {
		return errors.New("request cannot be nil")
	}
	if r.Name == "" {
		return errors.New("app name is required")
	}
	if r.Token != "" && len(r.Token) > MaxTokenLength {
		return errors.New("token exceeds maximum length")
	}
	return nil
}

// UpdateAppRequest is the request to update an app.
type UpdateAppRequest struct {
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Status      string            `json:"status,omitempty"`
}

// SetTokenRequest is the request to set a custom token for an app.
type SetTokenRequest struct {
	// Token is the custom token to set. If empty, a new token will be generated.
	Token string `json:"token"`
}

// Validate validates the set token request.
func (r *SetTokenRequest) Validate() error {
	if r == nil {
		return errors.New("request cannot be nil")
	}
	if r.Token != "" && len(r.Token) > MaxTokenLength {
		return errors.New("token exceeds maximum length")
	}
	return nil
}

// ═══════════════════════════════════════════════════
// TokenValidationResult
// ═══════════════════════════════════════════════════

// TokenValidationResult holds the result of token validation.
type TokenValidationResult struct {
	Valid   bool   `json:"valid"`
	AppID   string `json:"app_id,omitempty"`
	AppName string `json:"app_name,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
