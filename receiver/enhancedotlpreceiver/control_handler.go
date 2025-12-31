// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"encoding/json"
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

// requireControlPlane is a middleware that checks if control plane is available.
func (h *controlHandler) requireControlPlane(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.controlPlane == nil {
			writeError(w, http.StatusServiceUnavailable, "control plane not available")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ============================================================================
// Configuration Management
// ============================================================================

// getConfig handles GET /v1/control/config - fetch current config.
func (h *controlHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	config := h.controlPlane.GetCurrentConfig()
	writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
		Success: true,
		Message: "current configuration",
		Config:  config,
	})
}

// postConfig handles POST /v1/control/config - update config or fetch config.
func (h *controlHandler) postConfig(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[controlplanev1.ConfigRequest](r)
	if err != nil {
		// Handle empty body as config fetch request
		config := h.controlPlane.GetCurrentConfig()
		writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: true,
			Message: "current configuration",
			Config:  config,
		})
		return
	}

	// If no config provided, treat as fetch request
	if req.Config == nil {
		config := h.controlPlane.GetCurrentConfig()
		writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: true,
			Message: "current configuration",
			Config:  config,
		})
		return
	}

	// Update config
	if err := h.controlPlane.UpdateConfig(r.Context(), req.Config); err != nil {
		h.logger.Error("Failed to update config", zap.Error(err))
		writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, &controlplanev1.ConfigResponse{
		Success:        true,
		Message:        "configuration updated",
		AppliedVersion: req.Config.ConfigVersion,
	})
}

// ============================================================================
// Task Management
// ============================================================================

// getTasks handles GET /v1/control/tasks - retrieve task result or pending tasks.
func (h *controlHandler) getTasks(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		// Return pending tasks
		tasks := h.controlPlane.GetPendingTasks()
		writeJSON(w, http.StatusOK, map[string]any{
			"pending_tasks": tasks,
		})
		return
	}

	result, found := h.controlPlane.GetTaskResult(taskID)
	if !found {
		writeJSON(w, http.StatusOK, &controlplanev1.TaskResultResponse{
			Found: false,
		})
		return
	}

	writeJSON(w, http.StatusOK, &controlplanev1.TaskResultResponse{
		Found:  true,
		Result: result,
	})
}

