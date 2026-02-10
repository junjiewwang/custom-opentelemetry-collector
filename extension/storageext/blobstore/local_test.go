// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestLocalBlobStore(t *testing.T, opts ...func(*Config)) BlobStore {
	t.Helper()
	cfg := Config{
		Type:            "local",
		DataDir:         t.TempDir(),
		MaxBlobSize:     1024 * 1024, // 1MB for tests
		TTL:             0,           // no TTL by default in tests
		CleanupInterval: 0,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.ApplyDefaults()

	bs, err := NewLocalBlobStore(zap.NewNop(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Close() })
	return bs
}

func TestLocalBlobStore_NewLocalBlobStore_MissingDataDir(t *testing.T) {
	_, err := NewLocalBlobStore(zap.NewNop(), Config{DataDir: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data_dir is required")
}

func TestLocalBlobStore_NewLocalBlobStore_CreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "blobs")
	bs, err := NewLocalBlobStore(zap.NewNop(), Config{DataDir: dir})
	require.NoError(t, err)
	defer func() { _ = bs.Close() }()

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestLocalBlobStore_PutAndGet(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	ctx := context.Background()
	data := []byte("hello blob store")
	metadata := map[string]string{"content_type": "text/plain", "filename": "test.txt"}

	// Put
	written, err := bs.Put(ctx, "test-key", bytes.NewReader(data), metadata)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), written)

	// Get
	reader, err := bs.Get(ctx, "test-key")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestLocalBlobStore_PutAndGetMeta(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	ctx := context.Background()
	data := []byte("metadata test")
	metadata := map[string]string{"content_type": "application/octet-stream", "task_id": "t1"}

	written, err := bs.Put(ctx, "meta-key", bytes.NewReader(data), metadata)
	require.NoError(t, err)

	meta, err := bs.GetMeta(ctx, "meta-key")
	require.NoError(t, err)
	assert.Equal(t, "meta-key", meta.Key)
	assert.Equal(t, written, meta.Size)
	assert.Equal(t, "application/octet-stream", meta.ContentType)
	assert.Equal(t, "t1", meta.Metadata["task_id"])
	assert.False(t, meta.CreatedAt.IsZero())
}

func TestLocalBlobStore_Get_NotFound(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	reader, err := bs.Get(context.Background(), "nonexistent")
	assert.Nil(t, reader)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLocalBlobStore_GetMeta_NotFound(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	meta, err := bs.GetMeta(context.Background(), "nonexistent")
	assert.Nil(t, meta)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLocalBlobStore_Delete(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	ctx := context.Background()

	// Put then delete
	_, err := bs.Put(ctx, "del-key", bytes.NewReader([]byte("data")), nil)
	require.NoError(t, err)

	err = bs.Delete(ctx, "del-key")
	require.NoError(t, err)

	// Verify deleted
	_, err = bs.Get(ctx, "del-key")
	assert.True(t, errors.Is(err, ErrNotFound))

	_, err = bs.GetMeta(ctx, "del-key")
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLocalBlobStore_Delete_Idempotent(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	err := bs.Delete(context.Background(), "never-existed")
	require.NoError(t, err)
}

func TestLocalBlobStore_Put_NestedKey(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	ctx := context.Background()
	data := []byte("nested data")

	written, err := bs.Put(ctx, "artifacts/task-123", bytes.NewReader(data), nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), written)

	reader, err := bs.Get(ctx, "artifacts/task-123")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestLocalBlobStore_Put_MaxBlobSizeEnforced(t *testing.T) {
	bs := newTestLocalBlobStore(t, func(cfg *Config) {
		cfg.MaxBlobSize = 10 // 10 bytes max
	})

	ctx := context.Background()

	// Within limit — should succeed
	_, err := bs.Put(ctx, "small", bytes.NewReader([]byte("hello")), nil)
	require.NoError(t, err)

	// Exceeds limit — should fail
	bigData := []byte(strings.Repeat("x", 11))
	_, err = bs.Put(ctx, "big", bytes.NewReader(bigData), nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooLarge))

	// Verify the failed blob was not persisted
	_, err = bs.Get(ctx, "big")
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLocalBlobStore_Put_NoSizeLimit(t *testing.T) {
	bs := newTestLocalBlobStore(t, func(cfg *Config) {
		cfg.MaxBlobSize = 0 // no limit
	})

	data := []byte(strings.Repeat("x", 2048))
	written, err := bs.Put(context.Background(), "no-limit", bytes.NewReader(data), nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), written)
}

func TestLocalBlobStore_Put_Overwrite(t *testing.T) {
	bs := newTestLocalBlobStore(t)
	ctx := context.Background()

	// First write
	_, err := bs.Put(ctx, "key", bytes.NewReader([]byte("first")), nil)
	require.NoError(t, err)

	// Overwrite
	_, err = bs.Put(ctx, "key", bytes.NewReader([]byte("second")), nil)
	require.NoError(t, err)

	// Verify latest data
	reader, err := bs.Get(ctx, "key")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), got)
}

func TestLocalBlobStore_CleanupExpired(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Type:            "local",
		DataDir:         dir,
		MaxBlobSize:     1024 * 1024,
		TTL:             100 * time.Millisecond,
		CleanupInterval: 50 * time.Millisecond,
	}

	bs, err := NewLocalBlobStore(zap.NewNop(), cfg)
	require.NoError(t, err)
	defer func() { _ = bs.Close() }()

	ctx := context.Background()

	// Store a blob
	_, err = bs.Put(ctx, "expiring", bytes.NewReader([]byte("data")), nil)
	require.NoError(t, err)

	// Immediately readable
	reader, err := bs.Get(ctx, "expiring")
	require.NoError(t, err)
	_ = reader.Close()

	// Wait for expiration + cleanup cycle
	time.Sleep(300 * time.Millisecond)

	// Should be cleaned up
	_, err = bs.Get(ctx, "expiring")
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLocalBlobStore_Close_StopsCleanup(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Type:            "local",
		DataDir:         dir,
		TTL:             1 * time.Hour,
		CleanupInterval: 10 * time.Millisecond,
	}

	bs, err := NewLocalBlobStore(zap.NewNop(), cfg)
	require.NoError(t, err)

	// Close should not panic and should be idempotent
	require.NoError(t, bs.Close())
	require.NoError(t, bs.Close())
}

func TestLocalBlobStore_ImplementsInterface(t *testing.T) {
	var _ BlobStore = &localBlobStore{}
}
