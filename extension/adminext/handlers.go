// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// handleHealth handles GET /health
func (e *Extension) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleConfigs handles GET/POST /api/v1/configs
func (e *Extension) handleConfigs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// List all configs (for now, just return current config)
		config, err := e.configMgr.GetConfig(r.Context())
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		e.writeJSON(w, http.StatusOK, map[string]any{
			"configs": []*controlplanev1.AgentConfig{config},
		})

	case http.MethodPost:
		// Create/update config
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var config controlplanev1.AgentConfig
		if err := json.Unmarshal(body, &config); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if err := e.configMgr.UpdateConfig(r.Context(), &config); err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "config created/updated",
			"version": config.ConfigVersion,
		})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleConfigByID handles GET/PUT/DELETE /api/v1/configs/{id}
func (e *Extension) handleConfigByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/configs/")
	if path == "" {
		e.writeError(w, http.StatusBadRequest, "config id is required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		config, err := e.configMgr.GetConfig(r.Context())
		if err != nil {
			e.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		e.writeJSON(w, http.StatusOK, config)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var config controlplanev1.AgentConfig
		if err := json.Unmarshal(body, &config); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if err := e.configMgr.UpdateConfig(r.Context(), &config); err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "config updated",
		})

	case http.MethodDelete:
		// TODO: Implement config deletion
		e.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "config deleted",
		})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTasks handles GET/POST /api/v1/tasks
func (e *Extension) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// List pending tasks
		tasks, err := e.taskMgr.GetGlobalPendingTasks(r.Context())
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		e.writeJSON(w, http.StatusOK, map[string]any{
			"tasks": tasks,
			"total": len(tasks),
		})

	case http.MethodPost:
		// Submit new task
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var task controlplanev1.Task
		if err := json.Unmarshal(body, &task); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if task.TaskID == "" {
			e.writeError(w, http.StatusBadRequest, "task_id is required")
			return
		}

		if err := e.taskMgr.SubmitTask(r.Context(), &task); err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "task submitted",
			"task_id": task.TaskID,
		})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTaskByID handles GET/DELETE /api/v1/tasks/{id}
func (e *Extension) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if path == "" || path == "batch" {
		e.writeError(w, http.StatusBadRequest, "task id is required")
		return
	}
	taskID := path

	switch r.Method {
	case http.MethodGet:
		// Get task status/result
		result, found, err := e.taskMgr.GetTaskResult(r.Context(), taskID)
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			// Try to get task info
			info, err := e.taskMgr.GetTaskStatus(r.Context(), taskID)
			if err != nil {
				e.writeError(w, http.StatusNotFound, "task not found")
				return
			}
			e.writeJSON(w, http.StatusOK, info)
			return
		}
		e.writeJSON(w, http.StatusOK, result)

	case http.MethodDelete:
		// Cancel task
		if err := e.taskMgr.CancelTask(r.Context(), taskID); err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		e.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "task cancelled",
			"task_id": taskID,
		})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTaskBatch handles POST /api/v1/tasks/batch
func (e *Extension) handleTaskBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		e.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Action  string   `json:"action"` // "cancel"
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	switch req.Action {
	case "cancel":
		var cancelled, failed []string
		for _, taskID := range req.TaskIDs {
			if err := e.taskMgr.CancelTask(r.Context(), taskID); err != nil {
				failed = append(failed, taskID)
			} else {
				cancelled = append(cancelled, taskID)
			}
		}
		e.writeJSON(w, http.StatusOK, map[string]any{
			"success":   len(failed) == 0,
			"cancelled": cancelled,
			"failed":    failed,
		})

	default:
		e.writeError(w, http.StatusBadRequest, "invalid action: "+req.Action)
	}
}

// handleAgents handles GET /api/v1/agents
func (e *Extension) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	agents, err := e.agentReg.GetOnlineAgents(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"agents": agents,
		"total":  len(agents),
	})
}

