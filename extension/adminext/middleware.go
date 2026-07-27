// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"bufio"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
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
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(e.config.CORS.MaxAge))
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

		// Skip auth for WebSocket endpoints (they use their own token-based auth)
		// WebSocket connections cannot set custom headers, so we use short-lived tokens
		if isWebSocketRequest(r) && strings.HasSuffix(r.URL.Path, "/ws") {
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

// isWebSocketRequest checks if the request is a WebSocket upgrade request.
func isWebSocketRequest(r *http.Request) bool {
	// Connection header may contain multiple values like "keep-alive, Upgrade"
	connectionHeader := strings.ToLower(r.Header.Get("Connection"))
	return strings.Contains(connectionHeader, "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
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

	// Constant-time comparison to avoid timing side channels on credentials.
	userOK := subtle.ConstantTimeCompare([]byte(parts[0]), []byte(e.config.Auth.Basic.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(e.config.Auth.Basic.Password)) == 1
	return userOK && passOK
}

// authenticateJWT validates an HS256 JWT bearer token against the configured
// secret/issuer/audience. A token that is missing, malformed, expired, or
// signed with the wrong key is rejected. Only HS256 is accepted (alg confusion
// is prevented in the validator).
func (e *Extension) authenticateJWT(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(auth[7:])
	if token == "" {
		return false
	}
	validator := newJWTValidator(e.config.Auth.JWT)
	if err := validator.validate(token); err != nil {
		e.logger.Debug("JWT authentication rejected", zap.Error(err))
		return false
	}
	return true
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

	// Constant-time comparison per candidate key to avoid timing side channels.
	for _, validKey := range e.config.Auth.APIKey.Keys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(validKey)) == 1 {
			return true
		}
	}

	return false
}

// responseWriter wraps http.ResponseWriter to capture status code.
//
// IMPORTANT: some handlers (e.g. gorilla/websocket upgrader) require additional
// interfaces like http.Hijacker/http.Flusher. When we wrap ResponseWriter we
// must continue to expose these capabilities.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}

func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := rw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
