// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

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

// AppInfo represents an application group.
type AppInfo struct {
	// ID is the unique identifier for the app.
	ID string `json:"id"`

	// Name is the display name of the app.
	Name string `json:"name"`

	// Token is the authentication token for agents.
	Token string `json:"token"`

	// Description is an optional description.
	Description string `json:"description,omitempty"`

	// CreatedAt is when the app was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the app was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// Metadata holds additional key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`

	// AgentCount is the number of registered agents (computed).
	AgentCount int `json:"agent_count,omitempty"`

	// Status is the app status: "active", "disabled".
	Status string `json:"status"`
}

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

// TokenValidationResult holds the result of token validation.
type TokenValidationResult struct {
	Valid   bool   `json:"valid"`
	AppID   string `json:"app_id,omitempty"`
	AppName string `json:"app_name,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// TokenManager manages application groups and their tokens.
type TokenManager interface {
	// CreateApp creates a new application group and generates a token.
	CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error)

	// GetApp returns app info by ID.
	GetApp(ctx context.Context, appID string) (*AppInfo, error)

	// GetAppByToken returns app info by token.
	GetAppByToken(ctx context.Context, token string) (*AppInfo, error)

	// UpdateApp updates an existing app.
	UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error)

	// DeleteApp deletes an app and invalidates its token.
	DeleteApp(ctx context.Context, appID string) error

	// ListApps returns all apps.
	ListApps(ctx context.Context) ([]*AppInfo, error)

	// ValidateToken validates a token and returns the associated app info.
	ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error)

	// RegenerateToken generates a new token for an app (invalidates old token).
	RegenerateToken(ctx context.Context, appID string) (*AppInfo, error)

	// SetToken sets a custom token for an app (invalidates old token).
	// If the token is empty, a new token will be generated.
	SetToken(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error)

	// Start initializes the token manager.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// Config holds configuration for TokenManager.
type Config struct {
	// Type specifies the backend type: "memory" or "redis".
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from storage extension.
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for Redis keys.
	KeyPrefix string `mapstructure:"key_prefix"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:      "memory",
		RedisName: "default",
		KeyPrefix: "otel:apps",
	}
}
