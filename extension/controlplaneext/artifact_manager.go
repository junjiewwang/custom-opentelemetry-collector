// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// ArtifactManager handles the assembly and persistence of chunked uploads.
//
// When a chunked upload completes (all chunks received), ArtifactManager:
// 1. Retrieves the assembled data from ChunkManager
// 2. Persists it to BlobStore
// 3. Updates the TaskResult with the artifact reference
type ArtifactManager struct {
	logger       *zap.Logger
	chunkMgr     *ChunkManager
	blobStore    blobstore.BlobStore
	taskMgr      taskmanager.TaskManager
	stopCh       chan struct{}
	stopOnce     sync.Once
}

// NewArtifactManager creates a new ArtifactManager.
func NewArtifactManager(
	logger *zap.Logger,
	chunkMgr *ChunkManager,
	blobStore blobstore.BlobStore,
	taskMgr taskmanager.TaskManager,
) *ArtifactManager {
	return &ArtifactManager{
		logger:    logger,
		chunkMgr:  chunkMgr,
		blobStore: blobStore,
		taskMgr:   taskMgr,
		stopCh:    make(chan struct{}),
	}
}

// HandleUploadChunk processes a chunk upload and triggers persistence when complete.
//
// Returns the chunk upload response for the caller.
func (am *ArtifactManager) HandleUploadChunk(ctx context.Context, req *model.ChunkUpload) (*model.ChunkUploadResponse, error) {
	resp, _, err := am.chunkMgr.HandleChunkV2(ctx, req)
	if err != nil {
		return nil, err
	}

	// If upload is complete, persist asynchronously
	if resp != nil && resp.Status == model.ChunkUploadStatusUploadComplete {
		uploadKey := resp.UploadID
		taskID := req.TaskID
		go am.persistArtifact(uploadKey, taskID)
	}

	return resp, nil
}

// persistArtifact assembles the complete upload and persists it to BlobStore.
func (am *ArtifactManager) persistArtifact(uploadKey, taskID string) {
	// Check if we're shutting down
	select {
	case <-am.stopCh:
		am.logger.Warn("Artifact persistence skipped due to shutdown",
			zap.String("upload_key", uploadKey),
			zap.String("task_id", taskID),
		)
		return
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Step 1: Get assembled data from ChunkManager
	data, ok := am.chunkMgr.GetCompleteUpload(uploadKey)
	if !ok {
		am.logger.Warn("Complete upload data not found, may have already been consumed or expired",
			zap.String("upload_key", uploadKey),
			zap.String("task_id", taskID),
		)
		return
	}

	am.logger.Info("Persisting artifact",
		zap.String("task_id", taskID),
		zap.String("upload_key", uploadKey),
		zap.Int("data_size", len(data)),
	)

	// Step 2: Build blob key (use task_id as the primary key)
	blobKey := fmt.Sprintf("artifacts/%s", taskID)

	// Step 3: Persist to BlobStore
	metadata := map[string]string{
		"task_id":    taskID,
		"upload_key": uploadKey,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}

	written, err := am.blobStore.Put(ctx, blobKey, bytes.NewReader(data), metadata)
	if err != nil {
		am.logger.Error("Failed to persist artifact",
			zap.String("task_id", taskID),
			zap.String("blob_key", blobKey),
			zap.Error(err),
		)
		return
	}

	am.logger.Info("Artifact persisted successfully",
		zap.String("task_id", taskID),
		zap.String("blob_key", blobKey),
		zap.Int64("bytes_written", written),
	)

	// Step 4: Update TaskResult with artifact reference
	am.updateTaskResultWithArtifact(ctx, taskID, blobKey, written)
}

// updateTaskResultWithArtifact updates the TaskResult to include artifact reference info.
func (am *ArtifactManager) updateTaskResultWithArtifact(ctx context.Context, taskID, blobKey string, size int64) {
	if am.taskMgr == nil {
		return
	}

	result, _, err := am.taskMgr.GetTaskResult(ctx, taskID)
	if err != nil {
		am.logger.Debug("Failed to get task result for artifact update",
			zap.String("task_id", taskID),
			zap.Error(err),
		)
		return
	}

	if result == nil {
		am.logger.Debug("Task result not found for artifact update, creating minimal result",
			zap.String("task_id", taskID),
		)
		result = &model.TaskResult{
			TaskID:            taskID,
			Status:            model.TaskStatusSuccess,
			CompletedAtMillis: time.Now().UnixMilli(),
		}
	}

	// Set artifact reference fields
	result.ArtifactRef = blobKey
	result.ArtifactSize = size

	if err := am.taskMgr.ReportTaskResult(ctx, result); err != nil {
		am.logger.Warn("Failed to update task result with artifact reference",
			zap.String("task_id", taskID),
			zap.String("artifact_ref", blobKey),
			zap.Error(err),
		)
	}
}

// Close stops the ArtifactManager.
func (am *ArtifactManager) Close() {
	am.stopOnce.Do(func() {
		close(am.stopCh)
	})
}
