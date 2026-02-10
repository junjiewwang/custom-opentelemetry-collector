// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tencentyun/cos-go-sdk-v5"
	"go.uber.org/zap"
)

// --- Validation Tests (no real COS needed) ---

func TestNewCOSBlobStore_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cos     COSConfig
		wantErr string
	}{
		{
			name:    "missing bucket",
			cos:     COSConfig{Region: "r", SecretID: "id", SecretKey: "key"},
			wantErr: "cos bucket and region are required",
		},
		{
			name:    "missing region",
			cos:     COSConfig{Bucket: "b", SecretID: "id", SecretKey: "key"},
			wantErr: "cos bucket and region are required",
		},
		{
			name:    "missing secret_id",
			cos:     COSConfig{Bucket: "b", Region: "r", SecretKey: "key"},
			wantErr: "cos secret_id and secret_key are required",
		},
		{
			name:    "missing secret_key",
			cos:     COSConfig{Bucket: "b", Region: "r", SecretID: "id"},
			wantErr: "cos secret_id and secret_key are required",
		},
		{
			name: "valid config",
			cos:  COSConfig{Bucket: "b", Region: "r", SecretID: "id", SecretKey: "key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCOSBlobStore(zap.NewNop(), Config{Type: "cos", COS: tt.cos})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCOSConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     COSConfig
		wantErr string
	}{
		{name: "empty bucket", cfg: COSConfig{Region: "r", SecretID: "id", SecretKey: "key"}, wantErr: "bucket is required"},
		{name: "empty region", cfg: COSConfig{Bucket: "b", SecretID: "id", SecretKey: "key"}, wantErr: "region is required"},
		{name: "empty secret_id", cfg: COSConfig{Bucket: "b", Region: "r", SecretKey: "key"}, wantErr: "secret_id is required"},
		{name: "empty secret_key", cfg: COSConfig{Bucket: "b", Region: "r", SecretID: "id"}, wantErr: "secret_key is required"},
		{name: "valid", cfg: COSConfig{Bucket: "b", Region: "r", SecretID: "id", SecretKey: "key"}},
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

func TestCOSConfig_ApplyDefaults(t *testing.T) {
	cfg := COSConfig{Bucket: "b", Region: "r", SecretID: "id", SecretKey: "key"}
	cfg.applyDefaults()
	assert.Equal(t, "myqcloud.com", cfg.Domain)
	assert.Equal(t, "https", cfg.Scheme)

	cfg2 := COSConfig{Bucket: "b", Region: "r", SecretID: "id", SecretKey: "key", Domain: "custom.com", Scheme: "http"}
	cfg2.applyDefaults()
	assert.Equal(t, "custom.com", cfg2.Domain)
	assert.Equal(t, "http", cfg2.Scheme)
}

func TestCOSBlobStore_ObjectKey(t *testing.T) {
	s := &cosBlobStore{keyPrefix: "prefix/"}
	assert.Equal(t, "prefix/mykey.blob", s.objectKey("mykey", ".blob"))
	assert.Equal(t, "prefix/mykey.meta.json", s.objectKey("mykey", ".meta.json"))

	s2 := &cosBlobStore{keyPrefix: ""}
	assert.Equal(t, "mykey.blob", s2.objectKey("mykey", ".blob"))
}

func TestCOSBlobStore_ImplementsInterface(t *testing.T) {
	var _ BlobStore = &cosBlobStore{}
}

func TestCOSBlobStore_Close(t *testing.T) {
	bs, err := NewCOSBlobStore(zap.NewNop(), Config{
		Type: "cos",
		COS:  COSConfig{Bucket: "b", Region: "r", SecretID: "id", SecretKey: "key"},
	})
	require.NoError(t, err)
	require.NoError(t, bs.Close())
	require.NoError(t, bs.Close()) // idempotent
}

// --- Mock COS Server Tests ---

// newTestCOSBlobStore creates a cosBlobStore backed by a mock HTTP server.
// This allows testing Put/Get/GetMeta/Delete without a real COS backend.
func newTestCOSBlobStore(t *testing.T) (*cosBlobStore, *mockCOSServer) {
	t.Helper()
	mock := newMockCOSServer()
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := cos.NewClient(&cos.BaseURL{
		BucketURL: u,
	}, &http.Client{})
	// Disable CRC64 verification for mock server tests.
	client.Conf.EnableCRC = false

	s := &cosBlobStore{
		logger:    zap.NewNop(),
		client:    client,
		cfg:       Config{Type: "cos", MaxBlobSize: 1024 * 1024},
		keyPrefix: "test/",
	}
	return s, mock
}

// mockCOSServer is an in-memory mock that mimics COS Object API behavior.
type mockCOSServer struct {
	objects map[string][]byte
}

func newMockCOSServer() *mockCOSServer {
	return &mockCOSServer{objects: make(map[string][]byte)}
}

func (m *mockCOSServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/")

	switch r.Method {
	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		m.objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		data, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodHead:
		data, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(m.objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestCOSBlobStore_PutAndGet(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	ctx := t.Context()
	data := "hello cos blob store"

	written, err := s.Put(ctx, "my-key", strings.NewReader(data), map[string]string{
		"content_type": "text/plain",
		"filename":     "test.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), written)

	reader, err := s.Get(ctx, "my-key")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, string(got))
}

func TestCOSBlobStore_PutAndGetMeta(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	ctx := t.Context()
	metadata := map[string]string{"content_type": "application/octet-stream", "task_id": "t1"}

	_, err := s.Put(ctx, "meta-key", strings.NewReader("data"), metadata)
	require.NoError(t, err)

	meta, err := s.GetMeta(ctx, "meta-key")
	require.NoError(t, err)
	assert.Equal(t, "meta-key", meta.Key)
	assert.Equal(t, "application/octet-stream", meta.ContentType)
	assert.Equal(t, "t1", meta.Metadata["task_id"])
	assert.False(t, meta.CreatedAt.IsZero())
}

func TestCOSBlobStore_Get_NotFound(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	reader, err := s.Get(t.Context(), "nonexistent")
	assert.Nil(t, reader)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCOSBlobStore_GetMeta_NotFound(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	meta, err := s.GetMeta(t.Context(), "nonexistent")
	assert.Nil(t, meta)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCOSBlobStore_Delete(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	ctx := t.Context()

	_, err := s.Put(ctx, "del-key", strings.NewReader("data"), nil)
	require.NoError(t, err)

	err = s.Delete(ctx, "del-key")
	require.NoError(t, err)

	_, err = s.Get(ctx, "del-key")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCOSBlobStore_Delete_Idempotent(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	err := s.Delete(t.Context(), "never-existed")
	require.NoError(t, err)
}

func TestCOSBlobStore_Put_MaxBlobSizeEnforced(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	s.cfg.MaxBlobSize = 10

	// Within limit
	_, err := s.Put(t.Context(), "small", strings.NewReader("hello"), nil)
	require.NoError(t, err)

	// Exceeds limit
	_, err = s.Put(t.Context(), "big", strings.NewReader(strings.Repeat("x", 11)), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestCOSBlobStore_Put_NestedKey(t *testing.T) {
	s, _ := newTestCOSBlobStore(t)
	ctx := t.Context()

	_, err := s.Put(ctx, "artifacts/task-123", strings.NewReader("nested"), nil)
	require.NoError(t, err)

	reader, err := s.Get(ctx, "artifacts/task-123")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "nested", string(got))
}

func TestCOSBlobStore_Put_MetadataStored(t *testing.T) {
	s, mock := newTestCOSBlobStore(t)
	ctx := t.Context()

	_, err := s.Put(ctx, "check-meta", strings.NewReader("data"), map[string]string{"foo": "bar"})
	require.NoError(t, err)

	// Verify metadata JSON was stored in COS
	metaKey := "test/check-meta.meta.json"
	rawMeta, ok := mock.objects[metaKey]
	require.True(t, ok, "metadata object should exist in COS")

	var meta cosMeta
	require.NoError(t, json.Unmarshal(rawMeta, &meta))
	assert.Equal(t, "check-meta", meta.Key)
	assert.Equal(t, "bar", meta.Metadata["foo"])
}
