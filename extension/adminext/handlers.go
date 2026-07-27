// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
)

// instanceResponse wraps AgentInfo with Arthas tunnel status.
type instanceResponse struct {
	*agentregistry.AgentInfo
	TunnelAgentID string `json:"arthas_tunnel_agent_id,omitempty"`
}

// withTunnel enriches AgentInfo slice with tunnel connection status.
func (h *adminHandlers) withTunnel(instances []*agentregistry.AgentInfo) []instanceResponse {
	tunnelIDs := h.getTunnelAgentIDs()
	result := make([]instanceResponse, len(instances))
	for i, inst := range instances {
		ir := instanceResponse{AgentInfo: inst}
		if tunnelIDs != nil {
			if _, ok := tunnelIDs[inst.AgentID]; ok {
				ir.TunnelAgentID = inst.AgentID
			}
		}
		result[i] = ir
	}
	return result
}

// ============================================================================
// Health Check
// ============================================================================

func (h *adminHandlers) handleHealth(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ============================================================================
// App Management
// ============================================================================

func (h *adminHandlers) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.tokenMgr.ListApps(r.Context())
	if err != nil {
		h.handleError(w, err)
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
		instances, _ := h.agentReg.GetAgentsByToken(r.Context(), app.Token)
		services, _ := h.agentReg.GetServicesByApp(r.Context(), app.ID)
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

	h.writeJSON(w, http.StatusOK, listResponse("apps", result, len(result)))
}

func (h *adminHandlers) createApp(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Metadata    map[string]string `json:"metadata"`
	}](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	if req.Name == "" {
		h.handleError(w, errBadRequest("name is required"))
		return
	}

	app, err := h.tokenMgr.CreateApp(r.Context(), &appmanager.CreateAppRequest{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.logger.Info("App created via API", zap.String("id", app.ID), zap.String("name", app.Name))
	h.writeJSON(w, http.StatusCreated, app)
}

func (h *adminHandlers) getApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := h.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		h.handleError(w, errNotFound(err.Error()))
		return
	}

	// Enrich with instance count
	instances, _ := h.agentReg.GetAgentsByToken(r.Context(), app.Token)
	app.AgentCount = len(instances)

	h.writeJSON(w, http.StatusOK, app)
}

func (h *adminHandlers) updateApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	req, err := decodeJSON[struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Metadata    map[string]string `json:"metadata"`
		Status      string            `json:"status"`
	}](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	app, err := h.tokenMgr.UpdateApp(r.Context(), appID, &appmanager.UpdateAppRequest{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Status:      req.Status,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, app)
}

func (h *adminHandlers) deleteApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	if err := h.tokenMgr.DeleteApp(r.Context(), appID); err != nil {
		h.handleError(w, err)
		return
	}

	h.logger.Info("App deleted via API", zap.String("id", appID))
	h.writeJSON(w, http.StatusOK, map[string]string{"message": "app deleted"})
}

func (h *adminHandlers) regenerateAppToken(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := h.tokenMgr.RegenerateToken(r.Context(), appID)
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.logger.Info("Token regenerated via API", zap.String("app_id", appID))
	h.writeJSON(w, http.StatusOK, app)
}

func (h *adminHandlers) setAppToken(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	req, err := decodeJSON[appmanager.SetTokenRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	app, err := h.tokenMgr.SetToken(r.Context(), appID, req)
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.logger.Info("Token set via API", zap.String("app_id", appID))
	h.writeJSON(w, http.StatusOK, app)
}

// ============================================================================
// Config Management (Simplified: Service-level only)
// ============================================================================

func (h *adminHandlers) getAppWithOnDemandCheck(r *http.Request) (*appmanager.AppInfo, error) {
	if h.onDemandConfigMgr == nil {
		return nil, errNotImplemented("on-demand config manager not enabled")
	}

	appID := chi.URLParam(r, "appID")
	app, err := h.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		return nil, errNotFound("app not found: " + err.Error())
	}
	return app, nil
}

