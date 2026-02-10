// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "", cfg.Type)
	assert.Equal(t, "", cfg.DataDir)
	assert.Equal(t, int64(512*1024*1024), cfg.MaxBlobSize)
	assert.Equal(t, 24*time.Hour, cfg.TTL)
	assert.Equal(t, 30*time.Minute, cfg.CleanupInterval)
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "empty type is valid (defaults to noop)",
			cfg:  Config{Type: ""},
		},
		{
			name: "noop type is valid",
			cfg:  Config{Type: "noop"},
		},
		{
			name: "local type with data_dir is valid",
			cfg:  Config{Type: "local", DataDir: "/tmp/blobs"},
		},
		{
			name:    "local type without data_dir is invalid",
			cfg:     Config{Type: "local"},
			wantErr: "blob_store.data_dir is required when type is 'local'",
		},
		{
			name:    "unsupported type is invalid",
			cfg:     Config{Type: "minio"},
			wantErr: "blob_store.type must be 'local', 'cos', or 'noop'",
		},
		{
			name:    "negative max_blob_size is invalid",
			cfg:     Config{Type: "noop", MaxBlobSize: -1},
			wantErr: "blob_store.max_blob_size must be non-negative",
		},
		{
			name:    "negative ttl is invalid",
			cfg:     Config{Type: "noop", TTL: -1},
			wantErr: "blob_store.ttl must be non-negative",
		},
		{
			name:    "negative cleanup_interval is invalid",
			cfg:     Config{Type: "noop", CleanupInterval: -1},
			wantErr: "blob_store.cleanup_interval must be non-negative",
		},
		{
			name: "zero values for optional fields are valid",
			cfg:  Config{Type: "noop", MaxBlobSize: 0, TTL: 0, CleanupInterval: 0},
		},
		{
			name: "cos type with full config is valid",
			cfg: Config{Type: "cos", COS: COSConfig{
				Bucket: "test-bucket", Region: "ap-guangzhou",
				SecretID: "id", SecretKey: "key",
			}},
		},
		{
			name:    "cos type without bucket is invalid",
			cfg:     Config{Type: "cos", COS: COSConfig{Region: "ap-guangzhou", SecretID: "id", SecretKey: "key"}},
			wantErr: "blob_store.cos.bucket is required",
		},
		{
			name:    "cos type without region is invalid",
			cfg:     Config{Type: "cos", COS: COSConfig{Bucket: "b", SecretID: "id", SecretKey: "key"}},
			wantErr: "blob_store.cos.region is required",
		},
		{
			name:    "cos type without secret_id is invalid",
			cfg:     Config{Type: "cos", COS: COSConfig{Bucket: "b", Region: "r", SecretKey: "key"}},
			wantErr: "blob_store.cos.secret_id is required",
		},
		{
			name:    "cos type without secret_key is invalid",
			cfg:     Config{Type: "cos", COS: COSConfig{Bucket: "b", Region: "r", SecretID: "id"}},
			wantErr: "blob_store.cos.secret_key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfig_ApplyDefaults(t *testing.T) {
	t.Run("fills zero values with defaults", func(t *testing.T) {
		cfg := Config{Type: "local", DataDir: "/tmp"}
		cfg.ApplyDefaults()

		defaults := DefaultConfig()
		assert.Equal(t, defaults.MaxBlobSize, cfg.MaxBlobSize)
		assert.Equal(t, defaults.TTL, cfg.TTL)
		assert.Equal(t, defaults.CleanupInterval, cfg.CleanupInterval)
	})

	t.Run("preserves explicitly set values", func(t *testing.T) {
		cfg := Config{
			Type:            "local",
			DataDir:         "/data",
			MaxBlobSize:     1024,
			TTL:             1 * time.Hour,
			CleanupInterval: 10 * time.Minute,
		}
		cfg.ApplyDefaults()

		assert.Equal(t, int64(1024), cfg.MaxBlobSize)
		assert.Equal(t, 1*time.Hour, cfg.TTL)
		assert.Equal(t, 10*time.Minute, cfg.CleanupInterval)
	})
}