// createTask handles POST /v1/control/tasks - submit new task.
func (h *controlHandler) createTask(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[controlplanev1.TaskRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Task == nil {
		writeError(w, http.StatusBadRequest, "task is required")
		return
	}

	if err := h.controlPlane.SubmitTask(r.Context(), req.Task); err != nil {
		h.logger.Error("Failed to submit task", zap.Error(err))
		writeJSON(w, http.StatusOK, &controlplanev1.TaskResponse{
			Accepted: false,
			Message:  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, &controlplanev1.TaskResponse{
		Accepted: true,
		Message:  "task submitted",
		TaskID:   req.Task.TaskID,
	})
}

// deleteTask handles DELETE /v1/control/tasks - cancel task.
func (h *controlHandler) deleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id is required")
		return
	}

	if err := h.controlPlane.CancelTask(r.Context(), taskID); err != nil {
		h.logger.Error("Failed to cancel task", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "task cancelled",
		"task_id": taskID,
	})
}

// cancelTasks handles POST /v1/control/tasks/cancel - batch cancel tasks.
func (h *controlHandler) cancelTasks(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[taskCancelRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	var cancelled, failed []string
	for _, taskID := range req.TaskIDs {
		if err := h.controlPlane.CancelTask(r.Context(), taskID); err != nil {
			failed = append(failed, taskID)
		} else {
			cancelled = append(cancelled, taskID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":   len(failed) == 0,
		"cancelled": cancelled,
		"failed":    failed,
	})
}

type taskCancelRequest struct {
	TaskIDs []string `json:"task_ids"`
}

// ============================================================================
// Status Management
// ============================================================================

// getStatus handles GET /v1/control/status - retrieve current status.
func (h *controlHandler) getStatus(w http.ResponseWriter, r *http.Request) {
	status := h.controlPlane.GetStatus()
	writeJSON(w, http.StatusOK, status)
}

// postStatus handles POST /v1/control/status - report status and get pending tasks.
func (h *controlHandler) postStatus(w http.ResponseWriter, r *http.Request) {
	// Extract AppID from token in header
	appID := h.extractAppIDFromToken(r)

	// Read body for parsing
	req, err := decodeJSON[json.RawMessage](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	// Parse status request
	agentInfo, health, parseErr := h.parseStatusRequest(*req, appID)
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+parseErr.Error())
		return
	}

	// Update health if provided
	if health != nil {
		h.controlPlane.UpdateHealth(health)
	}

	// If agent_id is provided, auto-register or update heartbeat (upsert semantics)
	if agentInfo != nil && agentInfo.AgentID != "" {
		if err := h.controlPlane.RegisterOrHeartbeatAgent(r.Context(), agentInfo); err != nil {
			h.logger.Warn("Failed to register/heartbeat agent",
				zap.String("agent_id", agentInfo.AgentID),
				zap.Error(err),
			)
		}
	}

	// Return pending tasks
	pendingTasks := h.controlPlane.GetPendingTasks()
	writeJSON(w, http.StatusOK, &controlplanev1.StatusResponse{
		Received:     true,
		Message:      "status received",
		PendingTasks: pendingTasks,
	})
}

// ============================================================================
// Agent Management
// ============================================================================

// registerAgent handles POST /v1/control/register.
func (h *controlHandler) registerAgent(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[agentregistry.AgentInfo](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.controlPlane.RegisterAgent(r.Context(), req); err != nil {
		h.logger.Error("Failed to register agent", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"agent_id": req.AgentID,
		"message":  "agent registered",
	})
}

// unregisterAgent handles POST /v1/control/unregister.
func (h *controlHandler) unregisterAgent(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[agentUnregisterRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}

	if err := h.controlPlane.UnregisterAgent(r.Context(), req.AgentID); err != nil {
		h.logger.Error("Failed to unregister agent", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"agent_id": req.AgentID,
		"message":  "agent unregistered",
	})
}

type agentUnregisterRequest struct {
	AgentID string `json:"agent_id"`
}

// listAgents handles GET /v1/control/agents.
func (h *controlHandler) listAgents(w http.ResponseWriter, r *http.Request) {
	// Check for agent_id in query
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" {
		agent, err := h.controlPlane.GetAgent(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, agent)
		return
	}

	// Get all online agents
	agents, err := h.controlPlane.GetOnlineAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agents": agents,
		"total":  len(agents),
	})
}

// getAgentStats handles GET /v1/control/agents/stats.
func (h *controlHandler) getAgentStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.controlPlane.GetAgentStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ============================================================================
// Chunk Upload
// ============================================================================

