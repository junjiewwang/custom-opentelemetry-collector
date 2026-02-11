// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import "time"

// ChunkManagerConfig defines chunk manager settings.
type ChunkManagerConfig struct {
	// Type is the chunk store backend type.
	// Supported values: "memory" (default), "redis".
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis instance (from storage extension) to use.
	// Only used when Type is "redis". Default: "default".
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the Redis key prefix for chunk data.
	// Only used when Type is "redis". Default: "otel:chunks".
	KeyPrefix string `mapstructure:"key_prefix"`

	// CleanupInterval is how often to scan for expired uploads.
	// Only used for memory backend. Default: 5 minutes.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`

	// UploadTTL is how long an incomplete upload is kept before being cleaned up.
	// Default: 1 hour.
	UploadTTL time.Duration `mapstructure:"upload_ttl"`
}

// DefaultChunkManagerConfig returns default chunk manager configuration.
func DefaultChunkManagerConfig() ChunkManagerConfig {
	return ChunkManagerConfig{
		Type:            "",
		CleanupInterval: 5 * time.Minute,
		UploadTTL:       1 * time.Hour,
	}
}
