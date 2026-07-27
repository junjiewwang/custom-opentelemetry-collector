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

// Middleware is decoupled from *Extension: each constructor takes only the
// dependencies it needs (logger, config). This makes middleware unit-testable
// without spinning up an Extension, and follows dependency inversion — the
// router injects these at wiring time.

// NewLoggingMiddleware returns middleware that logs each request at Debug level.
func NewLoggingMiddleware(logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)
			logger.Debug("HTTP request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", wrapped.statusCode),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// NewCORSMiddleware returns middleware that handles CORS for the given config.
func NewCORSMiddleware(cfg CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			allowed := false
			for _, o := range cfg.AllowedOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
			}

			// Handle preflight
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// NewAuthMiddleware returns middleware that enforces authentication per cfg.
// Health check, CORS preflight, and WebSocket /ws endpoints bypass admin auth
// (WebSocket connections cannot set custom headers; they use short-lived tokens).
func NewAuthMiddleware(cfg AuthConfig, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
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
			if isWebSocketRequest(r) && strings.HasSuffix(r.URL.Path, "/ws") {
				next.ServeHTTP(w, r)
				return
			}

			var authenticated bool
			switch cfg.Type {
			case "basic":
				authenticated = authenticateBasic(cfg, r)
			case "jwt":
				authenticated = authenticateJWT(cfg, logger, r)
			case "api_key":
				authenticated = authenticateAPIKey(cfg, r)
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
}

// NewTracingMiddleware is implemented in tracing.go (it depends on the OTel
// API packages kept there).

// isWebSocketRequest checks if the request is a WebSocket upgrade request.
func isWebSocketRequest(r *http.Request) bool {
	// Connection header may contain multiple values like "keep-alive, Upgrade"
	connectionHeader := strings.ToLower(r.Header.Get("Connection"))
	return strings.Contains(connectionHeader, "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// authenticateBasic performs basic authentication (constant-time comparison).
func authenticateBasic(cfg AuthConfig, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Basic ") {
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
	userOK := subtle.ConstantTimeCompare([]byte(parts[0]), []byte(cfg.Basic.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(cfg.Basic.Password)) == 1
	return userOK && passOK
}

// authenticateJWT validates an HS256 JWT bearer token against the configured
// secret/issuer/audience. See jwtValidator for the full guarantees.
func authenticateJWT(cfg AuthConfig, logger *zap.Logger, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(auth[7:])
	if token == "" {
		return false
	}
	if err := newJWTValidator(cfg.JWT).validate(token); err != nil {
		logger.Debug("JWT authentication rejected", zap.Error(err))
		return false
	}
	return true
}

// authenticateAPIKey performs API key authentication (constant-time comparison).
func authenticateAPIKey(cfg AuthConfig, r *http.Request) bool {
	header := cfg.APIKey.Header
	if header == "" {
		header = "X-API-Key"
	}
	key := r.Header.Get(header)
	if key == "" {
		return false
	}
	for _, validKey := range cfg.APIKey.Keys {
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
