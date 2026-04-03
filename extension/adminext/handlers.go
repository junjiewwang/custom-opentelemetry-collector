// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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

	type appWithStats struct {
		ID           string            `json:"id"`
		Name         string            `json:"name"`
		Description  string            `json:"description,omitempty"`
		Token        string            `json:"token"`
		Status       string            `json:"status,omitempty"`
		CreatedAt    time.Time         `json:"created_at"`
		UpdatedAt    time.Time         `json:"updated_at"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		AgentCount   int               `json:"agent_count"`
		ServiceCount int               `json:"service_count"`
	}

	result := make([]appWithStats, 0, len(apps))
	for _, app := range apps {
		instances, _ := e.agentReg.GetAgentsByToken(r.Context(), app.Token)
		services, _ := e.agentReg.GetServicesByApp(r.Context(), app.ID)
		result = append(result, appWithStats{
			ID:           app.ID,
			Name:         app.Name,
			Description:  app.Description,
			Token:        app.Token,
			Status:       app.Status,
			CreatedAt:    app.CreatedAt,
			UpdatedAt:    app.UpdatedAt,
			Metadata:     app.Metadata,
			AgentCount:   len(instances),
			ServiceCount: len(services),
		})
	}

	e.writeJSON(w, http.StatusOK, listResponse("apps", result, len(result)))
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

	serviceNames, err := e.agentReg.GetServicesByApp(r.Context(), app.ID)
	if err != nil {
		e.handleError(w, err)
		return
	}

	type serviceWithStats struct {
		ServiceName   string `json:"service_name"`
		InstanceCount int    `json:"instance_count"`
	}

	services := make([]serviceWithStats, 0, len(serviceNames))
	for _, svcName := range serviceNames {
		instances, _ := e.agentReg.GetInstancesByService(r.Context(), app.ID, svcName)
		services = append(services, serviceWithStats{
			ServiceName:   svcName,
			InstanceCount: len(instances),
		})
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
	serviceName := r.URL.Query().Get("service_name")
	sortBy := r.URL.Query().Get("sort_by")
	sortOrder := r.URL.Query().Get("sort_order")

	// Parse and validate sort parameters (whitelist validation)
	sortOpts, err := agentregistry.ParseSortOptions(sortBy, sortOrder)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	var instances []*agentregistry.AgentInfo

	if appID != "" && serviceName != "" {
		// Filter by specific app + service (most specific)
		app, err := e.tokenMgr.GetApp(r.Context(), appID)
		if err != nil {
			e.handleError(w, errNotFound("app not found: "+err.Error()))
			return
		}
		instances, err = e.agentReg.GetInstancesByService(r.Context(), app.ID, serviceName)
		if err != nil {
			e.handleError(w, err)
			return
		}
	} else if appID != "" {
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
	} else {
		// Fetch base set based on status parameter
		switch status {
		case "all":
			instances, err = e.agentReg.GetAllAgents(r.Context())
		case "online", "":
			instances, err = e.agentReg.GetOnlineAgents(r.Context())
		case "offline":
			instances, err = e.agentReg.GetAllAgents(r.Context())
		default:
			e.handleError(w, errBadRequest("invalid status filter: "+status+", valid values: all, online, offline"))
			return
		}
		if err != nil {
			e.handleError(w, err)
			return
		}
	}

	// Apply status filter when needed (for appID queries or "offline" filter)
	if status == "online" || status == "offline" {
		filtered := make([]*agentregistry.AgentInfo, 0, len(instances))
		for _, inst := range instances {
			state := ""
			if inst.Status != nil {
				state = string(inst.Status.State)
			}
			if state == status {
				filtered = append(filtered, inst)
			}
		}
		instances = filtered
	}

	// Sort instances after filtering
	agentregistry.SortAgents(instances, sortOpts)

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
	AppName         string            `json:"app_name,omitempty"`
	ServiceName     string            `json:"service_name,omitempty"`
	AgentState      string            `json:"agent_state,omitempty"`
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
	query, err := parseListTasksQuery(r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	page, err := e.taskMgr.ListTasks(r.Context(), query)
	if err != nil {
		e.handleError(w, err)
		return
	}

	// Build appID→appName map for display enrichment
	appNameMap := make(map[string]string)
	if apps, err := e.tokenMgr.ListApps(r.Context()); err == nil {
		for _, app := range apps {
			appNameMap[app.ID] = app.Name
		}
	}

	// Build agentID→metadata map to backfill offline agent info and agent state.
	// Optimization: when filter conditions are present, only look up agents that
	// appear in the result set instead of loading ALL agents.
	type agentMeta struct {
		AppID       string
		ServiceName string
		State       string
	}
	agentMetaMap := make(map[string]agentMeta)

	hasFilter := query.AppID != "" || query.ServiceName != "" || query.AgentID != "" || query.TaskType != ""
	if hasFilter {
		// Targeted lookup: only resolve agents referenced in the result page
		for _, t := range page.Items {
			agentID := t.AgentID
			if agentID == "" || agentMetaMap[agentID] != (agentMeta{}) {
				continue
			}
			if agent, err := e.agentReg.GetAgent(r.Context(), agentID); err == nil && agent != nil {
				state := ""
				if agent.Status != nil {
					state = string(agent.Status.State)
				}
				agentMetaMap[agentID] = agentMeta{
					AppID:       agent.AppID,
					ServiceName: agent.ServiceName,
					State:       state,
				}
			}
		}
	} else {
		// No filter: load all agents (original behavior)
		if agents, err := e.agentReg.GetAllAgents(r.Context()); err == nil {
			for _, agent := range agents {
				state := ""
				if agent.Status != nil {
					state = string(agent.Status.State)
				}
				agentMetaMap[agent.AgentID] = agentMeta{
					AppID:       agent.AppID,
					ServiceName: agent.ServiceName,
					State:       state,
				}
			}
		}
	}

	out := make([]*taskInfoV2, 0, len(page.Items))
	for _, t := range page.Items {
		info := toTaskInfoV2(t)
		if info == nil {
			continue
		}

		// Backfill missing app_id/service_name and enrich agent_state from agent registry
		if info.AgentID != "" {
			if meta, ok := agentMetaMap[info.AgentID]; ok {
				if info.AppID == "" {
					info.AppID = meta.AppID
				}
				if info.ServiceName == "" {
					info.ServiceName = meta.ServiceName
				}
				info.AgentState = meta.State
			}
		}

		// Enrich app_name
		if info.AppID != "" {
			info.AppName = appNameMap[info.AppID]
		}

		out = append(out, info)
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"tasks":       out,
		"total":       len(out),
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
	})
}

func parseListTasksQuery(r *http.Request) (taskmanager.ListTasksQuery, error) {
	query := taskmanager.ListTasksQuery{
		AppID:       strings.TrimSpace(r.URL.Query().Get("app_id")),
		ServiceName: strings.TrimSpace(r.URL.Query().Get("service_name")),
		AgentID:     strings.TrimSpace(r.URL.Query().Get("agent_id")),
		TaskType:    strings.TrimSpace(r.URL.Query().Get("task_type")),
		Cursor:      strings.TrimSpace(r.URL.Query().Get("cursor")),
	}

	if limitStr := strings.TrimSpace(r.URL.Query().Get("limit")); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 0 {
			return taskmanager.ListTasksQuery{}, errors.New("invalid limit")
		}
		query.Limit = limit
	}

	statusValues := r.URL.Query()["status"]
	if len(statusValues) == 0 {
		if statusCSV := strings.TrimSpace(r.URL.Query().Get("statuses")); statusCSV != "" {
			statusValues = strings.Split(statusCSV, ",")
		}
	}

	for _, raw := range statusValues {
		status, ok, err := parseTaskStatus(raw)
		if err != nil {
			return taskmanager.ListTasksQuery{}, err
		}
		if ok {
			query.Statuses = append(query.Statuses, status)
		}
	}

	return query, nil
}

func parseTaskStatus(raw string) (model.TaskStatus, bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return 0, false, nil
	case "unknown":
		return model.TaskStatusUnspecified, true, nil

	case "pending":
		return model.TaskStatusPending, true, nil
	case "running":
		return model.TaskStatusRunning, true, nil
	case "success":
		return model.TaskStatusSuccess, true, nil
	case "failed":
		return model.TaskStatusFailed, true, nil
	case "timeout":
		return model.TaskStatusTimeout, true, nil
	case "cancelled":
		return model.TaskStatusCancelled, true, nil
	case "result_too_large":
		return model.TaskStatusResultTooLarge, true, nil
	default:
		return 0, false, errors.New("invalid status filter: " + raw)
	}
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
		agent, err := e.agentReg.GetAgent(r.Context(), task.TargetAgentID)
		if err != nil || agent == nil {
			e.handleError(w, errNotFound("agent not found: "+task.TargetAgentID))
			return
		}

		// Reject task submission if agent is not online
		if agent.Status == nil || agent.Status.State != agentregistry.AgentStateOnline {
			e.handleError(w, errBadRequest("agent is not online, cannot submit task"))
			return
		}

		agentMeta := &taskmanager.AgentMeta{
			AgentID:     agent.AgentID,
			AppID:       agent.AppID,
			ServiceName: agent.ServiceName,
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