func (h *adminHandlers) getAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := h.getAppWithOnDemandCheck(r)
	if err != nil {
		h.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")

	cfg, err := h.onDemandConfigMgr.GetServiceConfig(r.Context(), app.ID, serviceName)
	if err != nil {
		// "config not found" is a normal condition for first-time setup.
		// Return a template + reference so the UI can guide users to publish one.
		if errors.Is(err, configmanager.ErrConfigNotFound) {
			cfg = nil
		} else {
			h.handleError(w, err)
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

	h.writeJSON(w, http.StatusOK, map[string]any{
		"config":    cfg,
		"reference": reference,
	})
}

func (h *adminHandlers) setAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := h.getAppWithOnDemandCheck(r)
	if err != nil {
		h.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")
	cfg, err := decodeJSON[model.AgentConfig](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	if err := h.onDemandConfigMgr.SetServiceConfig(r.Context(), app.ID, serviceName, cfg); err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, successResponse("config updated", map[string]any{"service_name": serviceName}))
}

func (h *adminHandlers) deleteAppServiceConfigV2(w http.ResponseWriter, r *http.Request) {
	app, err := h.getAppWithOnDemandCheck(r)
	if err != nil {
		h.handleError(w, err)
		return
	}

	serviceName := chi.URLParam(r, "serviceName")

	if err := h.onDemandConfigMgr.DeleteServiceConfig(r.Context(), app.ID, serviceName); err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"message": "config deleted"})
}

// ============================================================================
// App Services & Instances
// ============================================================================

func (h *adminHandlers) listAppServices(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	// Validate app exists
	if _, err := h.tokenMgr.GetApp(r.Context(), appID); err != nil {
		h.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	// Query from ServiceManager
	query := servicemanager.ListServicesQuery{
		NamePattern:    r.URL.Query().Get("name"),
		IncludeRuntime: true,
	}
	services, err := h.serviceMgr.ListServicesByApp(r.Context(), appID, query)
	if err != nil {
		h.handleError(w, err)
		return
	}

	// Enrich with runtime stats from AgentRegistry
	h.enrichServicesRuntime(r, services)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":   appID,
		"services": services,
		"total":    len(services),
	})
}

func (h *adminHandlers) getService(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	serviceName := chi.URLParam(r, "serviceName")

	svc, err := h.serviceMgr.GetService(r.Context(), appID, serviceName)
	if err != nil {
		h.handleError(w, errNotFound("service not found: "+err.Error()))
		return
	}

	// Enrich with runtime stats
	h.enrichServicesRuntime(r, []*servicemanager.ServiceInfo{svc})

	h.writeJSON(w, http.StatusOK, svc)
}

func (h *adminHandlers) updateServiceMetadata(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	serviceName := chi.URLParam(r, "serviceName")

	req, err := decodeJSON[servicemanager.UpdateServiceRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	svc, err := h.serviceMgr.UpdateServiceMetadata(r.Context(), appID, serviceName, req)
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, svc)
}

func (h *adminHandlers) deleteService(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	serviceName := chi.URLParam(r, "serviceName")

	// Precondition: instance_count == 0
	instances, err := h.agentReg.GetInstancesByService(r.Context(), appID, serviceName)
	if err != nil {
		h.handleError(w, errInternal("failed to check instance count: "+err.Error()))
		return
	}
	if len(instances) > 0 {
		h.handleError(w, errConflict(
			fmt.Sprintf("cannot delete service with %d active instance(s); remove all instances first", len(instances)),
		))
		return
	}

	if err := h.serviceMgr.DeleteService(r.Context(), appID, serviceName); err != nil {
		h.handleError(w, err)
		return
	}

	h.logger.Info("Service deleted via API",
		zap.String("app_id", appID),
		zap.String("service_name", serviceName),
	)
	h.writeJSON(w, http.StatusOK, successResponse("service deleted", map[string]any{
		"app_id":       appID,
		"service_name": serviceName,
	}))
}

// enrichServicesRuntime populates runtime aggregated fields (InstanceCount, OnlineCount)
// on the given ServiceInfo slice by querying AgentRegistry.
func (h *adminHandlers) enrichServicesRuntime(r *http.Request, services []*servicemanager.ServiceInfo) {
	for _, svc := range services {
		instances, err := h.agentReg.GetInstancesByService(r.Context(), svc.AppID, svc.ServiceName)
		if err != nil {
			continue
		}
		svc.InstanceCount = len(instances)
		online := 0
		for _, inst := range instances {
			if inst.Status != nil && inst.Status.State == agentregistry.AgentStateOnline {
				online++
			}
		}
		svc.OnlineCount = online
	}
}

func (h *adminHandlers) listServiceInstances(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	serviceName := chi.URLParam(r, "serviceName")

	app, err := h.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		h.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	instances, err := h.agentReg.GetInstancesByService(r.Context(), app.ID, serviceName)
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":       appID,
		"service_name": serviceName,
		"instances":    instances,
		"total":        len(instances),
	})
}

func (h *adminHandlers) listAppInstances(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")

	app, err := h.tokenMgr.GetApp(r.Context(), appID)
	if err != nil {
		h.handleError(w, errNotFound("app not found: "+err.Error()))
		return
	}

	instances, err := h.agentReg.GetAgentsByToken(r.Context(), app.Token)
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"app_id":    appID,
		"instances": h.withTunnel(instances),
		"total":     len(instances),
	})
}

