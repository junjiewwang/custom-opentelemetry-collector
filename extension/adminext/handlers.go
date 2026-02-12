// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

// ============================================================================
// Health Check
// ============================================================================

func (e *Extension) handleHealth(w http.ResponseWriter, _ *http.Request) {
	e.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ============================================================================
// App Management
// ============================================================================

func (e *Extension) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := e.tokenMgr.ListApps(r.Context())
	if err != nil {
		e.handleError(w, err)
		return
	}

	// Enrich with instance count
	for _, app := range apps {
		instances, _ := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
		app.AgentCount = len(instances)
	}

	e.writeJSON(w, http.StatusOK, listResponse("apps", apps, len(apps)))
}

func (e *Extension) createApp(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Metadata    map[string]string `json:"metadata"`
	}](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if req.Name == "" {
		e.handleError(w, errBadRequest("name is required"))
		return
	}

	app, err := e.tokenMgr.CreateApp(r.Context(), &tokenmanager.CreateAppRequest{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
	})
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.logger.Info("App created via API", zap.String("id", app.ID), zap.String("name", app.Name))
	e.writeJSON(w, http.StatusCreated, app)
}

func (e *Extension) getApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}

	// Enrich with instance count
	instances, _ := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
	app.AgentCount = len(instances)

	e.writeJSON(w, http.StatusOK, app)
}

func (e *Extension) updateApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	req, err := decodeJSON[struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Metadata    map[string]string `json:"metadata"`
		Status      string            `json:"status"`
	}](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	app, err := e.tokenMgr.UpdateApp(r.Context(), appID, &tokenmanager.UpdateAppRequest{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Status:      req.Status,
	})
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, app)
}

func (e *Extension) deleteApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	if err := e.tokenMgr.DeleteApp(r.Context(), appID); err != nil {
		e.handleError(w, err)
		return
	}

	e.logger.Info("App deleted via API", zap.String("id", appID))
	e.writeJSON(w, http.StatusOK, map[string]string{"message": "app deleted"})
}

func (e *Extension) regenerateAppToken(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := e.tokenMgr.RegenerateToken(r.Context(), appID)
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.logger.Info("Token regenerated via API", zap.String("app_id", appID))
	e.writeJSON(w, http.StatusOK, app)
}

func (e *Extension) setAppToken(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	req, err := decodeJSON[tokenmanager.SetTokenRequest](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	app, err := e.tokenMgr.SetToken(r.Context(), appID, req)
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.logger.Info("Token set via API", zap.String("app_id", appID))
	e.writeJSON(w, http.StatusOK, app)
}

// ============================================================================
// Config Management (Simplified: Service-level only)
// ============================================================================

func (e *Extension) getAppWithOnDemandCheck(r *http.Request) (*tokenmanager.AppInfo, error) {
	if e.onDemandConfigMgr == nil {
		return nil, errNotImplemented("on-demand config manager not enabled")
	}

	appID := chi.URLParam(r, "appID")
	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		return nil, errNotFound("app not found: " + err.Error())
	}
	return app, nil
}

func (e *Extension) getAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")

	cfg, err := e.onDemandConfigMgr.GetServiceConfig(r.Context(), app.ID, serviceName)
	if err != nil {
		// "config not found" is a normal condition for first-time setup.
		// Return a template + reference so the UI can guide users to publish one.
		if errors.Is(err, configmanager.ErrConfigNotFound) {
			cfg = nil
		} else {
			e.handleError(w, err)
			return
		}
	}

	if cfg == nil {
		cfg = &model.AgentConfig{
			Version: "0", // Use "0" to indicate it's a skeleton/template
		}
	}

	// Always provide a reference template for the UI to guide users
	// and detect missing fields in older configurations.
	reference := &model.AgentConfig{
		Sampler: &model.SamplerConfig{
			Type:  3, // TraceIDRatio
			Ratio: 0.1,
		},
		Batch: &model.BatchConfig{
			MaxExportBatchSize:  512,
			MaxQueueSize:        2048,
			ScheduleDelayMillis: 5000,
			ExportTimeoutMillis: 30000,
		},
		DynamicResourceAttributes: map[string]string{
			"service.version": "1.0.0",
			"deployment.env":  "production",
		},
		ExtensionConfigJSON: `{"example_key": "example_value"}`,
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"config":    cfg,
		"reference": reference,
	})
}

func (e *Extension) setAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")
	cfg, err := decodeJSON[model.AgentConfig](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if err := e.onDemandConfigMgr.SetServiceConfig(r.Context(), app.ID, serviceName, cfg); err != nil {
		e.handleError(w, err)
		return
	}
	e.writeJSON(w, http.StatusOK, successResponse("config updated", map[string]any{"service_name": serviceName}))
}

