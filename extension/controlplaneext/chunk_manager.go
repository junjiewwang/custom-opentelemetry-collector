// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// CompleteUpload holds the result of a fully assembled chunked upload.
type CompleteUpload struct {
	Data        []byte
	FileName    string
	ContentType string
}

// ChunkStore abstracts chunk storage operations.
// Implementations must be safe for concurrent use.
type ChunkStore interface {
	// StoreChunk stores a single chunk for the given upload key.
	// It creates the upload entry if it doesn't exist (using metadata from req).
	// Returns (chunksReceived, totalChunks, error).
	// If totalChunks mismatch is detected, returns a non-nil error.
	StoreChunk(ctx context.Context, key string, req *model.ChunkUpload) (chunksReceived int32, totalChunks int32, err error)

	// GetCompleteUpload returns the assembled data for a finished upload
	// and removes it from the store. Returns (nil, false) if not complete or not found.
	GetCompleteUpload(ctx context.Context, key string) (*CompleteUpload, bool)

	// Close releases any resources held by the store.
	Close() error
}

// ChunkManager manages chunked uploads.
// It handles validation, checksum verification, and delegates storage to ChunkStore.
type ChunkManager struct {
	logger *zap.Logger
	store  ChunkStore
}

// NewChunkManager creates a new chunk manager with the given store backend.
func NewChunkManager(logger *zap.Logger, store ChunkStore) *ChunkManager {
	return &ChunkManager{
		logger: logger,
		store:  store,
	}
}

// chunkKey derives the storage key from a chunk upload request.
func chunkKey(req *model.ChunkUpload) string {
	if req == nil {
		return ""
	}
	if req.UploadID != "" {
		return req.UploadID
	}
	return req.TaskID
}

// HandleChunkV2 handles a chunk upload request in model format.
//
// It returns:
// - model response (for v2/probe)
// - chunksReceived count (for legacy response compatibility)
func (m *ChunkManager) HandleChunkV2(ctx context.Context, req *model.ChunkUpload) (*model.ChunkUploadResponse, int32, error) {
	if req == nil {
		return nil, 0, errors.New("request is nil")
	}
	if req.TaskID == "" {
		return nil, 0, errors.New("task_id is required")
	}
	if req.TotalChunks <= 0 {
		return nil, 0, errors.New("total_chunks must be positive")
	}
	if req.ChunkIndex < 0 || req.ChunkIndex >= req.TotalChunks {
		return nil, 0, errors.New("chunk_index out of range")
	}

	key := chunkKey(req)
	if key == "" {
		return nil, 0, errors.New("upload_id/task_id is required")
	}

	// Verify checksum if provided (MD5, aligned with probe contract)
	if req.ChunkChecksum != "" {
		hash := md5.Sum(req.ChunkData)
		actualChecksum := hex.EncodeToString(hash[:])
		if actualChecksum != req.ChunkChecksum {
			return &model.ChunkUploadResponse{
				UploadID:           key,
				ReceivedChunkIndex: req.ChunkIndex,
				Status:             model.ChunkUploadStatusChecksumMismatch,
				ErrorMessage:       "checksum mismatch",
			}, 0, nil
		}
	}

	// Delegate to store
	chunksReceived, totalChunks, err := m.store.StoreChunk(ctx, key, req)
	if err != nil {
		return &model.ChunkUploadResponse{
			UploadID:           key,
			ReceivedChunkIndex: req.ChunkIndex,
			Status:             model.ChunkUploadStatusUploadFailed,
			ErrorMessage:       err.Error(),
		}, chunksReceived, nil
	}

	complete := chunksReceived == totalChunks

	m.logger.Debug("Chunk received",
		zap.String("task_id", req.TaskID),
		zap.String("upload_id", req.UploadID),
		zap.String("key", key),
		zap.Int32("chunk_index", req.ChunkIndex),
		zap.Int32("chunks_received", chunksReceived),
		zap.Int32("total_chunks", totalChunks),
		zap.Bool("complete", complete),
	)

	status := model.ChunkUploadStatusChunkReceived
	if complete {
		status = model.ChunkUploadStatusUploadComplete
	}

	return &model.ChunkUploadResponse{
		UploadID:           key,
		ReceivedChunkIndex: req.ChunkIndex,
		Status:             status,
		ErrorMessage:       "",
	}, chunksReceived, nil
}

// GetCompleteUpload returns the complete data for a finished upload.
func (m *ChunkManager) GetCompleteUpload(key string) (*CompleteUpload, bool) {
	return m.store.GetCompleteUpload(context.Background(), key)
}

// Close stops the chunk manager and releases resources.
func (m *ChunkManager) Close() {
	_ = m.store.Close()
}
