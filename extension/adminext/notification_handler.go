// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/notification"
)

// ============================================================================
// Notification Management (for monitoring and retry)
// ============================================================================

// listNotifications handles GET /api/v2/notifications?status=failed&limit=50
//
// Lists notification records filtered by status.
func (h *adminHandlers) listNotifications(w http.ResponseWriter, r *http.Request) {
	if h.notificationStore == nil {
		h.handleError(w, errNotImplemented("notification store not configured"))
		return
	}

	status := notification.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = notification.StatusFailed
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := parseIntParam(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	records, err := h.notificationStore.ListByStatus(r.Context(), status, limit)
	if err != nil {
		h.logger.Error("Failed to list notifications",
			zap.String("status", string(status)),
			zap.Error(err),
		)
		h.handleError(w, errInternal("failed to list notifications"))
		return
	}

	h.writeJSON(w, http.StatusOK, listResponse("notifications", records, len(records)))
}

// getNotification handles GET /api/v2/notifications/{id}
func (h *adminHandlers) getNotification(w http.ResponseWriter, r *http.Request) {
	if h.notificationStore == nil {
		h.handleError(w, errNotImplemented("notification store not configured"))
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		h.handleError(w, errBadRequest("id is required"))
		return
	}

	record, err := h.notificationStore.Get(r.Context(), id)
	if err != nil {
		h.handleError(w, errInternal("failed to get notification"))
		return
	}
	if record == nil {
		h.handleError(w, errNotFound("notification not found"))
		return
	}

	h.writeJSON(w, http.StatusOK, record)
}

// retryNotification handles POST /api/v2/notifications/{id}/retry
//
// Retries a single failed notification.
func (h *adminHandlers) retryNotification(w http.ResponseWriter, r *http.Request) {
	if h.notificationStore == nil || h.artifactNotifier == nil {
		h.handleError(w, errNotImplemented("notification not configured"))
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		h.handleError(w, errBadRequest("id is required"))
		return
	}

	record, err := h.notificationStore.Get(r.Context(), id)
	if err != nil {
		h.handleError(w, errInternal("failed to get notification"))
		return
	}
	if record == nil {
		h.handleError(w, errNotFound("notification not found"))
		return
	}

	if record.Status == notification.StatusSent {
		h.writeJSON(w, http.StatusOK, successResponse("notification already sent"))
		return
	}

	// Mark as retrying
	record.Status = notification.StatusRetrying
	record.AttemptCount++
	_ = h.notificationStore.Update(r.Context(), record)

	// Retry the notification
	n := &notification.ArtifactNotification{
		TaskID:      record.TaskID,
		TaskType:    record.TaskType,
		Profiler:    record.Profiler,
		Event:       record.Event,
		ArtifactRef: record.ArtifactRef,
	}

	result := h.artifactNotifier.Notify(r.Context(), n)

	if result.Success {
		record.Status = notification.StatusSent
		record.LastError = ""
	} else {
		record.Status = notification.StatusFailed
		record.LastError = result.ErrorMessage
	}

	if err := h.notificationStore.Update(r.Context(), record); err != nil {
		h.logger.Warn("Failed to update notification record after retry",
			zap.String("id", id),
			zap.Error(err),
		)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"success":    result.Success,
		"id":         id,
		"status":     string(record.Status),
		"last_error": record.LastError,
	})
}

// retryAllFailedNotifications handles POST /api/v2/notifications/retry-all
//
// Retries all failed notifications (up to a limit).
func (h *adminHandlers) retryAllFailedNotifications(w http.ResponseWriter, r *http.Request) {
	if h.notificationStore == nil || h.artifactNotifier == nil {
		h.handleError(w, errNotImplemented("notification not configured"))
		return
	}

	records, err := h.notificationStore.ListByStatus(r.Context(), notification.StatusFailed, 100)
	if err != nil {
		h.handleError(w, errInternal("failed to list failed notifications"))
		return
	}

	var succeeded, failed int
	for _, record := range records {
		record.Status = notification.StatusRetrying
		record.AttemptCount++
		_ = h.notificationStore.Update(r.Context(), record)

		n := &notification.ArtifactNotification{
			TaskID:      record.TaskID,
			TaskType:    record.TaskType,
			Profiler:    record.Profiler,
			Event:       record.Event,
			ArtifactRef: record.ArtifactRef,
		}

		result := h.artifactNotifier.Notify(r.Context(), n)
		if result.Success {
			record.Status = notification.StatusSent
			record.LastError = ""
			succeeded++
		} else {
			record.Status = notification.StatusFailed
			record.LastError = result.ErrorMessage
			failed++
		}

		_ = h.notificationStore.Update(r.Context(), record)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"total":     len(records),
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// parseIntParam safely parses a string to int.
func parseIntParam(s string) (int, error) {
	var v int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadRequest("invalid number")
		}
		v = v*10 + int(c-'0')
	}
	return v, nil
}