func (e *Extension) deleteAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")

	if err := e.onDemandConfigMgr.DeleteServiceConfig(r.Context(), app.ID, serviceName); err != nil {
		e.handleError(w, err)
		return
	}
	e.writeJSON(w, http.StatusOK, map[string]string{"message": "config deleted"})
}

// ============================================================================
// App Services & Instances
// ============================================================================

func (e *Extension) listAppServices(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	services, err := e.agentReg.GetServicesByApp(r.Context(), app.ID)
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":   appID,
		"services": services,
		"total":    len(services),
	})
}

func (e *Extension) listServiceInstances(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	serviceName := chi.URLParam(r, "serviceName")

	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	instances, err := e.agentReg.GetInstancesByService(r.Context(), app.ID, serviceName)
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":       appID,
		"service_name": serviceName,
		"instances":    instances,
		"total":        len(instances),
	})
}

func (e *Extension) listAppInstances(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := e.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		e.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	instances, err := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":    appID,
		"instances": instances,
		"total":     len(instances),
	})
}

func (e *Extension) getAppInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	instance, err := e.agentReg.GetAgent(r.Context(), instanceID)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, instance)
}

func (e *Extension) kickAppInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	if err := e.agentReg.Unregister(r.Context(), instanceID); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instance kicked", map[string]any{"instance_id": instanceID}))
}

// ============================================================================
// Global Service View
// ============================================================================

func (e *Extension) listAllServices(w http.ResponseWriter, r *http.Request) {
	apps, err := e.tokenMgr.ListApps(r.Context())
	if err != nil {
		e.handleError(w, err)
		return
	}

	type ServiceInfo struct {
		AppID         string `json:"app_id"`
		AppName       string `json:"app_name"`
		ServiceName   string `json:"service_name"`
		InstanceCount int    `json:"instance_count"`
	}

	var services []ServiceInfo
	for _, app := range apps {
		serviceNames, err := e.agentReg.GetServicesByApp(r.Context(), app.ID)
		if err != nil {
			continue
		}

		for _, svcName := range serviceNames {
			instances, _ := e.agentReg.GetInstancesByService(r.Context(), app.ID, svcName)
			services = append(services, ServiceInfo{
				AppID:         app.ID,
				AppName:       app.Name,
				ServiceName:   svcName,
				InstanceCount: len(instances),
			})
		}
	}

	e.writeJSON(w, http.StatusOK, listResponse("services", services, len(services)))
}

// ============================================================================
// Global Instance View
// ============================================================================

func (e *Extension) listAllInstances(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	appID := r.URL.Query().Get("app_id")

	var instances []*agentregistry.AgentInfo
	var err error

	if appID != "" {
		// Filter by specific app
		app, err := e.tokenMgr.GetApp(r.Context(), appID)
		if err != nil {
			e.handleError(w, errNotFound("app not found: "+err.Error()))
			return
		}
		instances, err = e.agentReg.GetAgentsByToken(r.Context(), app.Token)
		if err != nil {
			e.handleError(w, err)
			return
		}
	} else if status == "all" {
		// Get all instances (including offline)
		instances, err = e.agentReg.GetAllAgents(r.Context())
		if err != nil {
			e.handleError(w, err)
			return
		}
	} else {
		// Default: online only
		instances, err = e.agentReg.GetOnlineAgents(r.Context())
		if err != nil {
			e.handleError(w, err)
			return
		}
	}

	e.writeJSON(w, http.StatusOK, listResponse("instances", instances, len(instances)))
}

func (e *Extension) getInstanceStats(w http.ResponseWriter, r *http.Request) {
	stats, err := e.agentReg.GetAgentStats(r.Context())
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, stats)
}

func (e *Extension) getInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	instance, err := e.agentReg.GetAgent(r.Context(), instanceID)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, instance)
}

func (e *Extension) kickInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	if err := e.agentReg.Unregister(r.Context(), instanceID); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instance kicked", map[string]any{"instance_id": instanceID}))
}

// ============================================================================
// Task Management (v2-only, model JSON)
// ============================================================================

type taskInfoV2 struct {
	Task            *model.Task       `json:"task"`
	Status          model.TaskStatus  `json:"status"`
	AgentID         string            `json:"agent_id,omitempty"`
	AppID           string            `json:"app_id,omitempty"`
	ServiceName     string            `json:"service_name,omitempty"`
	CreatedAtMillis int64             `json:"created_at_millis"`
	StartedAtMillis int64             `json:"started_at_millis,omitempty"`
	Result          *model.TaskResult `json:"result,omitempty"`
}

