// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"errors"
	"time"
)

// Config defines the configuration for the BlobStore.
type Config struct {
	// Type is the blob store backend type.
	// Supported values: "local", "cos", "noop".
	// Default (empty string) is treated as "noop".
	Type string `mapstructure:"type"`

	// DataDir is the root directory for local file storage.
	// Only used when Type is "local".
	DataDir string `mapstructure:"data_dir"`

	// COS holds configuration for Tencent Cloud COS backend.
	// Only used when Type is "cos".
	COS COSConfig `mapstructure:"cos"`

	// MaxBlobSize is the maximum allowed blob size in bytes.
	// 0 means no limit. Default is 512MB.
	MaxBlobSize int64 `mapstructure:"max_blob_size"`

	// TTL is the time-to-live for stored blobs.
	// Expired blobs are cleaned up periodically.
	// 0 means no expiration. Default is 24h.
	TTL time.Duration `mapstructure:"ttl"`

	// CleanupInterval is how often to scan for expired blobs.
	// Only effective when TTL > 0. Default is 30m.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
}

// COSConfig holds Tencent Cloud Object Storage configuration.
type COSConfig struct {
	// Bucket is the COS bucket name (e.g., "my-bucket-1250000000").
	Bucket string `mapstructure:"bucket"`

	// Region is the COS region (e.g., "ap-guangzhou").
	Region string `mapstructure:"region"`

	// SecretID is the Tencent Cloud API SecretID.
	SecretID string `mapstructure:"secret_id"`

	// SecretKey is the Tencent Cloud API SecretKey.
	SecretKey string `mapstructure:"secret_key"`

	// Domain is the COS service domain. Default is "myqcloud.com".
	Domain string `mapstructure:"domain"`

	// Scheme is the URL scheme. Default is "https".
	Scheme string `mapstructure:"scheme"`

	// KeyPrefix is an optional prefix prepended to all blob keys.
	// Useful for isolating data within a shared bucket (e.g., "otel-collector/blobs/").
	KeyPrefix string `mapstructure:"key_prefix"`
}

// DefaultConfig returns a Config with reasonable defaults.
func DefaultConfig() Config {
	return Config{
		Type:            "",
		DataDir:         "",
		MaxBlobSize:     512 * 1024 * 1024, // 512MB
		TTL:             24 * time.Hour,
		CleanupInterval: 30 * time.Minute,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	switch c.Type {
	case "", "local", "noop", "cos":
		// valid
	default:
		return errors.New("blob_store.type must be 'local', 'cos', or 'noop'")
	}

	if c.Type == "local" && c.DataDir == "" {
		return errors.New("blob_store.data_dir is required when type is 'local'")
	}

	if c.Type == "cos" {
		if err := c.COS.Validate(); err != nil {
			return err
		}
	}

	if c.MaxBlobSize < 0 {
		return errors.New("blob_store.max_blob_size must be non-negative")
	}

	if c.TTL < 0 {
		return errors.New("blob_store.ttl must be non-negative")
	}

	if c.CleanupInterval < 0 {
		return errors.New("blob_store.cleanup_interval must be non-negative")
	}

	return nil
}

// Validate checks if the COS configuration is valid.
func (c *COSConfig) Validate() error {
	if c.Bucket == "" {
		return errors.New("blob_store.cos.bucket is required when type is 'cos'")
	}
	if c.Region == "" {
		return errors.New("blob_store.cos.region is required when type is 'cos'")
	}
	if c.SecretID == "" {
		return errors.New("blob_store.cos.secret_id is required when type is 'cos'")
	}
	if c.SecretKey == "" {
		return errors.New("blob_store.cos.secret_key is required when type is 'cos'")
	}
	return nil
}

// ApplyDefaults sets default values for unset fields.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.MaxBlobSize == 0 {
		c.MaxBlobSize = defaults.MaxBlobSize
	}
	if c.TTL == 0 {
		c.TTL = defaults.TTL
	}
	if c.CleanupInterval == 0 {
		c.CleanupInterval = defaults.CleanupInterval
	}

	if c.Type == "cos" {
		c.COS.applyDefaults()
	}
}

// applyDefaults sets COS-specific default values.
func (c *COSConfig) applyDefaults() {
	if c.Domain == "" {
		c.Domain = "myqcloud.com"
	}
	if c.Scheme == "" {
		c.Scheme = "https"
	}
}
