// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ChunkManager manages chunked uploads.
type ChunkManager struct {
	logger *zap.Logger
	cfg    ChunkManagerConfig

	mu      sync.Mutex
	uploads map[string]*chunkUpload

	stopOnce sync.Once
	stopCh   chan struct{}
}

// chunkUpload tracks an ongoing chunked upload.
type chunkUpload struct {
	key         string
	taskID      string
	uploadID    string
	totalChunks int32
	chunks      map[int32][]byte
	createdAt   time.Time
}

// ChunkManagerConfig defines chunk manager settings.
type ChunkManagerConfig struct {
	// CleanupInterval is how often to scan for expired uploads.
	// Default: 5 minutes.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`

	// UploadTTL is how long an incomplete upload is kept before being cleaned up.
	// Default: 1 hour.
	UploadTTL time.Duration `mapstructure:"upload_ttl"`
}

// DefaultChunkManagerConfig returns default chunk manager configuration.
func DefaultChunkManagerConfig() ChunkManagerConfig {
	return ChunkManagerConfig{
		CleanupInterval: 5 * time.Minute,
		UploadTTL:       1 * time.Hour,
	}
}

// newChunkManager creates a new chunk manager.
func newChunkManager(logger *zap.Logger, cfg ChunkManagerConfig) *ChunkManager {
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = 1 * time.Hour
	}

	cm := &ChunkManager{
		logger:  logger,
		cfg:     cfg,
		uploads: make(map[string]*chunkUpload),
		stopCh:  make(chan struct{}),
	}

	go cm.cleanupLoop()

	return cm
}

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
	_ = ctx
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get or create upload
	upload, exists := m.uploads[key]
	if !exists {
		upload = &chunkUpload{
			key:         key,
			taskID:      req.TaskID,
			uploadID:    req.UploadID,
			totalChunks: req.TotalChunks,
			chunks:      make(map[int32][]byte),
			createdAt:   time.Now(),
		}
		m.uploads[key] = upload
	}

	// Validate total chunks matches
	if upload.totalChunks != req.TotalChunks {
		return &model.ChunkUploadResponse{
			UploadID:           key,
			ReceivedChunkIndex: req.ChunkIndex,
			Status:             model.ChunkUploadStatusUploadFailed,
			ErrorMessage:       "total_chunks mismatch with existing upload",
		}, int32(len(upload.chunks)), nil
	}

	// Store chunk
	upload.chunks[req.ChunkIndex] = req.ChunkData

	chunksReceived := int32(len(upload.chunks))
	complete := chunksReceived == upload.totalChunks

	m.logger.Debug("Chunk received",
		zap.String("task_id", req.TaskID),
		zap.String("upload_id", req.UploadID),
		zap.String("key", key),
		zap.Int32("chunk_index", req.ChunkIndex),
		zap.Int32("chunks_received", chunksReceived),
		zap.Int32("total_chunks", upload.totalChunks),
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
func (m *ChunkManager) GetCompleteUpload(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	upload, exists := m.uploads[key]
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
	delete(m.uploads, key)

	return data, true
}

// cleanupLoop periodically removes stale uploads.
func (m *ChunkManager) cleanupLoop() {
	ticker := time.NewTicker(m.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

// cleanup removes uploads older than the configured TTL.
func (m *ChunkManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-m.cfg.UploadTTL)
	for key, upload := range m.uploads {
		if upload.createdAt.Before(cutoff) {
			m.logger.Debug("Cleaning up stale upload", zap.String("key", key))
			delete(m.uploads, key)
		}
	}
}

// Close stops the cleanup goroutine and releases resources.
func (m *ChunkManager) Close() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}
