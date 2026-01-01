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

	// API v1 routes (with optional auth middleware)
	r.Route("/api/v1", func(r chi.Router) {
		// Apply auth middleware only to API routes
		if e.config.Auth.Enabled {
			r.Use(e.authMiddleware)
		}
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

				// Config management
				r.Route("/config", func(r chi.Router) {
					r.Get("/", e.getAppDefaultConfig)
					r.Put("/", e.setAppDefaultConfig)
					r.Get("/{instanceID}", e.getAppInstanceConfig)
					r.Put("/{instanceID}", e.setAppInstanceConfig)
					r.Delete("/{instanceID}", e.deleteAppInstanceConfig)
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
		// Task Management (global, cross-app)
		// ============================================================================
		r.Route("/tasks", func(r chi.Router) {
			r.Get("/", e.listTasks)
			r.Post("/", e.createTask)
			r.Post("/batch", e.batchTaskAction)
			r.Get("/{taskID}", e.getTask)
			r.Delete("/{taskID}", e.cancelTask)
		})

		// ============================================================================
		// Dashboard
		// ============================================================================
		r.Get("/dashboard/overview", e.getDashboardOverview)
	})

	return r
}