func (h *adminHandlers) getAppInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	instance, err := h.agentReg.GetAgent(r.Context(), instanceID)
	if err != nil {
		h.handleError(w, errNotFound(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, instance)
}

func (h *adminHandlers) kickAppInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	if err := h.agentReg.Unregister(r.Context(), instanceID); err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instance kicked", map[string]any{"instance_id": instanceID}))
}

// ============================================================================
// Global Service View
// ============================================================================

func (h *adminHandlers) listAllServices(w http.ResponseWriter, r *http.Request) {
	query := servicemanager.ListServicesQuery{
		NamePattern:    r.URL.Query().Get("name"),
		IncludeRuntime: true,
	}

	services, err := h.serviceMgr.ListAllServices(r.Context(), query)
	if err != nil {
		h.handleError(w, err)
		return
	}

	// Enrich with runtime stats from AgentRegistry
	h.enrichServicesRuntime(r, services)

	// Build appID→appName map for display enrichment
	appNameMap := make(map[string]string)
	if apps, err := h.tokenMgr.ListApps(r.Context()); err == nil {
		for _, app := range apps {
			appNameMap[app.ID] = app.Name
		}
	}

	// Build enriched response with app_name
	type serviceWithAppName struct {
		*servicemanager.ServiceInfo
		AppName string `json:"app_name"`
	}

	result := make([]serviceWithAppName, 0, len(services))
	for _, svc := range services {
		result = append(result, serviceWithAppName{
			ServiceInfo: svc,
			AppName:     appNameMap[svc.AppID],
		})
	}

	h.writeJSON(w, http.StatusOK, listResponse("services", result, len(result)))
}

// ============================================================================
// Global Instance View
// ============================================================================

func (h *adminHandlers) listAllInstances(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	appID := r.URL.Query().Get("app_id")
	serviceName := r.URL.Query().Get("service_name")
	sortBy := r.URL.Query().Get("sort_by")
	sortOrder := r.URL.Query().Get("sort_order")

	// Parse and validate sort parameters (whitelist validation)
	sortOpts, err := agentregistry.ParseSortOptions(sortBy, sortOrder)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	var instances []*agentregistry.AgentInfo

	if appID != "" && serviceName != "" {
		// Filter by specific app + service (most specific)
		app, err := h.tokenMgr.GetApp(r.Context(), appID)
		if err != nil {
			h.handleError(w, errNotFound("app not found: "+err.Error()))
			return
		}
		instances, err = h.agentReg.GetInstancesByService(r.Context(), app.ID, serviceName)
		if err != nil {
			h.handleError(w, err)
			return
		}
	} else if appID != "" {
		// Filter by specific app
		app, err := h.tokenMgr.GetApp(r.Context(), appID)
		if err != nil {
			h.handleError(w, errNotFound("app not found: "+err.Error()))
			return
		}
		instances, err = h.agentReg.GetAgentsByToken(r.Context(), app.Token)
		if err != nil {
			h.handleError(w, err)
			return
		}
	} else {
		// Fetch base set based on status parameter
		switch status {
		case "all":
			instances, err = h.agentReg.GetAllAgents(r.Context())
		case "online", "":
			instances, err = h.agentReg.GetOnlineAgents(r.Context())
		case "offline":
			instances, err = h.agentReg.GetAllAgents(r.Context())
		default:
			h.handleError(w, errBadRequest("invalid status filter: "+status+", valid values: all, online, offline"))
			return
		}
		if err != nil {
			h.handleError(w, err)
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

	h.writeJSON(w, http.StatusOK, listResponse("instances", h.withTunnel(instances), len(instances)))
}

func (h *adminHandlers) getInstanceStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.agentReg.GetAgentStats(r.Context())
	if err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, stats)
}

