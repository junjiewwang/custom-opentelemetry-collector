// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

func newTestChunkManager(t *testing.T) *ChunkManager {
	t.Helper()
	store := NewMemoryChunkStore(zap.NewNop(), DefaultChunkManagerConfig())
	cm := NewChunkManager(zap.NewNop(), store)
	t.Cleanup(func() { cm.Close() })
	return cm
}

func TestChunkManager_HandleChunkV2_ChecksumMismatch(t *testing.T) {
	cm := newTestChunkManager(t)

	resp, chunksReceived, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:        "t1",
		UploadID:      "u1",
		ChunkIndex:    0,
		TotalChunks:   1,
		ChunkData:     []byte("hello"),
		ChunkChecksum: "deadbeef",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, int32(0), chunksReceived)
	require.Equal(t, model.ChunkUploadStatusChecksumMismatch, resp.Status)
	require.NotEmpty(t, resp.ErrorMessage)
}

func TestChunkManager_HandleChunkV2_CompleteWithUploadID(t *testing.T) {
	cm := newTestChunkManager(t)

	resp1, chunks1, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:      "t1",
		UploadID:    "u1",
		ChunkIndex:  0,
		TotalChunks: 2,
		ChunkData:   []byte("a"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp1)
	require.Equal(t, "u1", resp1.UploadID)
	require.Equal(t, int32(1), chunks1)
	require.Equal(t, model.ChunkUploadStatusChunkReceived, resp1.Status)

	resp2, chunks2, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:      "t1",
		UploadID:    "u1",
		ChunkIndex:  1,
		TotalChunks: 2,
		ChunkData:   []byte("b"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp2)
	require.Equal(t, "u1", resp2.UploadID)
	require.Equal(t, int32(2), chunks2)
	require.Equal(t, model.ChunkUploadStatusUploadComplete, resp2.Status)
}

func TestChunkManager_HandleChunkV2_KeyFallbackToTaskID(t *testing.T) {
	cm := newTestChunkManager(t)

	hash := md5.Sum([]byte("x"))
	checksum := hex.EncodeToString(hash[:])

	resp, chunksReceived, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:        "t1",
		ChunkIndex:    0,
		TotalChunks:   1,
		ChunkData:     []byte("x"),
		ChunkChecksum: checksum,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "t1", resp.UploadID)
	require.Equal(t, int32(1), chunksReceived)
	require.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)
}

func TestChunkManager_HandleChunkV2_TotalChunksMismatch(t *testing.T) {
	cm := newTestChunkManager(t)

	_, _, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:      "t1",
		UploadID:    "u1",
		ChunkIndex:  0,
		TotalChunks: 2,
		ChunkData:   []byte("a"),
	})
	require.NoError(t, err)

	resp, chunksReceived, err := cm.HandleChunkV2(context.Background(), &model.ChunkUpload{
		TaskID:      "t1",
		UploadID:    "u1",
		ChunkIndex:  1,
		TotalChunks: 3,
		ChunkData:   []byte("b"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, int32(1), chunksReceived)
	require.Equal(t, model.ChunkUploadStatusUploadFailed, resp.Status)
	require.NotEmpty(t, resp.ErrorMessage)
}
