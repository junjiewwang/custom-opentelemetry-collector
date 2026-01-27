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

	// WebUI - serve embedded static files (no auth required for UI assets)
	webUI, err := newWebUIHandler()
	if err == nil {
		serveIndex := func(w http.ResponseWriter, req *http.Request) {
			req.URL.Path = "/index.html"
			webUI.ServeHTTP(w, req)
		}
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
		r.Get("/ui", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/ui/", http.StatusMovedPermanently)
		})
		// Handle /ui/ explicitly (chi's /* doesn't match trailing slash)
		r.Get("/ui/", serveIndex)
		r.Get("/ui/*", func(w http.ResponseWriter, req *http.Request) {
			// Strip /ui prefix for file serving
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/ui")
			webUI.ServeHTTP(w, req)
		})
	}

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
		})

		// ============================================================================
		// Dashboard
		// ============================================================================
		r.Get("/dashboard/overview", e.getDashboardOverview)

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
