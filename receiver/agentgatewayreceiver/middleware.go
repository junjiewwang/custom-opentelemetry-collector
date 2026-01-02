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

// tokenAuthMiddleware validates tokens for protected endpoints.
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

		if authHeader == "" {
			r.logger.Debug("Missing authorization header",
				zap.String("path", req.URL.Path),
				zap.String("header", headerName),
			)
			http.Error(w, "Unauthorized: missing authorization header", http.StatusUnauthorized)
			return
		}

		// Extract token value
		token := authHeader
		if headerPrefix != "" && strings.HasPrefix(authHeader, headerPrefix) {
			token = strings.TrimPrefix(authHeader, headerPrefix)
		}

		if token == "" {
			r.logger.Debug("Empty token after prefix removal",
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

		// Extract agent ID from header if present
		agentID := req.Header.Get("X-Agent-ID")
		if agentID != "" {
			ctx = context.WithValue(ctx, ContextKeyAgentID, agentID)
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
