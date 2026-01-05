// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// Context keys for passing data between middleware and handlers.
type contextKey string

const (
	// ContextKeyAppID is the context key for the validated app ID.
	ContextKeyAppID contextKey = "appID"
	// ContextKeyAgentID is the context key for the agent ID from header.
	ContextKeyAgentID contextKey = "agentID"
	// ContextKeyToken is the context key for the raw token.
	ContextKeyToken contextKey = "token"
)

// GetAppIDFromContext retrieves the app ID from context.
func GetAppIDFromContext(ctx context.Context) string {
	if v := ctx.Value(ContextKeyAppID); v != nil {
		return v.(string)
	}
	return ""
}

// GetAgentIDFromContext retrieves the agent ID from context.
func GetAgentIDFromContext(ctx context.Context) string {
	if v := ctx.Value(ContextKeyAgentID); v != nil {
		return v.(string)
	}
	return ""
}

// GetTokenFromContext retrieves the token from context.
func GetTokenFromContext(ctx context.Context) string {
	if v := ctx.Value(ContextKeyToken); v != nil {
		return v.(string)
	}
	return ""
}

// loggingMiddleware logs HTTP requests.
func (r *agentGatewayReceiver) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)

		defer func() {
			r.logger.Debug("HTTP request",
				zap.String("method", req.Method),
				zap.String("path", req.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote_addr", req.RemoteAddr),
			)
		}()

		next.ServeHTTP(ww, req)
	})
}

// isOTLPPath checks if the path is an OTLP endpoint.
func (r *agentGatewayReceiver) isOTLPPath(path string) bool {
	return path == r.config.GetTracesPath() ||
		path == r.config.GetMetricsPath() ||
		path == r.config.GetLogsPath()
}

// isArthasTunnelPath checks if the path is the Arthas tunnel endpoint.
func (r *agentGatewayReceiver) isArthasTunnelPath(path string) bool {
	return path == r.config.GetArthasTunnelPath()
}

// extractArthasTunnelToken extracts token from Arthas tunnel request query parameters.
// Official Arthas agent passes appName as query parameter during agentRegister.
// We use appName as the token for authentication.
func (r *agentGatewayReceiver) extractArthasTunnelToken(req *http.Request) string {
	// Try appName first (official Arthas agent parameter)
	if token := req.URL.Query().Get("appName"); token != "" {
		return token
	}
	return ""
}

// tokenAuthMiddleware validates tokens for protected endpoints.
// OTLP requests without Authorization header are allowed through (can use tokenauth processor).
// Control plane requests must have valid Authorization.
// Arthas tunnel requests can use appName query parameter as token.
func (r *agentGatewayReceiver) tokenAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Check if path should skip authentication
		if r.config.IsPathSkipped(req.URL.Path) {
			next.ServeHTTP(w, req)
			return
		}

		// Skip auth for health endpoint
		if req.URL.Path == "/health" {
			next.ServeHTTP(w, req)
			return
		}

		// Extract token from header
		headerName := r.config.GetTokenAuthHeaderName()
		headerPrefix := r.config.GetTokenAuthHeaderPrefix()
		authHeader := req.Header.Get(headerName)

		var token string

		if authHeader != "" {
			// Extract token value from header
			token = authHeader
			if headerPrefix != "" && strings.HasPrefix(authHeader, headerPrefix) {
				token = strings.TrimPrefix(authHeader, headerPrefix)
			}
		} else {
			// No Authorization header, check alternative token sources

			// OTLP requests without Authorization header are allowed through
			// They can be authenticated later by tokenauth processor in the pipeline
			if r.isOTLPPath(req.URL.Path) {
				r.logger.Debug("OTLP request without auth header, allowing through for processor auth",
					zap.String("path", req.URL.Path),
				)
				next.ServeHTTP(w, req)
				return
			}

			// For Arthas tunnel path, try to extract token from query parameter appName.
			// Official Arthas agent passes appName during agentRegister, we use it as token.
			if r.isArthasTunnelPath(req.URL.Path) {
				method := req.URL.Query().Get("method")

				// openTunnel is a callback from agent after receiving startTunnel.
				// It only carries clientConnectionId (server-generated random 20-char string).
				// We allow it through without token - the clientConnectionId itself serves as auth.
				if method == "openTunnel" {
					r.logger.Debug("Arthas tunnel openTunnel request, allowing through",
						zap.String("path", req.URL.Path),
						zap.String("clientConnectionId", req.URL.Query().Get("clientConnectionId")),
					)
					next.ServeHTTP(w, req)
					return
				}

				token = r.extractArthasTunnelToken(req)
				if token != "" {
					r.logger.Debug("Arthas tunnel using appName as token",
						zap.String("path", req.URL.Path),
						zap.String("method", method),
					)
					// Fall through to token validation below
				} else {
					r.logger.Debug("Arthas tunnel request without appName token",
						zap.String("path", req.URL.Path),
						zap.String("method", method),
					)
					http.Error(w, "Unauthorized: missing appName for Arthas tunnel", http.StatusUnauthorized)
					return
				}
			} else {
				// Non-OTLP requests (control plane, etc.) must have Authorization
				r.logger.Debug("Missing authorization header for non-OTLP request",
					zap.String("path", req.URL.Path),
					zap.String("header", headerName),
				)
				http.Error(w, "Unauthorized: missing authorization header", http.StatusUnauthorized)
				return
			}
		}

		if token == "" {
			r.logger.Debug("Empty token after extraction",
				zap.String("path", req.URL.Path),
			)
			http.Error(w, "Unauthorized: invalid token format", http.StatusUnauthorized)
			return
		}

		// Validate token using control plane
		result, err := r.controlPlane.ValidateToken(req.Context(), token)
		if err != nil {
			r.logger.Warn("Token validation error",
				zap.String("path", req.URL.Path),
				zap.Error(err),
			)
			http.Error(w, "Unauthorized: token validation failed", http.StatusUnauthorized)
			return
		}

		if !result.Valid {
			r.logger.Debug("Invalid token",
				zap.String("path", req.URL.Path),
				zap.String("reason", result.Reason),
			)
			http.Error(w, "Unauthorized: "+result.Reason, http.StatusUnauthorized)
			return
		}

		// Store validated info in context
		ctx := req.Context()
		ctx = context.WithValue(ctx, ContextKeyAppID, result.AppID)
		ctx = context.WithValue(ctx, ContextKeyToken, token)

		// Extract agent ID if present.
		//
		// For WebSocket endpoints, custom headers may not be set by clients.
		// We support passing agent_id via query as well, and mirror it into
		// X-Agent-ID so downstream WS handlers can read it after upgrade.
		agentID := req.Header.Get("X-Agent-ID")
		if agentID == "" {
			agentID = req.URL.Query().Get("agent_id")
			if agentID == "" {
				agentID = req.URL.Query().Get("agentId")
			}
		}
		if agentID != "" {
			ctx = context.WithValue(ctx, ContextKeyAgentID, agentID)
			req.Header.Set("X-Agent-ID", agentID)
		}

		// Also set X-App-ID header for WebSocket handlers (context is lost after upgrade)
		req.Header.Set("X-App-ID", result.AppID)

		r.logger.Debug("Token validated",
			zap.String("path", req.URL.Path),
			zap.String("app_id", result.AppID),
			zap.String("agent_id", agentID),
		)

		next.ServeHTTP(w, req.WithContext(ctx))
	})
}