func (h *adminHandlers) getInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	instance, err := h.agentReg.GetAgent(r.Context(), instanceID)
	if err != nil {
		h.handleError(w, errNotFound(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, instance)
}

func (h *adminHandlers) kickInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instanceID")

	if err := h.agentReg.Unregister(r.Context(), instanceID); err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instance kicked", map[string]any{"instance_id": instanceID}))
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

func (h *adminHandlers) listTasksV2(w http.ResponseWriter, r *http.Request) {
	query, err := parseListTasksQuery(r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	page, err := h.taskMgr.ListTasks(r.Context(), query)
	if err != nil {
		h.handleError(w, err)
		return
	}

	// Build appID→appName map for display enrichment
	appNameMap := make(map[string]string)
	if apps, err := h.tokenMgr.ListApps(r.Context()); err == nil {
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
			if agent, err := h.agentReg.GetAgent(r.Context(), agentID); err == nil && agent != nil {
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
		if agents, err := h.agentReg.GetAllAgents(r.Context()); err == nil {
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

	h.writeJSON(w, http.StatusOK, map[string]any{
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

func (h *adminHandlers) createTaskV2(w http.ResponseWriter, r *http.Request) {
	task, err := decodeJSON[model.Task](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}
	if task.TypeName == "" {
		h.handleError(w, errBadRequest("task_type_name is required"))
		return
	}
	if task.ID == "" {
		task.ID = uuid.New().String()
	}

	if len(task.ParametersJSON) > 0 {
		var m map[string]any
		if err := json.Unmarshal(task.ParametersJSON, &m); err != nil {
			h.handleError(w, errBadRequest("parameters_json must be a JSON object"))
			return
		}
		if m == nil {
			h.handleError(w, errBadRequest("parameters_json must be a JSON object"))
			return
		}
	}

	if task.TargetAgentID != "" {
		agent, err := h.agentReg.GetAgent(r.Context(), task.TargetAgentID)
		if err != nil || agent == nil {
			h.handleError(w, errNotFound("agent not found: "+task.TargetAgentID))
			return
		}

		// Reject task submission if agent is not online
		if agent.Status == nil || agent.Status.State != agentregistry.AgentStateOnline {
			h.handleError(w, errBadRequest("agent is not online, cannot submit task"))
			return
		}

		agentMeta := &taskmanager.AgentMeta{
			AgentID:     agent.AgentID,
			AppID:       agent.AppID,
			ServiceName: agent.ServiceName,
		}

		if err := h.taskMgr.SubmitTaskForAgent(r.Context(), agentMeta, task); err != nil {
			h.handleError(w, err)
			return
		}
	} else {
		if err := h.taskMgr.SubmitTask(r.Context(), task); err != nil {
			h.handleError(w, err)
			return
		}
	}

	h.writeJSON(w, http.StatusOK, successResponse("task submitted", map[string]any{"task_id": task.ID}))
}

func (h *adminHandlers) getTaskV2(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	info, err := h.taskMgr.GetTaskStatus(r.Context(), taskID)
	if err == nil && info != nil {
		// Always fetch the latest result from the result store.
		// Callbacks (h.g., analysis_callback) may update the result independently
		// after the task has reached a terminal state, so info.Result can be stale.
		result, found, err := h.taskMgr.GetTaskResult(r.Context(), taskID)
		if err != nil {
			h.handleError(w, err)
			return
		}
		if found {
			info.Result = result
		}

		h.writeJSON(w, http.StatusOK, toTaskInfoV2(info))
		return
	}

	result, found, err := h.taskMgr.GetTaskResult(r.Context(), taskID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	if found {
		h.writeJSON(w, http.StatusOK, result)
		return
	}

	h.handleError(w, errNotFound("task not found"))
}

func (h *adminHandlers) cancelTaskV2(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	if err := h.taskMgr.CancelTask(r.Context(), taskID); err != nil {
		h.handleError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("task cancelled", map[string]any{"task_id": taskID}))
}

func (h *adminHandlers) batchTaskActionV2(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[struct {
		Action  string   `json:"action"`
		TaskIDs []string `json:"task_ids"`
	}](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	switch req.Action {
	case "cancel":
		var cancelled, failed []string
		for _, taskID := range req.TaskIDs {
			if err := h.taskMgr.CancelTask(r.Context(), taskID); err != nil {
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
	default:
		h.handleError(w, errBadRequest("invalid action: "+req.Action))
	}
}

// ============================================================================
// Dashboard
// ============================================================================

func (h *adminHandlers) getDashboardOverview(w http.ResponseWriter, r *http.Request) {
	// Get cached instance stats (CachingRegistry, 5s TTL, O(1) with cache hit)
	instanceStats, err := h.agentReg.GetAgentStats(r.Context())
	if err != nil {
		instanceStats = &agentregistry.AgentStats{}
	}

	// Get app count (single Redis HGetAll, efficient)
	apps, _ := h.tokenMgr.ListApps(r.Context())

	// pending/running task counts are omitted here; the /tasks page provides
	// full task visibility. These queries (GetGlobalPendingTasks via ListTasks)
	// require O(n) Redis SCAN+GET and hurt dashboard performance.

	h.writeJSON(w, http.StatusOK, map[string]any{
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
			"pending": 0,
		},
	})
}