// uploadChunk handles POST /v1/control/upload-chunk.
func (h *controlHandler) uploadChunk(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[controlplanev1.UploadChunkRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	resp, err := h.controlPlane.UploadChunk(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to handle chunk upload", zap.Error(err))
		writeJSON(w, http.StatusOK, &controlplanev1.UploadChunkResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ============================================================================
// Helper Methods
// ============================================================================

// extractAppIDFromToken extracts AppID by validating the token from HTTP header.
func (h *controlHandler) extractAppIDFromToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return ""
	}
	token := strings.TrimPrefix(authHeader, bearerPrefix)
	if token == "" {
		return ""
	}

	result, err := h.controlPlane.ValidateToken(r.Context(), token)
	if err != nil {
		h.logger.Debug("Token validation failed", zap.Error(err))
		return ""
	}

	if !result.Valid {
		h.logger.Debug("Token is invalid", zap.String("reason", result.Reason))
		return ""
	}

	return result.AppID
}

// parseStatusRequest parses status request body, supporting both nested and flat JSON formats.
func (h *controlHandler) parseStatusRequest(body json.RawMessage, appID string) (*agentregistry.AgentInfo, *controlplanev1.HealthStatus, error) {
	// First try nested format (standard format)
	var nestedReq controlplanev1.StatusRequest
	if err := json.Unmarshal(body, &nestedReq); err == nil && nestedReq.Status != nil && nestedReq.Status.AgentID != "" {
		agentInfo := &agentregistry.AgentInfo{
			AgentID:  nestedReq.Status.AgentID,
			AppID:    appID,
			Hostname: nestedReq.Status.Hostname,
			IP:       nestedReq.Status.IP,
			Version:  nestedReq.Status.Version,
			Status: &agentregistry.AgentStatus{
				Health: nestedReq.Status.Health,
			},
		}
		if nestedReq.Status.Labels != nil {
			agentInfo.Labels = nestedReq.Status.Labels
			if svcName, ok := nestedReq.Status.Labels["service.name"]; ok {
				agentInfo.ServiceName = svcName
			}
		}
		if nestedReq.Status.Health != nil && agentInfo.Status != nil {
			agentInfo.Status.ConfigVersion = nestedReq.Status.Health.CurrentConfigVersion
		}
		return agentInfo, nestedReq.Status.Health, nil
	}

	// Try flat format (Java agent format)
	var flatReq javaAgentStatusRequest
	if err := json.Unmarshal(body, &flatReq); err != nil {
		return nil, nil, err
	}

	if flatReq.AgentID == "" {
		return nil, nil, nil
	}

	agentInfo := &agentregistry.AgentInfo{
		AgentID:     flatReq.AgentID,
		AppID:       appID,
		ServiceName: flatReq.ServiceName,
		Hostname:    flatReq.Hostname,
		IP:          flatReq.IP,
		Version:     flatReq.SDKVersion,
		StartTime:   flatReq.StartupTimestamp,
		Labels: map[string]string{
			"service.name":      flatReq.ServiceName,
			"service.namespace": flatReq.ServiceNamespace,
			"process.pid":       flatReq.ProcessID,
		},
	}

	health := &controlplanev1.HealthStatus{}
	switch flatReq.RunningState {
	case "RUNNING":
		health.State = controlplanev1.HealthStateHealthy
	case "STARTING":
		health.State = controlplanev1.HealthStateDegraded
	default:
		health.State = controlplanev1.HealthStateUnhealthy
	}

	if flatReq.SpanExportStats != nil {
		health.SuccessRate = flatReq.SpanExportStats.SuccessRate
		health.SuccessCount = flatReq.SpanExportStats.SuccessCount
		health.FailureCount = flatReq.SpanExportStats.FailureCount
	}

	agentInfo.Status = &agentregistry.AgentStatus{
		Health: health,
	}

	h.logger.Debug("Parsed Java agent status request",
		zap.String("agent_id", agentInfo.AgentID),
		zap.String("app_id", agentInfo.AppID),
		zap.String("service_name", agentInfo.ServiceName),
		zap.String("hostname", agentInfo.Hostname),
	)

	return agentInfo, health, nil
}

// javaAgentStatusRequest represents the flat status format sent by Java agent.
type javaAgentStatusRequest struct {
	AgentID          string           `json:"agentId"`
	Hostname         string           `json:"hostname"`
	ProcessID        string           `json:"processId"`
	IP               string           `json:"ip"`
	SDKVersion       string           `json:"sdkVersion"`
	ServiceNamespace string           `json:"serviceNamespace"`
	ServiceName      string           `json:"serviceName"`
	StartupTimestamp int64            `json:"startupTimestamp"`
	RunningState     string           `json:"runningState"`
	UptimeMs         int64            `json:"uptimeMs"`
	Timestamp        int64            `json:"timestamp"`
	ConnectionState  string           `json:"connectionState"`
	ConfigPollCount  int64            `json:"configPollCount"`
	TaskPollCount    int64            `json:"taskPollCount"`
	StatusReportCnt  int64            `json:"statusReportCount"`
	OTLPHealthState  string           `json:"otlpHealthState"`
	SpanExportStats  *spanExportStats `json:"spanExportStats"`
}

// spanExportStats represents span export statistics from Java agent.
type spanExportStats struct {
	TotalExports int64   `json:"totalExports"`
	SuccessRate  float64 `json:"successRate"`
	SuccessCount int64   `json:"successCount"`
	FailureCount int64   `json:"failureCount"`
}