// handleAgentStats handles GET /api/v1/agents/stats
func (e *Extension) handleAgentStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	stats, err := e.agentReg.GetAgentStats(r.Context())
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, stats)
}

// handleAgentByID handles GET/POST /api/v1/agents/{id}
func (e *Extension) handleAgentByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	if path == "" || path == "stats" {
		e.writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	// Check for action suffix
	parts := strings.Split(path, "/")
	agentID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		agent, err := e.agentReg.GetAgent(r.Context(), agentID)
		if err != nil {
			e.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		e.writeJSON(w, http.StatusOK, agent)

	case http.MethodPost:
		if action == "kick" {
			// Force agent offline
			if err := e.agentReg.Unregister(r.Context(), agentID); err != nil {
				e.writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			e.writeJSON(w, http.StatusOK, map[string]any{
				"success":  true,
				"message":  "agent kicked",
				"agent_id": agentID,
			})
			return
		}
		e.writeError(w, http.StatusBadRequest, "invalid action")

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleDashboardOverview handles GET /api/v1/dashboard/overview
func (e *Extension) handleDashboardOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get agent stats
	agentStats, err := e.agentReg.GetAgentStats(r.Context())
	if err != nil {
		agentStats = &agentregistry.AgentStats{}
	}

	// Get pending tasks count
	pendingTasks, err := e.taskMgr.GetGlobalPendingTasks(r.Context())
	pendingCount := 0
	if err == nil {
		pendingCount = len(pendingTasks)
	}

	// Get current config version
	config, _ := e.configMgr.GetConfig(r.Context())
	configVersion := ""
	if config != nil {
		configVersion = config.ConfigVersion
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"agents": map[string]any{
			"total":     agentStats.TotalAgents,
			"online":    agentStats.OnlineAgents,
			"offline":   agentStats.OfflineAgents,
			"unhealthy": agentStats.UnhealthyAgents,
		},
		"tasks": map[string]any{
			"pending": pendingCount,
		},
		"config": map[string]any{
			"version": configVersion,
		},
	})
}

// writeJSON writes a JSON response.
func (e *Extension) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		e.logger.Error("Failed to encode JSON response", zap.Error(err))
	}
}

// writeError writes an error response.
func (e *Extension) writeError(w http.ResponseWriter, status int, message string) {
	e.writeJSON(w, status, map[string]string{"error": message})
}

// ============================================================================
// App/Token Management Handlers
// ============================================================================

