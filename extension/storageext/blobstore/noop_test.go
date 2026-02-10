// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopBlobStore_Put(t *testing.T) {
	bs := NewNoopBlobStore()
	data := []byte("hello world")
	written, err := bs.Put(context.Background(), "key1", bytes.NewReader(data), nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), written)
}

func TestNoopBlobStore_Get(t *testing.T) {
	bs := NewNoopBlobStore()
	reader, err := bs.Get(context.Background(), "key1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
	assert.Nil(t, reader)
}

func TestNoopBlobStore_GetMeta(t *testing.T) {
	bs := NewNoopBlobStore()
	meta, err := bs.GetMeta(context.Background(), "key1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
	assert.Nil(t, meta)
}

func TestNoopBlobStore_Delete(t *testing.T) {
	bs := NewNoopBlobStore()
	err := bs.Delete(context.Background(), "key1")
	require.NoError(t, err)
}

func TestNoopBlobStore_Close(t *testing.T) {
	bs := NewNoopBlobStore()
	err := bs.Close()
	require.NoError(t, err)
}

func TestNoopBlobStore_ImplementsInterface(t *testing.T) {
	var _ BlobStore = &NoopBlobStore{}
}
