// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package blobstore defines the BlobStore interface for binary large object storage.
//
// It provides an abstraction over different storage backends (local filesystem,
// S3/MinIO, etc.) for persisting artifacts such as profiling data, heap dumps,
// and other binary files uploaded by agents.
package blobstore

import (
	"context"
	"io"
	"path/filepath"
	"time"
)

// BlobStore is the interface for storing and retrieving binary large objects.
//
// Implementations must be safe for concurrent use.
type BlobStore interface {
	// Put writes data from reader into the store under the given key.
	// metadata contains optional key-value pairs (e.g., content-type, original filename).
	// Returns the number of bytes written or an error.
	Put(ctx context.Context, key string, reader io.Reader, metadata map[string]string) (int64, error)

	// Get returns a ReadCloser for the blob identified by key.
	// The caller MUST close the returned reader.
	// Returns ErrNotFound if the key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// GetMeta returns the metadata for the blob identified by key.
	// Returns ErrNotFound if the key does not exist.
	GetMeta(ctx context.Context, key string) (*BlobMeta, error)

	// Delete removes the blob identified by key.
	// Returns nil if the key does not exist (idempotent).
	Delete(ctx context.Context, key string) error

	// Close releases any resources held by the store.
	Close() error
}

// BlobMeta holds metadata about a stored blob.
type BlobMeta struct {
	// Key is the unique identifier of the blob.
	Key string `json:"key"`

	// Size is the size of the blob in bytes.
	Size int64 `json:"size"`

	// ContentType is the MIME type of the blob (e.g., "application/octet-stream").
	ContentType string `json:"content_type,omitempty"`

	// Metadata holds arbitrary key-value pairs associated with the blob.
	Metadata map[string]string `json:"metadata,omitempty"`

	// CreatedAt is the time the blob was stored.
	CreatedAt time.Time `json:"created_at"`
}

// blobDataSuffix returns the file suffix for the blob data object.
// If the key already has a recognized file extension, no additional suffix is appended.
// Otherwise, ".blob" is used as the default data suffix.
func blobDataSuffix(key string) string {
	if ext := filepath.Ext(key); ext != "" {
		return ""
	}
	return ".blob"
}
