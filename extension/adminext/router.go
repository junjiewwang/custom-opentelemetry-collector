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
	r.Use(e.tracingMiddleware)
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

				// Retention management (per-app data lifecycle policy)
				r.Get("/retention", e.handleAppRetention)
				r.Put("/retention/{signal}", e.handleSetAppRetention)
				r.Delete("/retention/{signal}", e.handleDeleteAppRetention)

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
					r.Get("/disk-usage/daily", e.handleStorageDailyUsage)
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
			r.Route("/influxdb", func(r chi.Router) {
				r.Get("/ping", e.handleInfluxDBPing)   // Health check (some Grafana versions)
				r.Head("/ping", e.handleInfluxDBPing)  // Health check HEAD variant
				r.Post("/query", e.handleInfluxDBQuery)
				r.Get("/query", e.handleInfluxDBQuery) // Grafana may use GET with params
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
		r.Route("/prometheus/api/v1", func(r chi.Router) {
			r.Get("/query", e.handlePromQuery)
			r.Post("/query", e.handlePromQuery)
			r.Get("/query_range", e.handlePromQueryRange)
			r.Post("/query_range", e.handlePromQueryRange)
			r.Get("/labels", e.handlePromLabels)
			r.Post("/labels", e.handlePromLabels)
			r.Get("/label/{labelName}/values", e.handlePromLabelValues)
			r.Get("/series", e.handlePromSeries)
			r.Post("/series", e.handlePromSeries)
			r.Get("/metadata", e.handlePromMetadata)
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
		r.Route("/tempo", func(r chi.Router) {
			if e.storageTraceReader != nil {
			// V1 endpoints
			r.Get("/api/echo", e.handleTempoEcho)
			r.Get("/api/status/buildinfo", e.handleTempoBuildInfo)
			r.Get("/api/traces/{traceID}", e.handleTempoGetTrace)
				r.Get("/api/search", e.handleTempoSearch)
				r.Get("/api/search/tags", e.handleTempoSearchTags)
				r.Get("/api/search/tag/{tagName}/values", e.handleTempoSearchTagValues)

			// V2 endpoints (Grafana 12+ calls these by default)
			// Both GET and POST: Grafana may use POST for long TraceQL queries.
			r.Get("/api/v2/traces/{traceID}", e.handleTempoV2GetTrace)
			r.Post("/api/v2/traces/{traceID}", e.handleTempoV2GetTrace)
			r.Get("/api/v2/search", e.handleTempoV2Search)
			r.Post("/api/v2/search", e.handleTempoV2Search)
			r.Get("/api/v2/search/tags", e.handleTempoV2SearchTags)
			r.Get("/api/v2/search/tag/{tagName}/values", e.handleTempoV2SearchTagValues)
			}
			// TraceQL metrics (/api/metrics/query_range) requires either:
			// - storageTraceReader (primary: real-time aggregation from raw spans)
			// - storageMetricReader (fallback: pre-aggregated spanmetrics)
			if e.storageTraceReader != nil || e.storageMetricReader != nil {
				r.Get("/api/metrics/query_range", e.handleTempoMetricsQueryRange)
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
	r.Route("/loki/loki/api/v1", func(r chi.Router) {
		r.Get("/query", e.handleLokiInstantQuery)
		r.Post("/query", e.handleLokiInstantQuery)
		r.Get("/query_range", e.handleLokiQueryRange)
		r.Post("/query_range", e.handleLokiQueryRange)
		r.Get("/labels", e.handleLokiLabels)
		r.Get("/label/{name}/values", e.handleLokiLabelValues)
		// logs-drilldown app endpoints (Loki 3.x compatibility)
		r.Get("/index/volume", e.handleLokiIndexVolume)
		r.Get("/drilldown-limits", e.handleLokiDrilldownLimits)
	})
	// Shorter aliases for direct curl/API access
	// Full: /api/v2 + /loki/* = /api/v2/loki/*
	r.Route("/loki", func(r chi.Router) {
		r.Get("/query", e.handleLokiInstantQuery)
		r.Post("/query", e.handleLokiInstantQuery)
		r.Get("/query_range", e.handleLokiQueryRange)
		r.Post("/query_range", e.handleLokiQueryRange)
		r.Get("/labels", e.handleLokiLabels)
		r.Get("/label/{name}/values", e.handleLokiLabelValues)
	})

	// ============================================================================
	// Arthas Tunnel (if enabled)
		// ============================================================================
		if e.arthasTunnel != nil {
			r.Route("/arthas", func(r chi.Router) {
				// WebSocket endpoint for browser terminal (uses WS token auth)
				r.Get("/ws", e.handleArthasWebSocket)
			})
		}
	})

	return r
}
