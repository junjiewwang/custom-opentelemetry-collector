// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// newAPIKeyAuthMiddleware creates an HTTP middleware that validates API keys.
// API key can be provided via:
// - Authorization header: "Bearer <api_key>"
// - X-API-Key header: "<api_key>"
func newAPIKeyAuthMiddleware(validKeys []string, logger *zap.Logger) func(http.Handler) http.Handler {
	// Build a set for O(1) lookup
	keySet := make(map[string]struct{}, len(validKeys))
	for _, key := range validKeys {
		keySet[key] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := extractAPIKey(r)
			if apiKey == "" {
				logger.Warn("MCP request without API key",
					zap.String("remote_addr", r.RemoteAddr),
					zap.String("path", r.URL.Path),
				)
				http.Error(w, `{"error":"unauthorized","message":"API key is required. Use 'Authorization: Bearer <key>' or 'X-API-Key: <key>' header."}`, http.StatusUnauthorized)
				return
			}

			if _, valid := keySet[apiKey]; !valid {
				logger.Warn("MCP request with invalid API key",
					zap.String("remote_addr", r.RemoteAddr),
					zap.String("path", r.URL.Path),
				)
				http.Error(w, `{"error":"forbidden","message":"Invalid API key."}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractAPIKey extracts the API key from the request headers.
func extractAPIKey(r *http.Request) string {
	// Try Authorization header (Bearer token)
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}

	// Try X-API-Key header
	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "" {
		return apiKey
	}

	return ""
}
