// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
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

// handleTasks handles GET/POST/DELETE /v1/control/tasks
func (h *controlHandler) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	// DELETE - cancel task
	if r.Method == http.MethodDelete {
		taskID := r.URL.Query().Get("task_id")
		if taskID == "" {
			h.writeError(w, http.StatusBadRequest, "task_id is required")
			return
		}

		if err := h.controlPlane.CancelTask(r.Context(), taskID); err != nil {
			h.logger.Error("Failed to cancel task", zap.Error(err))
			h.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"message": err.Error(),
			})
			return
		}

		h.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "task cancelled",
			"task_id": taskID,
		})
		return
	}

	// GET - retrieve task result or pending tasks
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

// handleTaskCancel handles POST /v1/control/tasks/cancel
func (h *controlHandler) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	var cancelled []string
	var failed []string

	for _, taskID := range req.TaskIDs {
		if err := h.controlPlane.CancelTask(r.Context(), taskID); err != nil {
			failed = append(failed, taskID)
		} else {
			cancelled = append(cancelled, taskID)
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"success":   len(failed) == 0,
		"cancelled": cancelled,
		"failed":    failed,
	})
}

// handleStatus handles POST/GET /v1/control/status
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

	// POST - report status (from agent) and get pending tasks
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

	// If agent_id is provided, update heartbeat
	if req.Status != nil && req.Status.AgentID != "" {
		agentStatus := &agentregistry.AgentStatus{
			Health: req.Status.Health,
		}
		if req.Status.Metrics != nil {
			agentStatus.ConfigVersion = req.Status.Health.CurrentConfigVersion
		}
		_ = h.controlPlane.HeartbeatAgent(r.Context(), req.Status.AgentID, agentStatus)
	}

	// Return pending tasks
	pendingTasks := h.controlPlane.GetPendingTasks()
	h.writeJSON(w, http.StatusOK, &controlplanev1.StatusResponse{
		Received:     true,
		Message:      "status received",
		PendingTasks: pendingTasks,
	})
}

// handleRegister handles POST /v1/control/register
func (h *controlHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
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

	var req agentregistry.AgentInfo
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.controlPlane.RegisterAgent(r.Context(), &req); err != nil {
		h.logger.Error("Failed to register agent", zap.Error(err))
		h.writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"agent_id": req.AgentID,
		"message":  "agent registered",
	})
}

// handleUnregister handles POST /v1/control/unregister
func (h *controlHandler) handleUnregister(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.AgentID == "" {
		h.writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}

	if err := h.controlPlane.UnregisterAgent(r.Context(), req.AgentID); err != nil {
		h.logger.Error("Failed to unregister agent", zap.Error(err))
		h.writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"agent_id": req.AgentID,
		"message":  "agent unregistered",
	})
}

// handleAgents handles GET /v1/control/agents
func (h *controlHandler) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.controlPlane == nil {
		h.writeError(w, http.StatusServiceUnavailable, "control plane not available")
		return
	}

	// Check if requesting a specific agent
	path := r.URL.Path
	if strings.HasSuffix(path, "/stats") {
		// GET /v1/control/agents/stats
		stats, err := h.controlPlane.GetAgentStats(r.Context())
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.writeJSON(w, http.StatusOK, stats)
		return
	}

	// Check for agent_id in path or query
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" {
		agent, err := h.controlPlane.GetAgent(r.Context(), agentID)
		if err != nil {
			h.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		h.writeJSON(w, http.StatusOK, agent)
		return
	}

	// Get all online agents
	agents, err := h.controlPlane.GetOnlineAgents(r.Context())
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"agents": agents,
		"total":  len(agents),
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
