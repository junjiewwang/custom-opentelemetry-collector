// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ChunkManager manages chunked uploads.
type ChunkManager struct {
	logger *zap.Logger

	mu      sync.Mutex
	uploads map[string]*chunkUpload
}

// chunkUpload tracks an ongoing chunked upload.
type chunkUpload struct {
	taskID      string
	totalChunks int32
	chunks      map[int32][]byte
	createdAt   time.Time
}

// newChunkManager creates a new chunk manager.
func newChunkManager(logger *zap.Logger) *ChunkManager {
	cm := &ChunkManager{
		logger:  logger,
		uploads: make(map[string]*chunkUpload),
	}

	// Start cleanup goroutine
	go cm.cleanupLoop()

	return cm
}

// HandleChunk handles a chunk upload request.
func (m *ChunkManager) HandleChunk(ctx context.Context, req *controlplanev1.UploadChunkRequest) (*controlplanev1.UploadChunkResponse, error) {
	if req.TaskID == "" {
		return nil, errors.New("task_id is required")
	}

	if req.TotalChunks <= 0 {
		return nil, errors.New("total_chunks must be positive")
	}

	if req.ChunkIndex < 0 || req.ChunkIndex >= req.TotalChunks {
		return nil, errors.New("chunk_index out of range")
	}

	// Verify checksum if provided
	if req.Checksum != "" {
		hash := sha256.Sum256(req.Data)
		actualChecksum := hex.EncodeToString(hash[:])
		if actualChecksum != req.Checksum {
			return &controlplanev1.UploadChunkResponse{
				Success: false,
				Message: "checksum mismatch",
			}, nil
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get or create upload
	upload, exists := m.uploads[req.TaskID]
	if !exists {
		upload = &chunkUpload{
			taskID:      req.TaskID,
			totalChunks: req.TotalChunks,
			chunks:      make(map[int32][]byte),
			createdAt:   time.Now(),
		}
		m.uploads[req.TaskID] = upload
	}

	// Validate total chunks matches
	if upload.totalChunks != req.TotalChunks {
		return &controlplanev1.UploadChunkResponse{
			Success: false,
			Message: "total_chunks mismatch with existing upload",
		}, nil
	}

	// Store chunk
	upload.chunks[req.ChunkIndex] = req.Data

	chunksReceived := int32(len(upload.chunks))
	complete := chunksReceived == upload.totalChunks

	m.logger.Debug("Chunk received",
		zap.String("task_id", req.TaskID),
		zap.Int32("chunk_index", req.ChunkIndex),
		zap.Int32("chunks_received", chunksReceived),
		zap.Int32("total_chunks", upload.totalChunks),
		zap.Bool("complete", complete),
	)

	return &controlplanev1.UploadChunkResponse{
		Success:        true,
		ChunksReceived: chunksReceived,
		Complete:       complete,
	}, nil
}

// GetCompleteUpload returns the complete data for a finished upload.
func (m *ChunkManager) GetCompleteUpload(taskID string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	upload, exists := m.uploads[taskID]
	if !exists {
		return nil, false
	}

	if int32(len(upload.chunks)) != upload.totalChunks {
		return nil, false
	}

	// Assemble data in order
	var data []byte
	for i := int32(0); i < upload.totalChunks; i++ {
		chunk, ok := upload.chunks[i]
		if !ok {
			return nil, false
		}
		data = append(data, chunk...)
	}

	// Remove completed upload
	delete(m.uploads, taskID)

	return data, true
}

// cleanupLoop periodically removes stale uploads.
func (m *ChunkManager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanup()
	}
}

// cleanup removes uploads older than 1 hour.
func (m *ChunkManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	for taskID, upload := range m.uploads {
		if upload.createdAt.Before(cutoff) {
			m.logger.Debug("Cleaning up stale upload", zap.String("task_id", taskID))
			delete(m.uploads, taskID)
		}
	}
}
