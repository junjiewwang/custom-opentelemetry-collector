// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// newHTTPRouter creates a new HTTP router with all routes registered.
func (r *agentGatewayReceiver) newHTTPRouter() http.Handler {
	router := chi.NewRouter()

	// Global middleware
	router.Use(middleware.Recoverer)
	router.Use(middleware.RequestID)
	router.Use(r.loggingMiddleware)

	// Health check endpoint (no auth required)
	router.Get("/health", r.healthHandler)

	// Log route registration status
	r.logger.Info("Registering routes",
		zap.Bool("control_plane_enabled", r.config.ControlPlane.Enabled),
		zap.Bool("control_plane_available", r.controlPlane != nil),
		zap.Bool("otlp_enabled", r.config.OTLP.Enabled),
		zap.Bool("arthas_tunnel_enabled", r.config.ArthasTunnel.Enabled),
		zap.Bool("arthas_tunnel_available", r.arthasTunnel != nil),
	)

	// Routes that may require authentication
	router.Group(func(gr chi.Router) {
		// Token authentication middleware
		if r.config.TokenAuth.Enabled && r.controlPlane != nil {
			gr.Use(r.tokenAuthMiddleware)
		}

		// OTLP routes
		if r.config.OTLP.Enabled {
			r.registerOTLPRoutes(gr)
		}

		// Control Plane routes
		if r.config.ControlPlane.Enabled && r.controlPlane != nil {
			r.registerControlPlaneRoutes(gr)
		} else {
			r.logger.Warn("Control plane routes NOT registered",
				zap.Bool("enabled", r.config.ControlPlane.Enabled),
				zap.Bool("extension_found", r.controlPlane != nil),
			)
		}

		// Arthas Tunnel routes
		if r.config.ArthasTunnel.Enabled && r.arthasTunnel != nil {
			r.registerArthasTunnelRoutes(gr)
		}
	})

	return router
}

// healthHandler handles health check requests.
func (r *agentGatewayReceiver) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// registerOTLPRoutes registers OTLP HTTP protocol endpoints.
func (r *agentGatewayReceiver) registerOTLPRoutes(router chi.Router) {
	if r.tracesConsumer != nil {
		tracesPath := r.config.GetTracesPath()
		router.Post(tracesPath, r.handleTraces)
		r.logger.Debug("Registered traces endpoint", zap.String("path", tracesPath))
	}

	if r.metricsConsumer != nil {
		metricsPath := r.config.GetMetricsPath()
		router.Post(metricsPath, r.handleMetrics)
		r.logger.Debug("Registered metrics endpoint", zap.String("path", metricsPath))
	}

	if r.logsConsumer != nil {
		logsPath := r.config.GetLogsPath()
		router.Post(logsPath, r.handleLogs)
		r.logger.Debug("Registered logs endpoint", zap.String("path", logsPath))
	}
}

// registerControlPlaneRoutes registers control plane API endpoints.
func (r *agentGatewayReceiver) registerControlPlaneRoutes(router chi.Router) {
	prefix := r.config.GetControlPlanePathPrefix()
	handler := newControlPlaneHandler(r.logger, r.controlPlane, r.longPollManager)

	router.Route(prefix, func(cr chi.Router) {
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

		// Long polling endpoints
		cr.Post("/poll", handler.poll)              // Unified poll for config and tasks
		cr.Post("/poll/config", handler.pollConfig) // Config-only poll
		cr.Post("/poll/tasks", handler.pollTasks)   // Task-only poll
	})

	r.logger.Info("Registered control plane endpoints", zap.String("prefix", prefix))
}

// registerArthasTunnelRoutes registers Arthas tunnel WebSocket endpoint.
func (r *agentGatewayReceiver) registerArthasTunnelRoutes(router chi.Router) {
	path := r.config.GetArthasTunnelPath()

	router.Get(path, r.arthasTunnel.HandleAgentWebSocket)

	r.logger.Info("Registered Arthas tunnel endpoint", zap.String("path", path))
}
