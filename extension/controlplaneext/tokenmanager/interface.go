// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"context"
	"time"
)

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
}

// UpdateAppRequest is the request to update an app.
type UpdateAppRequest struct {
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Status      string            `json:"status,omitempty"`
}

// TokenValidationResult holds the result of token validation.
type TokenValidationResult struct {
	Valid   bool     `json:"valid"`
	AppID   string   `json:"app_id,omitempty"`
	AppName string   `json:"app_name,omitempty"`
	Reason  string   `json:"reason,omitempty"`
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

	// TokenLength is the length of generated tokens.
	TokenLength int `mapstructure:"token_length"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:        "memory",
		RedisName:   "default",
		KeyPrefix:   "otel:apps",
		TokenLength: 32,
	}
}