func toTaskInfoV2(info *taskmanager.TaskInfo) *taskInfoV2 {
	if info == nil {
		return nil
	}
	return &taskInfoV2{
		Task:            info.Task,
		Status:          info.Status,
		AgentID:         info.AgentID,
		AppID:           info.AppID,
		ServiceName:     info.ServiceName,
		CreatedAtMillis: info.CreatedAtMillis,
		StartedAtMillis: info.StartedAtMillis,
		Result:          info.Result,
	}
}

func (e *Extension) listTasksV2(w http.ResponseWriter, r *http.Request) {
	tasks, err := e.taskMgr.GetAllTasks(r.Context())
	if err != nil {
		e.handleError(w, err)
		return
	}

	out := make([]*taskInfoV2, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskInfoV2(t))
	}

	e.writeJSON(w, http.StatusOK, listResponse("tasks", out, len(out)))
}

func (e *Extension) createTaskV2(w http.ResponseWriter, r *http.Request) {
	task, err := decodeJSON[model.Task](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}
	if task.TypeName == "" {
		e.handleError(w, errBadRequest("task_type_name is required"))
		return
	}
	if task.ID == "" {
		task.ID = uuid.New().String()
	}

	if len(task.ParametersJSON) > 0 {
		var m map[string]any
		if err := json.Unmarshal(task.ParametersJSON, &m); err != nil {
			e.handleError(w, errBadRequest("parameters_json must be a JSON object"))
			return
		}
		if m == nil {
			e.handleError(w, errBadRequest("parameters_json must be a JSON object"))
			return
		}
	}

	if task.TargetAgentID != "" {
		var agentMeta *taskmanager.AgentMeta
		if agent, err := e.agentReg.GetAgent(r.Context(), task.TargetAgentID); err == nil && agent != nil {
			agentMeta = &taskmanager.AgentMeta{
				AgentID:     agent.AgentID,
				AppID:       agent.AppID,
				ServiceName: agent.ServiceName,
			}
		} else {
			agentMeta = &taskmanager.AgentMeta{AgentID: task.TargetAgentID}
		}

		if err := e.taskMgr.SubmitTaskForAgent(r.Context(), agentMeta, task); err != nil {
			e.handleError(w, err)
			return
		}
	} else {
		if err := e.taskMgr.SubmitTask(r.Context(), task); err != nil {
			e.handleError(w, err)
			return
		}
	}

	e.writeJSON(w, http.StatusOK, successResponse("task submitted", map[string]any{"task_id": task.ID}))
}

func (e *Extension) getTaskV2(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	info, err := e.taskMgr.GetTaskStatus(r.Context(), taskID)
	if err == nil && info != nil {
		// Always fetch the latest result from the result store.
		// Callbacks (e.g., analysis_callback) may update the result independently
		// after the task has reached a terminal state, so info.Result can be stale.
		result, found, err := e.taskMgr.GetTaskResult(r.Context(), taskID)
		if err != nil {
			e.handleError(w, err)
			return
		}
		if found {
			info.Result = result
		}

		e.writeJSON(w, http.StatusOK, toTaskInfoV2(info))
		return
	}

	result, found, err := e.taskMgr.GetTaskResult(r.Context(), taskID)
	if err != nil {
		e.handleError(w, err)
		return
	}
	if found {
		e.writeJSON(w, http.StatusOK, result)
		return
	}

	e.handleError(w, errNotFound("task not found"))
}

func (e *Extension) cancelTaskV2(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	if err := e.taskMgr.CancelTask(r.Context(), taskID); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("task cancelled", map[string]any{"task_id": taskID}))
}

func (e *Extension) batchTaskActionV2(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Action  string   `json:"action"`
		TaskIDs []string `json:"task_ids"`
	}](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
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
		e.handleError(w, errBadRequest("invalid action: "+req.Action))
	}
}

// ============================================================================
// Dashboard
// ============================================================================

func (e *Extension) getDashboardOverview(w http.ResponseWriter, r *http.Request) {
	// Get instance stats
	instanceStats, err := e.agentReg.GetAgentStats(r.Context())
	if err != nil {
		instanceStats = &agentregistry.AgentStats{}
	}

	// Get pending tasks count
	pendingTasks, err := e.taskMgr.GetGlobalPendingTasks(r.Context())
	pendingCount := 0
	if err == nil {
		pendingCount = len(pendingTasks)
	}

	// Get app count
	apps, _ := e.tokenMgr.ListApps(r.Context())

	e.writeJSON(w, http.StatusOK, map[string]any{
		"apps": map[string]any{
			"total": len(apps),
		},
		"instances": map[string]any{
			"total":     instanceStats.TotalAgents,
			"online":    instanceStats.OnlineAgents,
			"offline":   instanceStats.OfflineAgents,
			"unhealthy": instanceStats.UnhealthyAgents,
		},
		"tasks": map[string]any{
			"pending": pendingCount,
		},
	})
}
