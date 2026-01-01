// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
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

// ============================================================================
// App Config Management
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

func (e *Extension) getAppDefaultConfig(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	config, err := e.onDemandConfigMgr.GetDefaultConfig(r.Context(), app.Token)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}
	if config == nil {
		e.handleError(w, errNotFound("config not found"))
		return
	}

	e.writeJSON(w, http.StatusOK, config)
}

func (e *Extension) setAppDefaultConfig(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	config, err := decodeJSON[controlplanev1.AgentConfig](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if err := e.onDemandConfigMgr.SetDefaultConfig(r.Context(), app.Token, config); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("config updated", map[string]any{"instance_id": "_default_"}))
}

func (e *Extension) getAppInstanceConfig(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	instanceID := chi.URLParam(r, "instanceID")
	config, err := e.onDemandConfigMgr.GetConfigForAgent(r.Context(), app.Token, instanceID)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}
	if config == nil {
		e.handleError(w, errNotFound("config not found"))
		return
	}

	e.writeJSON(w, http.StatusOK, config)
}

func (e *Extension) setAppInstanceConfig(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	instanceID := chi.URLParam(r, "instanceID")
	config, err := decodeJSON[controlplanev1.AgentConfig](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if err := e.onDemandConfigMgr.SetConfigForAgent(r.Context(), app.Token, instanceID, config); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("config updated", map[string]any{"instance_id": instanceID}))
}

func (e *Extension) deleteAppInstanceConfig(w http.ResponseWriter, r *http.Request) {
	app, err := e.getAppWithOnDemandCheck(r)
	if err != nil {
		e.handleError(w, err)
		return
	}

	instanceID := chi.URLParam(r, "instanceID")
	if err := e.onDemandConfigMgr.DeleteConfigForAgent(r.Context(), app.Token, instanceID); err != nil {
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
// Task Management
// ============================================================================

func (e *Extension) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := e.taskMgr.GetGlobalPendingTasks(r.Context())
	if err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, listResponse("tasks", tasks, len(tasks)))
}

func (e *Extension) createTask(w http.ResponseWriter, r *http.Request) {
	task, err := decodeJSON[controlplanev1.Task](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if task.TaskID == "" {
		e.handleError(w, errBadRequest("task_id is required"))
		return
	}

	if err := e.taskMgr.SubmitTask(r.Context(), task); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("task submitted", map[string]any{"task_id": task.TaskID}))
}

func (e *Extension) getTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	result, found, err := e.taskMgr.GetTaskResult(r.Context(), taskID)
	if err != nil {
		e.handleError(w, err)
		return
	}

	if !found {
		info, err := e.taskMgr.GetTaskStatus(r.Context(), taskID)
		if err != nil {
			e.handleError(w, errNotFound("task not found"))
			return
		}
		e.writeJSON(w, http.StatusOK, info)
		return
	}

	e.writeJSON(w, http.StatusOK, result)
}

func (e *Extension) cancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	if err := e.taskMgr.CancelTask(r.Context(), taskID); err != nil {
		e.handleError(w, err)
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("task cancelled", map[string]any{"task_id": taskID}))
}

func (e *Extension) batchTaskAction(w http.ResponseWriter, r *http.Request) {
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
