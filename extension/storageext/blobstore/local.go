// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

// localBlobStore implements BlobStore using the local filesystem.
//
// Storage layout:
//
//	<data_dir>/
//	  <key>.blob      — binary data
//	  <key>.meta.json — metadata JSON
type localBlobStore struct {
	logger  *zap.Logger
	dataDir string
	cfg     Config

	stopOnce sync.Once
	stopCh   chan struct{}
}

// localMeta is the on-disk metadata format.
type localMeta struct {
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// NewLocalBlobStore creates a local filesystem BlobStore.
func NewLocalBlobStore(logger *zap.Logger, cfg Config) (BlobStore, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("data_dir is required for local blob store")
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create blob store directory %q: %w", cfg.DataDir, err)
	}

	s := &localBlobStore{
		logger:  logger,
		dataDir: cfg.DataDir,
		cfg:     cfg,
		stopCh:  make(chan struct{}),
	}

	if cfg.TTL > 0 {
		go s.cleanupLoop()
	}

	logger.Info("Local blob store initialized",
		zap.String("data_dir", cfg.DataDir),
		zap.Int64("max_blob_size", cfg.MaxBlobSize),
		zap.Duration("ttl", cfg.TTL),
	)

	return s, nil
}

func (s *localBlobStore) blobPath(key string) string {
	return filepath.Join(s.dataDir, key+blobDataSuffix(key))
}

func (s *localBlobStore) metaPath(key string) string {
	return filepath.Join(s.dataDir, key+".meta.json")
}

// Put implements BlobStore.
func (s *localBlobStore) Put(_ context.Context, key string, reader io.Reader, metadata map[string]string) (int64, error) {
	blobFile := s.blobPath(key)
	metaFile := s.metaPath(key)

	// Ensure parent directory exists (supports nested keys like "taskID/artifact")
	dir := filepath.Dir(blobFile)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	// Write blob data to a temp file first, then rename for atomicity
	tmpFile, err := os.CreateTemp(dir, ".blob-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	var written int64
	if s.cfg.MaxBlobSize > 0 {
		// Use LimitReader to enforce max size
		limitedReader := io.LimitReader(reader, s.cfg.MaxBlobSize+1)
		written, err = io.Copy(tmpFile, limitedReader)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return 0, fmt.Errorf("failed to write blob data: %w", err)
		}
		if written > s.cfg.MaxBlobSize {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return 0, ErrTooLarge
		}
	} else {
		written, err = io.Copy(tmpFile, reader)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return 0, fmt.Errorf("failed to write blob data: %w", err)
		}
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, blobFile); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("failed to rename blob file: %w", err)
	}

	// Write metadata
	meta := localMeta{
		Key:         key,
		Size:        written,
		ContentType: metadata["content_type"],
		Metadata:    metadata,
		CreatedAt:   time.Now(),
	}

	metaData, err := json.Marshal(meta)
	if err != nil {
		return written, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(metaFile, metaData, 0o640); err != nil {
		return written, fmt.Errorf("failed to write metadata: %w", err)
	}

	s.logger.Debug("Blob stored",
		zap.String("key", key),
		zap.Int64("size", written),
	)

	return written, nil
}

// Get implements BlobStore.
func (s *localBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	blobFile := s.blobPath(key)

	f, err := os.Open(blobFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to open blob %q: %w", key, err)
	}

	return f, nil
}

// GetMeta implements BlobStore.
func (s *localBlobStore) GetMeta(_ context.Context, key string) (*BlobMeta, error) {
	metaFile := s.metaPath(key)

	data, err := os.ReadFile(metaFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read metadata %q: %w", key, err)
	}

	var meta localMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata %q: %w", key, err)
	}

	return &BlobMeta{
		Key:         meta.Key,
		Size:        meta.Size,
		ContentType: meta.ContentType,
		Metadata:    meta.Metadata,
		CreatedAt:   meta.CreatedAt,
	}, nil
}

// Delete implements BlobStore.
func (s *localBlobStore) Delete(_ context.Context, key string) error {
	blobFile := s.blobPath(key)
	metaFile := s.metaPath(key)

	_ = os.Remove(metaFile)

	if err := os.Remove(blobFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete blob %q: %w", key, err)
	}

	return nil
}

// Close implements BlobStore.
func (s *localBlobStore) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	return nil
}

// cleanupLoop periodically removes expired blobs.
func (s *localBlobStore) cleanupLoop() {
	interval := s.cfg.CleanupInterval
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

// cleanupExpired scans for and removes expired blobs.
func (s *localBlobStore) cleanupExpired() {
	if s.cfg.TTL <= 0 {
		return
	}

	cutoff := time.Now().Add(-s.cfg.TTL)
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		s.logger.Warn("Failed to read blob store directory for cleanup", zap.Error(err))
		return
	}

	var cleaned int
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		name := entry.Name()
		if len(name) <= len(".meta.json") {
			continue
		}

		metaPath := filepath.Join(s.dataDir, name)
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}

		var meta localMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		if meta.CreatedAt.Before(cutoff) {
			key := meta.Key
			if key == "" {
				// Derive key from filename
				key = name[:len(name)-len(".meta.json")]
			}
			if err := s.Delete(context.Background(), key); err != nil {
				s.logger.Debug("Failed to cleanup expired blob", zap.String("key", key), zap.Error(err))
			} else {
				cleaned++
			}
		}
	}

	if cleaned > 0 {
		s.logger.Info("Cleaned up expired blobs", zap.Int("count", cleaned))
	}
}
