// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import "context"

// TokenManager is the composite interface for application group management.
// It combines AppManager (CRUD + Token lifecycle) with TokenValidator (auth hot-path)
// plus Start/Close lifecycle hooks.
//
// Implemented by AppService, which centralizes all business rules.
type TokenManager interface {
	AppManager
	TokenValidator

	// Start initializes the token manager.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// SeedAppConfig defines an app that is auto-registered on startup.
// Used for built-in apps like the collector's own internal telemetry.
type SeedAppConfig struct {
	// Name is the app name. Must be unique and non-empty.
	Name string `mapstructure:"name"`

	// Token is the fixed token for this app. If empty, a random token is generated.
	// For self-monitoring, a fixed token is recommended so it can be referenced
	// in the collector's telemetry resource config.
	Token string `mapstructure:"token"`

	// Description is an optional human-readable description.
	Description string `mapstructure:"description"`
}

// Config holds configuration for TokenManager.
type Config struct {
	// Type specifies the backend type: "memory" or "redis".
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from storage extension.
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for Redis keys.
	KeyPrefix string `mapstructure:"key_prefix"`

	// SeedApps defines apps that are automatically registered on startup.
	// If an app with the same name already exists, it is skipped (idempotent).
	// Use this for built-in apps like the collector's own internal telemetry.
	SeedApps []SeedAppConfig `mapstructure:"seed_apps"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:      "memory",
		RedisName: "default",
		KeyPrefix: "otel:apps",
	}
}