// handleApps handles GET/POST /api/v1/apps
func (e *Extension) handleApps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// List all apps
		apps, err := e.tokenMgr.ListApps(r.Context())
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Enrich with agent count
		for _, app := range apps {
			agents, _ := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
			app.AgentCount = len(agents)
		}

		e.writeJSON(w, http.StatusOK, map[string]any{
			"apps":  apps,
			"total": len(apps),
		})

	case http.MethodPost:
		// Create new app
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var req struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Metadata    map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if req.Name == "" {
			e.writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		app, err := e.tokenMgr.CreateApp(r.Context(), &tokenmanager.CreateAppRequest{
			Name:        req.Name,
			Description: req.Description,
			Metadata:    req.Metadata,
		})
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.logger.Info("App created via API",
			zap.String("id", app.ID),
			zap.String("name", app.Name),
		)

		e.writeJSON(w, http.StatusCreated, app)

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAppByID handles GET/PUT/DELETE /api/v1/apps/{id}
func (e *Extension) handleAppByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/apps/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		e.writeError(w, http.StatusBadRequest, "app id is required")
		return
	}

	appID := parts[0]

	// Check for sub-routes
	if len(parts) > 1 {
		switch parts[1] {
		case "token":
			e.handleAppToken(w, r, appID)
			return
		case "config":
			e.handleAppConfig(w, r, appID, parts[2:])
			return
		case "agents":
			e.handleAppAgents(w, r, appID)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		app, err := e.tokenMgr.GetApp(r.Context(), appID)
		if err != nil {
			e.writeError(w, http.StatusNotFound, err.Error())
			return
		}

		// Enrich with agent count
		agents, _ := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
		app.AgentCount = len(agents)

		e.writeJSON(w, http.StatusOK, app)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var req struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Metadata    map[string]string `json:"metadata"`
			Status      string            `json:"status"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		app, err := e.tokenMgr.UpdateApp(r.Context(), appID, &tokenmanager.UpdateAppRequest{
			Name:        req.Name,
			Description: req.Description,
			Metadata:    req.Metadata,
			Status:      req.Status,
		})
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, app)

	case http.MethodDelete:
		if err := e.tokenMgr.DeleteApp(r.Context(), appID); err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.logger.Info("App deleted via API", zap.String("id", appID))
		e.writeJSON(w, http.StatusOK, map[string]string{"message": "app deleted"})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAppToken handles POST /api/v1/apps/{id}/token (regenerate token)
func (e *Extension) handleAppToken(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodPost {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	app, err := e.tokenMgr.RegenerateToken(r.Context(), appID)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.logger.Info("Token regenerated via API", zap.String("app_id", appID))
	e.writeJSON(w, http.StatusOK, app)
}

// handleAppConfig handles config operations for an app
// GET/PUT /api/v1/apps/{id}/config - default config
// GET/PUT/DELETE /api/v1/apps/{id}/config/{agentId} - agent-specific config
func (e *Extension) handleAppConfig(w http.ResponseWriter, r *http.Request, appID string, pathParts []string) {
	// Get app to get token
	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.writeError(w, http.StatusNotFound, "app not found: "+err.Error())
		return
	}

	// Check if on-demand config manager is available
	if e.onDemandConfigMgr == nil {
		e.writeError(w, http.StatusNotImplemented, "on-demand config manager not enabled")
		return
	}

	// Determine if this is default config or agent-specific
	var agentID string
	if len(pathParts) > 0 && pathParts[0] != "" {
		agentID = pathParts[0]
	} else {
		agentID = "_default_"
	}

	switch r.Method {
	case http.MethodGet:
		var config *controlplanev1.AgentConfig
		if agentID == "_default_" {
			config, err = e.onDemandConfigMgr.GetDefaultConfig(r.Context(), app.Token)
		} else {
			config, err = e.onDemandConfigMgr.GetConfigForAgent(r.Context(), app.Token, agentID)
		}
		if err != nil {
			e.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if config == nil {
			e.writeError(w, http.StatusNotFound, "config not found")
			return
		}
		e.writeJSON(w, http.StatusOK, config)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			e.writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		defer r.Body.Close()

		var config controlplanev1.AgentConfig
		if err := json.Unmarshal(body, &config); err != nil {
			e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if agentID == "_default_" {
			err = e.onDemandConfigMgr.SetDefaultConfig(r.Context(), app.Token, &config)
		} else {
			err = e.onDemandConfigMgr.SetConfigForAgent(r.Context(), app.Token, agentID, &config)
		}
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "config updated",
			"agent_id": agentID,
		})

	case http.MethodDelete:
		if agentID == "_default_" {
			e.writeError(w, http.StatusBadRequest, "cannot delete default config")
			return
		}
		err = e.onDemandConfigMgr.DeleteConfigForAgent(r.Context(), app.Token, agentID)
		if err != nil {
			e.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		e.writeJSON(w, http.StatusOK, map[string]string{"message": "config deleted"})

	default:
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAppAgents handles GET /api/v1/apps/{id}/agents
func (e *Extension) handleAppAgents(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodGet {
		e.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get app to get token
	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.writeError(w, http.StatusNotFound, "app not found: "+err.Error())
		return
	}

	agents, err := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"agents": agents,
		"total":  len(agents),
	})
}
