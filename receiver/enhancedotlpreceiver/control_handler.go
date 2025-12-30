// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"encoding/json"
	"io"
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// controlHandler handles control plane HTTP requests.
type controlHandler struct {
	logger       *zap.Logger
	controlPlane controlplaneext.ControlPlane
}

// newControlHandler creates a new control handler.
func newControlHandler(logger *zap.Logger, controlPlane controlplaneext.ControlPlane) *controlHandler {
	return &controlHandler{
		logger:       logger,
		controlPlane: controlPlane,
	}
}

// handleConfig handles GET/POST /v1/control/config
// GET - fetch current config (for agents to poll)
// POST - update config (from control plane server)
func (h *controlHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	// GET - return current config (for agent polling)
	if r.Method == http.MethodGet {
		config := h.controlPlane.GetCurrentConfig()
		h.writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: true,
			Message: "current configuration",
			Config:  config,
		})
		return
	}

	// POST - update config or fetch config (depends on body)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Handle empty body or empty JSON as config fetch request
	if len(body) == 0 || string(body) == "{}" {
		config := h.controlPlane.GetCurrentConfig()
		h.writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: true,
			Message: "current configuration",
			Config:  config,
		})
		return
	}

	var req controlplanev1.ConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// If no config provided, treat as fetch request
	if req.Config == nil {
		config := h.controlPlane.GetCurrentConfig()
		h.writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: true,
			Message: "current configuration",
			Config:  config,
		})
		return
	}

	// Update config
	if err := h.controlPlane.UpdateConfig(r.Context(), req.Config); err != nil {
		h.logger.Error("Failed to update config", zap.Error(err))
		h.writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	h.writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
		Success:        true,
		Message:        "configuration updated",
		AppliedVersion: req.Config.ConfigVersion,
	})
}

// handleTasks handles POST /v1/control/tasks
func (h *controlHandler) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	// GET - retrieve task result
	if r.Method == http.MethodGet {
		taskID := r.URL.Query().Get("task_id")
		if taskID == "" {
			// Return pending tasks
			tasks := h.controlPlane.GetPendingTasks()
			h.writeJSON(w, http.StatusOK, map[string]any{
				"pending_tasks": tasks,
			})
			return
		}

		result, found := h.controlPlane.GetTaskResult(taskID)
		if !found {
			h.writeJSON(w, http.StatusOK, &controlplanev1.TaskResultResponse{
				Found: false,
			})
			return
		}

		h.writeJSON(w, http.StatusOK, &controlplanev1.TaskResultResponse{
			Found:  true,
			Result: result,
		})
		return
	}

	// POST - submit new task
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req controlplanev1.TaskRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Task == nil {
		h.writeError(w, http.StatusBadRequest, "task is required")
		return
	}

	if err := h.controlPlane.SubmitTask(r.Context(), req.Task); err != nil {
		h.logger.Error("Failed to submit task", zap.Error(err))
		h.writeJSON(w, http.StatusOK, &controlplanev1.TaskResponse{
			Accepted: false,
			Message:  err.Error(),
		})
		return
	}

	h.writeJSON(w, http.StatusOK, &controlplanev1.TaskResponse{
		Accepted: true,
		Message:  "task submitted",
		TaskID:   req.Task.TaskID,
	})
}

// handleStatus handles POST /v1/control/status
func (h *controlHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	// GET - retrieve current status
	if r.Method == http.MethodGet {
		status := h.controlPlane.GetStatus()
		h.writeJSON(w, http.StatusOK, status)
		return
	}

	// POST - report status (from agent)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req controlplanev1.StatusRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Update health if provided
	if req.Status != nil && req.Status.Health != nil {
		h.controlPlane.UpdateHealth(req.Status.Health)
	}

	// Return pending tasks
	pendingTasks := h.controlPlane.GetPendingTasks()
	h.writeJSON(w, http.StatusOK, &controlplanev1.StatusResponse{
		Received:     true,
		Message:      "status received",
		PendingTasks: pendingTasks,
	})
}

// handleUploadChunk handles POST /v1/control/upload-chunk
func (h *controlHandler) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req controlplanev1.UploadChunkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	resp, err := h.controlPlane.UploadChunk(r.Context(), &req)
	if err != nil {
		h.logger.Error("Failed to handle chunk upload", zap.Error(err))
		h.writeJSON(w, http.StatusOK, &controlplanev1.UploadChunkResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// writeJSON writes a JSON response.
func (h *controlHandler) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.logger.Error("Failed to encode JSON response", zap.Error(err))
	}
}

// writeError writes an error response.
func (h *controlHandler) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, map[string]string{"error": message})
}
