// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// Ensure memoryChunkStore implements ChunkStore.
var _ ChunkStore = (*memoryChunkStore)(nil)

// chunkUpload tracks an ongoing chunked upload in memory.
type chunkUpload struct {
	key         string
	taskID      string
	uploadID    string
	totalChunks int32
	chunks      map[int32][]byte
	createdAt   time.Time
	fileName    string
	contentType string
}

// memoryChunkStore implements ChunkStore using in-memory storage.
type memoryChunkStore struct {
	logger *zap.Logger
	cfg    ChunkManagerConfig

	mu      sync.Mutex
	uploads map[string]*chunkUpload

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewMemoryChunkStore creates an in-memory chunk store.
func NewMemoryChunkStore(logger *zap.Logger, cfg ChunkManagerConfig) ChunkStore {
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = 1 * time.Hour
	}

	s := &memoryChunkStore{
		logger:  logger,
		cfg:     cfg,
		uploads: make(map[string]*chunkUpload),
		stopCh:  make(chan struct{}),
	}

	go s.cleanupLoop()

	return s
}

// StoreChunk implements ChunkStore.
func (s *memoryChunkStore) StoreChunk(_ context.Context, key string, req *model.ChunkUpload) (int32, int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, exists := s.uploads[key]
	if !exists {
		upload = &chunkUpload{
			key:         key,
			taskID:      req.TaskID,
			uploadID:    req.UploadID,
			totalChunks: req.TotalChunks,
			chunks:      make(map[int32][]byte),
			createdAt:   time.Now(),
			fileName:    req.FileName,
			contentType: req.ContentType,
		}
		s.uploads[key] = upload
	}

	// Update file metadata from any chunk that provides it
	if req.FileName != "" && upload.fileName == "" {
		upload.fileName = req.FileName
	}
	if req.ContentType != "" && upload.contentType == "" {
		upload.contentType = req.ContentType
	}

	// Validate total chunks matches
	if upload.totalChunks != req.TotalChunks {
		return int32(len(upload.chunks)), upload.totalChunks,
			fmt.Errorf("total_chunks mismatch with existing upload")
	}

	// Store chunk
	upload.chunks[req.ChunkIndex] = req.ChunkData

	return int32(len(upload.chunks)), upload.totalChunks, nil
}

// GetCompleteUpload implements ChunkStore.
func (s *memoryChunkStore) GetCompleteUpload(_ context.Context, key string) (*CompleteUpload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, exists := s.uploads[key]
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

	result := &CompleteUpload{
		Data:        data,
		FileName:    upload.fileName,
		ContentType: upload.contentType,
	}

	// Remove completed upload
	delete(s.uploads, key)

	return result, true
}

// Close implements ChunkStore.
func (s *memoryChunkStore) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	return nil
}

// cleanupLoop periodically removes stale uploads.
func (s *memoryChunkStore) cleanupLoop() {
	ticker := time.NewTicker(s.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

// cleanup removes uploads older than the configured TTL.
func (s *memoryChunkStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-s.cfg.UploadTTL)
	for key, upload := range s.uploads {
		if upload.createdAt.Before(cutoff) {
			s.logger.Debug("Cleaning up stale upload", zap.String("key", key))
			delete(s.uploads, key)
		}
	}
}
