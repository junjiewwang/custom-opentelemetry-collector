// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewBlobStore(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		wantType interface{}
		wantErr  string
	}{
		{
			name:     "empty type creates noop",
			cfg:      Config{Type: ""},
			wantType: &NoopBlobStore{},
		},
		{
			name:     "noop type creates noop",
			cfg:      Config{Type: "noop"},
			wantType: &NoopBlobStore{},
		},
		{
			name:     "local type creates local blob store",
			cfg:      Config{Type: "local", DataDir: t.TempDir()},
			wantType: &localBlobStore{},
		},
		{
			name: "cos type creates cos blob store",
			cfg: Config{Type: "cos", COS: COSConfig{
				Bucket: "test-bucket", Region: "ap-guangzhou",
				SecretID: "test-id", SecretKey: "test-key",
			}},
			wantType: &cosBlobStore{},
		},
		{
			name:    "unsupported type returns error",
			cfg:     Config{Type: "s3"},
			wantErr: "unsupported blob store type: s3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bs, err := NewBlobStore(zap.NewNop(), tt.cfg)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, bs)
			assert.IsType(t, tt.wantType, bs)
			_ = bs.Close()
		})
	}
}

func TestNewBlobStore_NilLogger(t *testing.T) {
	bs, err := NewBlobStore(nil, Config{Type: "noop"})
	require.NoError(t, err)
	require.NotNil(t, bs)
	_ = bs.Close()
}

func TestNewBlobStore_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	bs, err := NewBlobStore(zap.NewNop(), Config{
		Type:    "local",
		DataDir: dir,
		// Leave MaxBlobSize, TTL, CleanupInterval as zero
	})
	require.NoError(t, err)
	require.NotNil(t, bs)

	local, ok := bs.(*localBlobStore)
	require.True(t, ok)
	assert.Equal(t, int64(512*1024*1024), local.cfg.MaxBlobSize)
	assert.Equal(t, DefaultConfig().TTL, local.cfg.TTL)
	assert.Equal(t, DefaultConfig().CleanupInterval, local.cfg.CleanupInterval)
	_ = bs.Close()
}
