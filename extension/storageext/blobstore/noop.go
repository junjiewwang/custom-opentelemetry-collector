// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"context"
	"io"
)

// NoopBlobStore is a no-op implementation that discards all writes and returns ErrNotFound for reads.
// Used as the default when no blob store is configured.
type NoopBlobStore struct{}

// NewNoopBlobStore creates a no-op BlobStore.
func NewNoopBlobStore() BlobStore {
	return &NoopBlobStore{}
}

func (n *NoopBlobStore) Put(_ context.Context, _ string, r io.Reader, _ map[string]string) (int64, error) {
	return io.Copy(io.Discard, r)
}

func (n *NoopBlobStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, ErrNotFound
}

func (n *NoopBlobStore) GetMeta(_ context.Context, _ string) (*BlobMeta, error) {
	return nil, ErrNotFound
}

func (n *NoopBlobStore) Delete(_ context.Context, _ string) error {
	return nil
}

func (n *NoopBlobStore) Close() error {
	return nil
}
