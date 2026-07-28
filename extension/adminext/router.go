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
	r.Use(NewTracingMiddleware())
	r.Use(NewLoggingMiddleware(e.logger))
	if e.config.CORS.Enabled {
		r.Use(NewCORSMiddleware(e.config.CORS))
	}

	// Admin API handlers (apps/services/instances/tasks/instrumentation/
	// notifications/artifact/arthas/retention) — dependency-injected, see admin_deps.go.
	admin := newAdminHandlers(e)

	// Health check (no auth required)
	r.Get("/health", admin.handleHealth)

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
	r.Post("/api/v2/callback/analysis", admin.handleAnalysisCallback)

	// API v2 routes (admin API is v2-only)
	r.Route("/api/v2", func(r chi.Router) {
		// Apply auth middleware only to API routes
		if e.config.Auth.Enabled {
			r.Use(NewAuthMiddleware(e.config.Auth, e.logger))
		}

		// ============================================================================
		// Auth - WebSocket Token (for secure WS connections)
		// ============================================================================
		r.Post("/auth/ws-token", admin.generateWSToken)

		// ============================================================================
		// App Management (App = AppGroup, 1:1 with Token)
		// ============================================================================
		r.Route("/apps", func(r chi.Router) {
			r.Get("/", admin.listApps)
			r.Post("/", admin.createApp)

			r.Route("/{appID}", func(r chi.Router) {
				r.Get("/", admin.getApp)
				r.Put("/", admin.updateApp)
				r.Delete("/", admin.deleteApp)
				r.Post("/token", admin.regenerateAppToken)
				r.Put("/token", admin.setAppToken)

				// Config management (Simplified: Service-level only)
				r.Route("/config", func(r chi.Router) {
					// Service level
					r.Get("/services/{serviceName}", admin.getAppServiceConfigV2)
					r.Put("/services/{serviceName}", admin.setAppServiceConfigV2)
					r.Delete("/services/{serviceName}", admin.deleteAppServiceConfigV2)
				})

				// Retention management (per-app data lifecycle policy)
				r.Get("/retention", admin.handleAppRetention)
				r.Put("/retention/{signal}", admin.handleSetAppRetention)
				r.Delete("/retention/{signal}", admin.handleDeleteAppRetention)

				// Services under app
				r.Get("/services", admin.listAppServices)
				r.Get("/services/{serviceName}", admin.getService)
				r.Put("/services/{serviceName}", admin.updateServiceMetadata)
				r.Delete("/services/{serviceName}", admin.deleteService)
				r.Get("/services/{serviceName}/instances", admin.listServiceInstances)

				// Instances under app
				r.Get("/instances", admin.listAppInstances)
				r.Get("/instances/{instanceID}", admin.getAppInstance)
				r.Post("/instances/{instanceID}/kick", admin.kickAppInstance)
			})
		})

		// ============================================================================
		// Global Service View
		// ============================================================================
		r.Get("/services", admin.listAllServices)

		// ============================================================================
		// Global Instance View (for operations/dashboard)
		// ============================================================================
		r.Get("/instances", admin.listAllInstances)
		r.Get("/instances/stats", admin.getInstanceStats)
		r.Get("/instances/{instanceID}", admin.getInstance)
		r.Post("/instances/{instanceID}/kick", admin.kickInstance)

		// ============================================================================
		// Task Management (global, cross-app) - model JSON
		// ============================================================================
		r.Route("/tasks", func(r chi.Router) {
			r.Get("/", admin.listTasksV2)
			r.Post("/", admin.createTaskV2)
			r.Post("/batch", admin.batchTaskActionV2)
			r.Get("/{taskID}", admin.getTaskV2)
			r.Delete("/{taskID}", admin.cancelTaskV2)

			// Artifact download (profiling data, heap dumps, etc.)
			r.Get("/{taskID}/artifact", admin.handleGetTaskArtifact)
			r.Get("/{taskID}/artifact/meta", admin.handleGetTaskArtifactMeta)
		})

		// ============================================================================
		// Dynamic Instrumentation Workbench
		// ============================================================================
		r.Route("/instrumentation", func(r chi.Router) {
			r.Get("/rules", admin.listInstrumentationRules)
			r.Post("/rules", admin.createInstrumentationRule)
			r.Get("/rules/{ruleID}", admin.getInstrumentationRule)
			r.Put("/rules/{ruleID}", admin.updateInstrumentationRule)
			r.Post("/rules/{ruleID}/pause", admin.pauseInstrumentationRule)
			r.Post("/rules/{ruleID}/resume", admin.resumeInstrumentationRule)
			r.Delete("/rules/{ruleID}", admin.deleteInstrumentationRule)
			r.Get("/rules/{ruleID}/targets", admin.listInstrumentationTargets)
			r.Get("/rules/{ruleID}/runtime-snapshot", admin.getInstrumentationRuntimeSnapshot)
			r.Post("/rules/{ruleID}/runtime-snapshot/refresh", admin.refreshInstrumentationRuntimeSnapshot)
		})

		// ============================================================================
		// Dashboard
		// ============================================================================
		r.Get("/dashboard/overview", admin.getDashboardOverview)

		// ============================================================================
		// Notification Management (monitoring & retry)
		// ============================================================================
		r.Route("/notifications", func(r chi.Router) {
			r.Get("/", admin.listNotifications)
			r.Post("/retry-all", admin.retryAllFailedNotifications)
			r.Get("/{id}", admin.getNotification)
			r.Post("/{id}/retry", admin.retryNotification)
		})

		// ============================================================================
		// Observability Query API (Trace + Metric + Log + Admin)
		//
		// One mode: Storage Extension mode — structured JSON responses from the
		// observability_storage Reader. The legacy Jaeger/Prometheus proxy mode
		// has been removed.
		// ============================================================================
		obsV2 := newObsV2Handlers(e)
		r.Route("/observability", func(r chi.Router) {
			// --- Trace 查询 ---
			if e.storageTraceReader != nil {
				// V2 mode: structured responses from storage extension
				r.Route("/traces", func(r chi.Router) {
					r.Get("/", obsV2.handleSearchTracesV2)
					r.Get("/services", obsV2.handleGetTraceServicesV2)
					r.Get("/services/{service}/operations", obsV2.handleGetTraceOperationsV2)
					r.Get("/{traceID}", obsV2.handleGetTraceV2)
				})
				r.Get("/dependencies", obsV2.handleGetDependenciesV2)
			}

			// --- Metric 查询 ---
			if e.storageMetricReader != nil {
				// V2 mode: structured responses from storage extension
				r.Route("/metrics", func(r chi.Router) {
					r.Get("/query", obsV2.handleMetricQueryV2)
					r.Get("/query_range", obsV2.handleMetricQueryRangeV2)
					r.Get("/names", obsV2.handleMetricNamesV2)
					r.Get("/labels", obsV2.handleMetricLabelsV2)
					r.Get("/labels/{labelName}/values", obsV2.handleMetricLabelValuesV2)
				})
			}

			// --- Log 查询 (仅 storage extension 模式) ---
			if e.storageLogReader != nil {
				r.Route("/logs", func(r chi.Router) {
					r.Get("/", obsV2.handleSearchLogs)
					r.Get("/fields", obsV2.handleListLogFields)
					r.Get("/stats", obsV2.handleGetLogStats)
					r.Get("/{logID}/context", obsV2.handleGetLogContext)
				})
			}

			// --- Storage Admin (仅 storage extension 模式) ---
			if e.storageAdmin != nil {
				r.Route("/admin", func(r chi.Router) {
					r.Get("/status", obsV2.handleStorageStatus)
					r.Get("/health", obsV2.handleStorageHealth)
					r.Get("/retention", obsV2.handleStorageRetention)
					r.Put("/retention/{signal}", obsV2.handleSetStorageRetention)
					r.Post("/purge/{signal}", obsV2.handleStoragePurge)
					r.Get("/disk-usage", obsV2.handleStorageDiskUsage)
					r.Get("/disk-usage/daily", obsV2.handleStorageDailyUsage)
				})
			}
		})

		// ============================================================================
		// InfluxDB v1 Compatible API (for Grafana direct connection)
		// ============================================================================
		// Grafana configuration:
		//   Type: InfluxDB
		//   URL: http://<collector>:8088/api/v2
		//   Access: Server
		//   Database: <app_id>
		if e.storageMetricReader != nil {
			influx := newInfluxHandlers(e)
			r.Route("/influxdb", func(r chi.Router) {
				r.Get("/ping", influx.handleInfluxDBPing)  // Health check (some Grafana versions)
				r.Head("/ping", influx.handleInfluxDBPing) // Health check HEAD variant
				r.Post("/query", influx.handleInfluxDBQuery)
				r.Get("/query", influx.handleInfluxDBQuery) // Grafana may use GET with params
			})
		}

	// ============================================================================
	// Prometheus v1 Compatible API (for Grafana Prometheus data source)
	// ============================================================================
	// Grafana configuration:
	//   Type: Prometheus
	//   URL: http://<collector>:8088/api/v2/prometheus
	//   Access: Server (proxy)
	//   Auth: Basic Auth (same as admin API)
	if e.storageMetricReader != nil {
		prom := newPromHandlers(e)
		r.Route("/prometheus/api/v1", func(r chi.Router) {
			r.Get("/query", prom.handlePromQuery)
			r.Post("/query", prom.handlePromQuery)
			r.Get("/query_range", prom.handlePromQueryRange)
			r.Post("/query_range", prom.handlePromQueryRange)
			r.Get("/labels", prom.handlePromLabels)
			r.Post("/labels", prom.handlePromLabels)
			r.Get("/label/{labelName}/values", prom.handlePromLabelValues)
			r.Get("/series", prom.handlePromSeries)
			r.Post("/series", prom.handlePromSeries)
			r.Get("/metadata", prom.handlePromMetadata)
		})
	}

	// ============================================================================
	// Grafana Tempo Compatible API (for Grafana Tempo data source)
	// ============================================================================
	// Grafana configuration:
	//   Type: Tempo
	//   URL: http://<collector>:8088/api/v2/tempo
	//   Access: Server (proxy)
	//   Auth: Basic Auth (same as admin API)
	// Tempo API — trace endpoints (require storageTraceReader) and metrics (require storageMetricReader).
	if e.storageTraceReader != nil || e.storageMetricReader != nil {
		tempo := newTempoHandlers(e)
		r.Route("/tempo", func(r chi.Router) {
			if e.storageTraceReader != nil {
			// V1 endpoints
			r.Get("/api/echo", tempo.handleTempoEcho)
			r.Get("/api/status/buildinfo", tempo.handleTempoBuildInfo)
			r.Get("/api/traces/{traceID}", tempo.handleTempoGetTrace)
				r.Get("/api/search", tempo.handleTempoSearch)
				r.Get("/api/search/tags", tempo.handleTempoSearchTags)
				r.Get("/api/search/tag/{tagName}/values", tempo.handleTempoSearchTagValues)

			// V2 endpoints (Grafana 12+ calls these by default)
			// Both GET and POST: Grafana may use POST for long TraceQL queries.
			r.Get("/api/v2/traces/{traceID}", tempo.handleTempoV2GetTrace)
			r.Post("/api/v2/traces/{traceID}", tempo.handleTempoV2GetTrace)
			r.Get("/api/v2/search", tempo.handleTempoV2Search)
			r.Post("/api/v2/search", tempo.handleTempoV2Search)
			r.Get("/api/v2/search/tags", tempo.handleTempoV2SearchTags)
			r.Get("/api/v2/search/tag/{tagName}/values", tempo.handleTempoV2SearchTagValues)
			}
			// TraceQL metrics (/api/metrics/query_range) requires either:
			// - storageTraceReader (primary: real-time aggregation from raw spans)
			// - storageMetricReader (fallback: pre-aggregated spanmetrics)
			if e.storageTraceReader != nil || e.storageMetricReader != nil {
				r.Get("/api/metrics/query_range", tempo.handleTempoMetricsQueryRange)
			}
		})

		// Loki Compatible API — requires storageLogReader
		// WARNING: Go import cycle detection in chi. Because this block is inside
		// the /tempo scope, the actual routes are exposed at /loki directly.
	}

	// Loki API — log endpoints (requires storageLogReader)
	// Grafana Loki datasource URL = /api/v2/loki
	// Grafana hardcodes /loki/api/v1/* suffix after the datasource URL.
	// Full request paths from Grafana:
	//   /api/v2/loki/loki/api/v1/query
	//   /api/v2/loki/loki/api/v1/query_range
	//   /api/v2/loki/loki/api/v1/labels
	//   /api/v2/loki/loki/api/v1/label/{name}/values
	//
	// Since we are INSIDE r.Route("/api/v2", ...), the sub-paths are relative
	// to /api/v2. So we register /loki/loki/api/v1/* here.
	//
	// Routes are registered unconditionally — each handler returns a clear
	// error when storageLogReader is nil, rather than a cryptic 404.

	// Main routes: Grafana calls (datasource path + hardcoded Grafana suffix)
	// Full: /api/v2 + /loki/loki/api/v1/* = /api/v2/loki/loki/api/v1/*
	// Supports both GET and POST — Grafana may use either for query_range.
	loki := newLokiHandlers(e)
	r.Route("/loki/loki/api/v1", func(r chi.Router) {
		r.Get("/query", loki.handleLokiInstantQuery)
		r.Post("/query", loki.handleLokiInstantQuery)
		r.Get("/query_range", loki.handleLokiQueryRange)
		r.Post("/query_range", loki.handleLokiQueryRange)
		r.Get("/labels", loki.handleLokiLabels)
		r.Get("/label/{name}/values", loki.handleLokiLabelValues)
		// logs-drilldown app endpoints (Loki 3.x compatibility)
		r.Get("/index/volume", loki.handleLokiIndexVolume)
		r.Get("/index/stats", loki.handleLokiIndexStats)
		r.Get("/drilldown-limits", loki.handleLokiDrilldownLimits)
		r.Get("/detected_labels", loki.handleLokiDetectedLabels)
		r.Get("/detected_fields", loki.handleLokiDetectedFields)
		r.Get("/detected_field/{name}/values", loki.handleLokiDetectedFieldValues)
	})
	// Shorter aliases for direct curl/API access
	// Full: /api/v2 + /loki/* = /api/v2/loki/*
	r.Route("/loki", func(r chi.Router) {
		r.Get("/query", loki.handleLokiInstantQuery)
		r.Post("/query", loki.handleLokiInstantQuery)
		r.Get("/query_range", loki.handleLokiQueryRange)
		r.Post("/query_range", loki.handleLokiQueryRange)
		r.Get("/labels", loki.handleLokiLabels)
		r.Get("/label/{name}/values", loki.handleLokiLabelValues)
	})

	// ============================================================================
	// Arthas Tunnel (if enabled)
		// ============================================================================
		if e.arthasTunnel != nil {
			r.Route("/arthas", func(r chi.Router) {
				// WebSocket endpoint for browser terminal (uses WS token auth)
				r.Get("/ws", admin.handleArthasWebSocket)
			})
		}
	})

	return r
}
