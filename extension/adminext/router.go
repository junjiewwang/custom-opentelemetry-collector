// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// newRouter creates and configures the HTTP router with all routes.
// Route hierarchy (App = AppGroup, 1:1 relationship):
//
//	App (应用) ← 一个 Token
//	  └── Service (服务)
//	        └── Instance (探针实例)
func (e *Extension) newRouter() http.Handler {
	r := chi.NewRouter()

	// Global middleware (must be defined before any routes)
	r.Use(middleware.Recoverer)
	r.Use(e.loggingMiddleware)
	if e.config.CORS.Enabled {
		r.Use(e.corsMiddleware)
	}

	// Health check (no auth required)
	r.Get("/health", e.handleHealth)

	// ============================================================================
	// Internal proxy routes for distributed Arthas tunnel (no admin auth)
	// ============================================================================
	// These routes handle cross-node proxy requests in distributed mode.
	// Authentication is handled internally via X-Internal-Token header.
	// Must be registered before /api/v1 routes to avoid auth middleware.
	if e.arthasTunnel != nil && e.arthasTunnel.IsDistributedMode() {
		internalPrefix := e.arthasTunnel.GetInternalPathPrefix()
		// Use Mount to delegate all requests under the prefix to the tunnel handler
		r.Mount(internalPrefix, http.HandlerFunc(e.arthasTunnel.HandleInternalProxy))
	}

	// ============================================================================
	// WebUI - React 前端 (/ui/) — 唯一前端入口
	// ============================================================================

	// React 前端 - 挂载在 /ui/
	reactUI, reactErr := newReactUIHandler()
	if reactErr == nil {
		serveReactIndex := func(w http.ResponseWriter, req *http.Request) {
			req.URL.Path = "/index.html"
			reactUI.ServeHTTP(w, req)
		}
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
		r.Get("/ui", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
		// Handle /ui/ explicitly (chi's /* doesn't match trailing slash)
		r.Get("/ui/", serveReactIndex)
		r.Get("/ui/*", func(w http.ResponseWriter, req *http.Request) {
			// Strip /ui prefix for file serving
			stripped := strings.TrimPrefix(req.URL.Path, "/ui")
			req.URL.Path = stripped
			reactUI.ServeHTTP(w, req)
		})
		// Legacy redirect: /legacy/* → /ui/
		r.Get("/legacy", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
		r.Get("/legacy/*", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
	}

	// ============================================================================
	// Analysis Callback (from perf-analysis service, no admin auth needed)
	// External services call back without admin credentials, so this must be
	// registered OUTSIDE the authMiddleware scope.
	// ============================================================================
	r.Post("/api/v2/callback/analysis", e.handleAnalysisCallback)

	// API v2 routes (admin API is v2-only)
	r.Route("/api/v2", func(r chi.Router) {
		// Apply auth middleware only to API routes
		if e.config.Auth.Enabled {
			r.Use(e.authMiddleware)
		}

		// ============================================================================
		// Auth - WebSocket Token (for secure WS connections)
		// ============================================================================
		r.Post("/auth/ws-token", e.generateWSToken)

		// ============================================================================
		// App Management (App = AppGroup, 1:1 with Token)
		// ============================================================================
		r.Route("/apps", func(r chi.Router) {
			r.Get("/", e.listApps)
			r.Post("/", e.createApp)

			r.Route("/{appID}", func(r chi.Router) {
				r.Get("/", e.getApp)
				r.Put("/", e.updateApp)
				r.Delete("/", e.deleteApp)
				r.Post("/token", e.regenerateAppToken)
				r.Put("/token", e.setAppToken)

				// Config management (Simplified: Service-level only)
				r.Route("/config", func(r chi.Router) {
					// Service level
					r.Get("/services/{serviceName}", e.getAppServiceConfigV2)
					r.Put("/services/{serviceName}", e.setAppServiceConfigV2)
					r.Delete("/services/{serviceName}", e.deleteAppServiceConfigV2)
				})

				// Services under app
				r.Get("/services", e.listAppServices)
				r.Get("/services/{serviceName}", e.getService)
				r.Put("/services/{serviceName}", e.updateServiceMetadata)
				r.Delete("/services/{serviceName}", e.deleteService)
				r.Get("/services/{serviceName}/instances", e.listServiceInstances)

				// Instances under app
				r.Get("/instances", e.listAppInstances)
				r.Get("/instances/{instanceID}", e.getAppInstance)
				r.Post("/instances/{instanceID}/kick", e.kickAppInstance)
			})
		})

		// ============================================================================
		// Global Service View
		// ============================================================================
		r.Get("/services", e.listAllServices)

		// ============================================================================
		// Global Instance View (for operations/dashboard)
		// ============================================================================
		r.Get("/instances", e.listAllInstances)
		r.Get("/instances/stats", e.getInstanceStats)
		r.Get("/instances/{instanceID}", e.getInstance)
		r.Post("/instances/{instanceID}/kick", e.kickInstance)

		// ============================================================================
		// Task Management (global, cross-app) - model JSON
		// ============================================================================
		r.Route("/tasks", func(r chi.Router) {
			r.Get("/", e.listTasksV2)
			r.Post("/", e.createTaskV2)
			r.Post("/batch", e.batchTaskActionV2)
			r.Get("/{taskID}", e.getTaskV2)
			r.Delete("/{taskID}", e.cancelTaskV2)

			// Artifact download (profiling data, heap dumps, etc.)
			r.Get("/{taskID}/artifact", e.handleGetTaskArtifact)
			r.Get("/{taskID}/artifact/meta", e.handleGetTaskArtifactMeta)
		})

		// ============================================================================
		// Dynamic Instrumentation Workbench
		// ============================================================================
		r.Route("/instrumentation", func(r chi.Router) {
			r.Get("/rules", e.listInstrumentationRules)
			r.Post("/rules", e.createInstrumentationRule)
			r.Get("/rules/{ruleID}", e.getInstrumentationRule)
			r.Put("/rules/{ruleID}", e.updateInstrumentationRule)
			r.Post("/rules/{ruleID}/pause", e.pauseInstrumentationRule)
			r.Post("/rules/{ruleID}/resume", e.resumeInstrumentationRule)
			r.Delete("/rules/{ruleID}", e.deleteInstrumentationRule)
			r.Get("/rules/{ruleID}/targets", e.listInstrumentationTargets)
			r.Get("/rules/{ruleID}/runtime-snapshot", e.getInstrumentationRuntimeSnapshot)
			r.Post("/rules/{ruleID}/runtime-snapshot/refresh", e.refreshInstrumentationRuntimeSnapshot)
		})

		// ============================================================================
		// Dashboard
		// ============================================================================
		r.Get("/dashboard/overview", e.getDashboardOverview)

		// ============================================================================
		// Notification Management (monitoring & retry)
		// ============================================================================
		r.Route("/notifications", func(r chi.Router) {
			r.Get("/", e.listNotifications)
			r.Post("/retry-all", e.retryAllFailedNotifications)
			r.Get("/{id}", e.getNotification)
			r.Post("/{id}/retry", e.retryNotification)
		})

		// ============================================================================
		// Observability Query API (Trace + Metric + Log + Admin)
		//
		// Two modes:
		//   1. Storage Extension mode (preferred): structured JSON responses from ES Reader
		//   2. Legacy Proxy mode: raw proxying to Jaeger/Prometheus (backward compatible)
		// ============================================================================
		r.Route("/observability", func(r chi.Router) {
			// --- Trace 查询 ---
			if e.storageTraceReader != nil {
				// V2 mode: structured responses from storage extension
				r.Route("/traces", func(r chi.Router) {
					r.Get("/", e.handleSearchTracesV2)
					r.Get("/services", e.handleGetTraceServicesV2)
					r.Get("/services/{service}/operations", e.handleGetTraceOperationsV2)
					r.Get("/{traceID}", e.handleGetTraceV2)
				})
				r.Get("/dependencies", e.handleGetDependenciesV2)
			} else if e.traceReader != nil {
				// Legacy mode: raw proxy to Jaeger
				r.Route("/traces", func(r chi.Router) {
					r.Get("/", e.handleSearchTraces)
					r.Get("/services", e.handleGetTraceServices)
					r.Get("/services/{service}/operations", e.handleGetTraceOperations)
					r.Get("/{traceID}", e.handleGetTrace)
				})
				r.Get("/dependencies", e.handleGetDependencies)
			}

			// --- Metric 查询 ---
			if e.storageMetricReader != nil {
				// V2 mode: structured responses from storage extension
				r.Route("/metrics", func(r chi.Router) {
					r.Get("/query", e.handleMetricQueryV2)
					r.Get("/query_range", e.handleMetricQueryRangeV2)
					r.Get("/names", e.handleMetricNamesV2)
					r.Get("/labels", e.handleMetricLabelsV2)
					r.Get("/labels/{labelName}/values", e.handleMetricLabelValuesV2)
				})
			} else if e.metricReader != nil {
				// Legacy mode: raw proxy to Prometheus
				r.Route("/metrics", func(r chi.Router) {
					r.Get("/query", e.handleMetricQuery)
					r.Get("/query_range", e.handleMetricQueryRange)
					r.Get("/labels", e.handleMetricLabels)
					r.Get("/labels/{labelName}/values", e.handleMetricLabelValues)
					r.Get("/series", e.handleMetricSeries)
					r.Get("/metadata", e.handleMetricMetadata)
				})
			}

			// --- Log 查询 (仅 storage extension 模式) ---
			if e.storageLogReader != nil {
				r.Route("/logs", func(r chi.Router) {
					r.Get("/", e.handleSearchLogs)
					r.Get("/fields", e.handleListLogFields)
					r.Get("/stats", e.handleGetLogStats)
					r.Get("/{logID}/context", e.handleGetLogContext)
				})
			}

			// --- Storage Admin (仅 storage extension 模式) ---
			if e.storageAdmin != nil {
				r.Route("/admin", func(r chi.Router) {
					r.Get("/status", e.handleStorageStatus)
					r.Get("/health", e.handleStorageHealth)
					r.Get("/retention", e.handleStorageRetention)
					r.Put("/retention/{signal}", e.handleSetStorageRetention)
					r.Post("/purge/{signal}", e.handleStoragePurge)
					r.Get("/disk-usage", e.handleStorageDiskUsage)
				})
			}
		})

		// ============================================================================
		// Arthas Tunnel (if enabled)
		// ============================================================================
		if e.arthasTunnel != nil {
			r.Route("/arthas", func(r chi.Router) {
				// List agents with active tunnel connections
				r.Get("/agents", e.listArthasAgents)
				// WebSocket endpoint for browser terminal (uses WS token auth)
				r.Get("/ws", e.handleArthasWebSocket)
			})
		}
	})

	return r
}
