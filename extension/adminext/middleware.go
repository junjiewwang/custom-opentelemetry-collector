// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// loggingMiddleware logs HTTP requests.
func (e *Extension) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		e.logger.Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", wrapped.statusCode),
			zap.Duration("duration", time.Since(start)),
			zap.String("remote_addr", r.RemoteAddr),
		)
	})
}

// corsMiddleware handles CORS.
func (e *Extension) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Check if origin is allowed
		allowed := false
		for _, o := range e.config.CORS.AllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if e.config.CORS.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(e.config.CORS.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(e.config.CORS.AllowedHeaders, ", "))
			if e.config.CORS.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", string(rune(e.config.CORS.MaxAge)))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authMiddleware handles authentication.
func (e *Extension) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for OPTIONS requests (CORS preflight)
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		var authenticated bool

		switch e.config.Auth.Type {
		case "basic":
			authenticated = e.authenticateBasic(r)
		case "jwt":
			authenticated = e.authenticateJWT(r)
		case "api_key":
			authenticated = e.authenticateAPIKey(r)
		default:
			authenticated = false
		}

		if !authenticated {
			w.Header().Set("WWW-Authenticate", `Basic realm="Admin API"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authenticateBasic performs basic authentication.
func (e *Extension) authenticateBasic(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}

	if !strings.HasPrefix(auth, "Basic ") {
		return false
	}

	decoded, err := base64.StdEncoding.DecodeString(auth[6:])
	if err != nil {
		return false
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}

	return parts[0] == e.config.Auth.Basic.Username && parts[1] == e.config.Auth.Basic.Password
}

// authenticateJWT performs JWT authentication.
func (e *Extension) authenticateJWT(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}

	// TODO: Implement proper JWT validation
	// For now, just check if token is not empty
	token := auth[7:]
	return token != ""
}

// authenticateAPIKey performs API key authentication.
func (e *Extension) authenticateAPIKey(r *http.Request) bool {
	header := e.config.Auth.APIKey.Header
	if header == "" {
		header = "X-API-Key"
	}

	key := r.Header.Get(header)
	if key == "" {
		return false
	}

	for _, validKey := range e.config.Auth.APIKey.Keys {
		if key == validKey {
			return true
		}
	}

	return false
}

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
