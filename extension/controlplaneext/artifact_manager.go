// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/notification"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// ArtifactManager handles the assembly and persistence of chunked uploads.
//
// When a chunked upload completes (all chunks received), ArtifactManager:
// 1. Retrieves the assembled data from ChunkManager
// 2. Persists it to BlobStore
// 3. Updates the TaskResult with the artifact reference
// 4. Notifies external analysis services (if configured)
type ArtifactManager struct {
	logger          *zap.Logger
	chunkMgr        *ChunkManager
	blobStore       blobstore.BlobStore
	taskMgr         taskmanager.TaskManager
	notifier        notification.Notifier
	notificationStr notification.Store
	stopCh          chan struct{}
	stopOnce        sync.Once
}

// NewArtifactManager creates a new ArtifactManager.
func NewArtifactManager(
	logger *zap.Logger,
	chunkMgr *ChunkManager,
	blobStore blobstore.BlobStore,
	taskMgr taskmanager.TaskManager,
	notifier notification.Notifier,
	notificationStore notification.Store,
) *ArtifactManager {
	if notifier == nil {
		notifier = notification.NewNoopNotifier()
	}
	return &ArtifactManager{
		logger:          logger,
		chunkMgr:        chunkMgr,
		blobStore:       blobStore,
		taskMgr:         taskMgr,
		notifier:        notifier,
		notificationStr: notificationStore,
		stopCh:          make(chan struct{}),
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
	complete, ok := am.chunkMgr.GetCompleteUpload(uploadKey)
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
		zap.String("file_name", complete.FileName),
		zap.Int("data_size", len(complete.Data)),
	)

	// Step 2: Build blob key from taskID and original file extension.
	// The key_prefix in BlobStore config handles namespace isolation,
	// so ArtifactManager only provides the business key.
	blobKey := buildArtifactKey(taskID, complete.FileName)

	// Step 3: Persist to BlobStore
	metadata := map[string]string{
		"task_id":    taskID,
		"upload_key": uploadKey,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	if complete.FileName != "" {
		metadata["filename"] = complete.FileName
	}
	if complete.ContentType != "" {
		metadata["content_type"] = complete.ContentType
	}

	written, err := am.blobStore.Put(ctx, blobKey, bytes.NewReader(complete.Data), metadata)
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

	// Step 5: Notify analysis service if applicable
	am.notifyAnalysisService(ctx, taskID, blobKey, written, metadata)
}

// buildArtifactKey constructs the blob key from taskID and an optional fileName.
// If fileName has an extension, the key uses that extension (e.g., "taskid.collapsed").
// Otherwise, falls back to just the taskID.
func buildArtifactKey(taskID, fileName string) string {
	if fileName != "" {
		if ext := filepath.Ext(fileName); ext != "" {
			return taskID + ext
		}
	}
	return taskID
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

// notifyAnalysisService triggers analysis for the persisted artifact.
// It resolves the task type and profiler/event parameters, checks the notifier whitelist,
// sends the notification, and persists the notification record for auditing and retry.
func (am *ArtifactManager) notifyAnalysisService(ctx context.Context, taskID, blobKey string, artifactSize int64, metadata map[string]string) {
	// Resolve task info (type, profiler, event) from TaskInfo
	taskType, profiler, event := am.resolveTaskInfo(ctx, taskID)
	if !am.notifier.ShouldNotify(taskType) {
		return
	}

	n := &notification.ArtifactNotification{
		TaskID:       taskID,
		TaskType:     taskType,
		Profiler:     profiler,
		Event:        event,
		ArtifactRef:  am.blobStore.FullKey(blobKey),
		ArtifactSize: artifactSize,
		Metadata:     metadata,
	}

	am.logger.Info("Notifying analysis service",
		zap.String("task_id", taskID),
		zap.String("task_type", taskType),
		zap.String("profiler", profiler),
		zap.String("event", event),
		zap.String("artifact_ref", blobKey),
	)

	result := am.notifier.Notify(ctx, n)

	// Persist notification record
	am.saveNotificationRecord(ctx, n, result)

	if result.Success {
		am.logger.Info("Analysis service notified successfully",
			zap.String("task_id", taskID),
		)
	} else {
		am.logger.Error("Failed to notify analysis service",
			zap.String("task_id", taskID),
			zap.String("task_type", taskType),
			zap.String("artifact_ref", blobKey),
			zap.Int("status_code", result.StatusCode),
			zap.String("error", result.ErrorMessage),
		)
	}
}

// resolveTaskInfo retrieves the task type, profiler, and event from TaskInfo.
// profiler is derived from taskType (e.g., "async-profiler"), event from ParametersJSON.
func (am *ArtifactManager) resolveTaskInfo(ctx context.Context, taskID string) (taskType, profiler, event string) {
	if am.taskMgr == nil {
		return "", "", ""
	}
	info, err := am.taskMgr.GetTaskStatus(ctx, taskID)
	if err != nil || info == nil || info.Task == nil {
		return "", "", ""
	}

	taskType = info.Task.TypeName

	// Task type itself represents the profiler (e.g., "async-profiler", "pprof")
	profiler = taskType

	// Extract event from ParametersJSON (e.g., "cpu", "alloc", "wall")
	if len(info.Task.ParametersJSON) > 0 {
		var params struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(info.Task.ParametersJSON, &params); err == nil {
			event = params.Event
		}
	}

	return taskType, profiler, event
}

// saveNotificationRecord persists the notification outcome for auditing and retry.
func (am *ArtifactManager) saveNotificationRecord(ctx context.Context, n *notification.ArtifactNotification, result *notification.NotifyResult) {
	if am.notificationStr == nil {
		return
	}

	status := notification.StatusSent
	if !result.Success {
		status = notification.StatusFailed
	}

	record := &notification.Record{
		ID:           uuid.New().String(),
		TaskID:       n.TaskID,
		TaskType:     n.TaskType,
		Profiler:     n.Profiler,
		Event:        n.Event,
		ArtifactRef:  n.ArtifactRef,
		Status:       status,
		AttemptCount: result.AttemptCount,
		LastError:    result.ErrorMessage,
		CreatedAt:    result.NotifiedAt,
		UpdatedAt:    result.NotifiedAt,
	}

	if err := am.notificationStr.Save(ctx, record); err != nil {
		am.logger.Warn("Failed to save notification record",
			zap.String("task_id", n.TaskID),
			zap.Error(err),
		)
	}
}

// GetNotificationStore returns the notification store (may be nil if not configured).
// Used by adminext for notification management APIs.
func (am *ArtifactManager) GetNotificationStore() notification.Store {
	return am.notificationStr
}

// GetNotifier returns the artifact notifier.
// Used by adminext for retry operations.
func (am *ArtifactManager) GetNotifier() notification.Notifier {
	return am.notifier
}

// Close stops the ArtifactManager.
func (am *ArtifactManager) Close() {
	am.stopOnce.Do(func() {
		close(am.stopCh)
	})
}
