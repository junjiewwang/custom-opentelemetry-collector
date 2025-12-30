// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenauthprocessor

import (
	"errors"

	"go.opentelemetry.io/collector/component"
)

// Config defines the configuration for the token auth processor.
type Config struct {
	// AttributeKey is the resource attribute key to extract the token from.
	// Default is "token".
	AttributeKey string `mapstructure:"attribute_key"`

	// Action defines what to do when token validation fails.
	// Options: "drop" (drop the data), "pass" (pass through without validation)
	// Default is "drop".
	Action string `mapstructure:"action"`

	// ControlPlaneExtension is the name of the control plane extension to use for token validation.
	// Default is "controlplane".
	ControlPlaneExtension string `mapstructure:"control_plane_extension"`

	// LogDropped indicates whether to log dropped data due to invalid tokens.
	// Default is true.
	LogDropped bool `mapstructure:"log_dropped"`

	// Cache configuration
	Cache CacheConfig `mapstructure:"cache"`
}

// CacheConfig defines the token validation cache configuration.
type CacheConfig struct {
	// Enabled indicates whether to enable token validation caching.
	// Default is true.
	Enabled bool `mapstructure:"enabled"`

	// ValidTTL is the TTL for valid token cache entries.
	// Default is 5 minutes.
	ValidTTL int `mapstructure:"valid_ttl_seconds"`

	// InvalidTTL is the TTL for invalid token cache entries.
	// Should be shorter than ValidTTL to allow quick recovery when tokens become valid.
	// Default is 30 seconds.
	InvalidTTL int `mapstructure:"invalid_ttl_seconds"`

	// MaxSize is the maximum number of entries in the cache.
	// When exceeded, oldest entries are evicted.
	// Default is 10000.
	MaxSize int `mapstructure:"max_size"`

	// CleanupInterval is the interval for background cache cleanup.
	// Default is 60 seconds.
	CleanupInterval int `mapstructure:"cleanup_interval_seconds"`
}

var _ component.Config = (*Config)(nil)

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg.Action != "" && cfg.Action != "drop" && cfg.Action != "pass" {
		return errors.New("action must be 'drop' or 'pass'")
	}
	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() component.Config {
	return &Config{
		AttributeKey:          "token",
		Action:                "drop",
		ControlPlaneExtension: "controlplane",
		LogDropped:            true,
		Cache: CacheConfig{
			Enabled:         true,
			ValidTTL:        300, // 5 minutes
			InvalidTTL:      30,  // 30 seconds - shorter to allow quick recovery
			MaxSize:         10000,
			CleanupInterval: 60, // 1 minute
		},
	}
}
