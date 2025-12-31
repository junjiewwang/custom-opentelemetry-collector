// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// newHTTPRouter creates a new HTTP router with all routes registered.
func (r *enhancedOTLPReceiver) newHTTPRouter() http.Handler {
	router := chi.NewRouter()

	// Global middleware
	router.Use(middleware.Recoverer)

	// Register OTLP HTTP endpoints
	r.registerOTLPRoutes(router)

	// Register Control Plane endpoints if enabled
	if r.config.ControlPlane.Enabled {
		r.registerControlPlaneRoutes(router)
	}

	// Health check endpoint
	router.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	return router
}

// registerOTLPRoutes registers OTLP HTTP protocol endpoints.
func (r *enhancedOTLPReceiver) registerOTLPRoutes(router chi.Router) {
	if r.tracesConsumer != nil {
		tracesPath := r.config.GetTracesURLPath()
		router.Post(tracesPath, r.handleTraces)
		r.logger.Debug("Registered traces endpoint", zap.String("path", tracesPath))
	}

	if r.metricsConsumer != nil {
		metricsPath := r.config.GetMetricsURLPath()
		router.Post(metricsPath, r.handleMetrics)
		r.logger.Debug("Registered metrics endpoint", zap.String("path", metricsPath))
	}

	if r.logsConsumer != nil {
		logsPath := r.config.GetLogsURLPath()
		router.Post(logsPath, r.handleLogs)
		r.logger.Debug("Registered logs endpoint", zap.String("path", logsPath))
	}
}

// registerControlPlaneRoutes registers control plane API endpoints.
func (r *enhancedOTLPReceiver) registerControlPlaneRoutes(router chi.Router) {
	prefix := r.config.GetControlURLPathPrefix()
	handler := newControlHandler(r.logger, r.controlPlane)

	router.Route(prefix, func(cr chi.Router) {
		// Middleware: check controlPlane availability
		cr.Use(handler.requireControlPlane)

		// Configuration management
		cr.Get("/config", handler.getConfig)
		cr.Post("/config", handler.postConfig)

		// Task management
		cr.Get("/tasks", handler.getTasks)
		cr.Post("/tasks", handler.createTask)
		cr.Delete("/tasks", handler.deleteTask)
		cr.Post("/tasks/cancel", handler.cancelTasks)

		// Status and heartbeat
		cr.Get("/status", handler.getStatus)
		cr.Post("/status", handler.postStatus)

		// Agent registration and management
		cr.Post("/register", handler.registerAgent)
		cr.Post("/unregister", handler.unregisterAgent)
		cr.Get("/agents", handler.listAgents)
		cr.Get("/agents/stats", handler.getAgentStats)

		// Chunk upload
		cr.Post("/upload-chunk", handler.uploadChunk)
	})

	r.logger.Info("Registered control plane endpoints", zap.String("prefix", prefix))
}
