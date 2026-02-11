// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// ============================================================================
// Artifact Download
// ============================================================================

// handleGetTaskArtifact handles GET /api/v2/tasks/{taskID}/artifact
//
// Downloads the artifact (profiling data, heap dump, etc.) associated with
// a completed task. The artifact is stored in BlobStore after chunked upload.
func (e *Extension) handleGetTaskArtifact(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	if taskID == "" {
		e.handleError(w, errBadRequest("taskID is required"))
		return
	}

	if e.blobStore == nil {
		e.handleError(w, errNotImplemented("blob store not configured"))
		return
	}

	// Resolve blob key from TaskResult.ArtifactRef (set by ArtifactManager),
	// falling back to taskID if ArtifactRef is empty (backward compatibility).
	blobKey := e.resolveArtifactKey(r, taskID)

	// Get metadata first to set Content-Length and other headers
	meta, err := e.blobStore.GetMeta(r.Context(), blobKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			e.handleError(w, errNotFound("artifact not found for task: "+taskID))
			return
		}
		e.logger.Error("Failed to get artifact metadata", zap.String("task_id", taskID), zap.Error(err))
		e.handleError(w, errInternal("failed to retrieve artifact metadata"))
		return
	}

	// Get the blob data
	reader, err := e.blobStore.Get(r.Context(), blobKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			e.handleError(w, errNotFound("artifact not found for task: "+taskID))
			return
		}
		e.logger.Error("Failed to get artifact data", zap.String("task_id", taskID), zap.Error(err))
		e.handleError(w, errInternal("failed to retrieve artifact"))
		return
	}
	defer reader.Close()

	// Set response headers
	contentType := "application/octet-stream"
	if meta.ContentType != "" {
		contentType = meta.ContentType
	}
	w.Header().Set("Content-Type", contentType)

	if meta.Size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	}

	// Set Content-Disposition for download
	filename := taskID
	if meta.Metadata != nil {
		if name, ok := meta.Metadata["filename"]; ok && name != "" {
			filename = name
		}
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	w.WriteHeader(http.StatusOK)

	// Stream the blob to the response
	written, err := io.Copy(w, reader)
	if err != nil {
		e.logger.Error("Failed to stream artifact",
			zap.String("task_id", taskID),
			zap.Int64("bytes_written", written),
			zap.Error(err),
		)
		return
	}

	e.logger.Debug("Artifact downloaded",
		zap.String("task_id", taskID),
		zap.Int64("bytes_sent", written),
	)
}

// handleGetTaskArtifactMeta handles GET /api/v2/tasks/{taskID}/artifact/meta
//
// Returns metadata about the artifact without downloading the actual data.
func (e *Extension) handleGetTaskArtifactMeta(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	if taskID == "" {
		e.handleError(w, errBadRequest("taskID is required"))
		return
	}

	if e.blobStore == nil {
		e.handleError(w, errNotImplemented("blob store not configured"))
		return
	}

	blobKey := e.resolveArtifactKey(r, taskID)

	meta, err := e.blobStore.GetMeta(r.Context(), blobKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			e.handleError(w, errNotFound("artifact not found for task: "+taskID))
			return
		}
		e.logger.Error("Failed to get artifact metadata", zap.String("task_id", taskID), zap.Error(err))
		e.handleError(w, errInternal("failed to retrieve artifact metadata"))
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"task_id":      taskID,
		"blob_key":     meta.Key,
		"size":         meta.Size,
		"content_type": meta.ContentType,
		"metadata":     meta.Metadata,
		"created_at":   meta.CreatedAt,
	})
}

// resolveArtifactKey looks up the TaskResult.ArtifactRef for the given taskID.
// If ArtifactRef is set, it is used as the blob key; otherwise, falls back to
// the taskID alone (backward compatibility with older uploads).
func (e *Extension) resolveArtifactKey(r *http.Request, taskID string) string {
	if e.taskMgr != nil {
		result, found, err := e.taskMgr.GetTaskResult(r.Context(), taskID)
		if err == nil && found && result != nil && result.ArtifactRef != "" {
			return result.ArtifactRef
		}
	}
	return taskID
}
