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
