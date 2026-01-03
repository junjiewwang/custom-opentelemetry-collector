// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func newTestExtension(_ *testing.T, config *Config) *Extension {
	if config == nil {
		config = createDefaultConfig()
	}
	return &Extension{
		config: config,
		logger: zap.NewNop(),
	}
}

func TestLoggingMiddleware(t *testing.T) {
	ext := newTestExtension(t, nil)

	handler := ext.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "OK", rr.Body.String())
}

func TestLoggingMiddleware_StatusCode(t *testing.T) {
	ext := newTestExtension(t, nil)

	handler := ext.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/notfound", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	config := createDefaultConfig()
	config.CORS.Enabled = true
	config.CORS.AllowedOrigins = []string{"http://localhost:3000"}
	ext := newTestExtension(t, config)

	handler := ext.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, "http://localhost:3000", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddleware_WildcardOrigin(t *testing.T) {
	config := createDefaultConfig()
	config.CORS.Enabled = true
	config.CORS.AllowedOrigins = []string{"*"}
	ext := newTestExtension(t, config)

	handler := ext.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://any-origin.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, "http://any-origin.com", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	config := createDefaultConfig()
	config.CORS.Enabled = true
	config.CORS.AllowedOrigins = []string{"http://localhost:3000"}
	ext := newTestExtension(t, config)

	handler := ext.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://evil.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddleware_Preflight(t *testing.T) {
	config := createDefaultConfig()
	config.CORS.Enabled = true
	config.CORS.AllowedOrigins = []string{"*"}
	config.CORS.AllowedMethods = []string{"GET", "POST"}
	config.CORS.AllowedHeaders = []string{"Authorization", "Content-Type"}
	ext := newTestExtension(t, config)

	handler := ext.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called for preflight")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "GET")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORSMiddleware_AllowCredentials(t *testing.T) {
	config := createDefaultConfig()
	config.CORS.Enabled = true
	config.CORS.AllowedOrigins = []string{"*"}
	config.CORS.AllowCredentials = true
	ext := newTestExtension(t, config)

	handler := ext.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"))
}

func TestAuthMiddleware_SkipHealthCheck(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "api_key"
	config.Auth.APIKey.Keys = []string{"secret-key"}
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_SkipOptions(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "api_key"
	config.Auth.APIKey.Keys = []string{"secret-key"}
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_BasicAuth_Success(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "basic"
	config.Auth.Basic.Username = "admin"
	config.Auth.Basic.Password = "secret"
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_BasicAuth_Failure(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "basic"
	config.Auth.Basic.Username = "admin"
	config.Auth.Basic.Password = "secret"
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		header string
	}{
		{"no auth header", ""},
		{"wrong prefix", "Bearer token"},
		{"invalid base64", "Basic !!!invalid!!!"},
		{"wrong credentials", "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:wrong"))},
		{"malformed credentials", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestAuthMiddleware_JWTAuth_Success(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "jwt"
	config.Auth.JWT.Secret = "my-secret"
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_JWTAuth_Failure(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "jwt"
	config.Auth.JWT.Secret = "my-secret"
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		header string
	}{
		{"no auth header", ""},
		{"wrong prefix", "Basic token"},
		{"empty token", "Bearer "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestAuthMiddleware_APIKeyAuth_Success(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "api_key"
	config.Auth.APIKey.Header = "X-API-Key"
	config.Auth.APIKey.Keys = []string{"key1", "key2"}
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test with first key
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-API-Key", "key1")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Test with second key
	req = httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-API-Key", "key2")
	rr = httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_APIKeyAuth_Failure(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "api_key"
	config.Auth.APIKey.Header = "X-API-Key"
	config.Auth.APIKey.Keys = []string{"valid-key"}
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name  string
		key   string
		empty bool
	}{
		{"no key", "", true},
		{"wrong key", "invalid-key", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			if !tt.empty {
				req.Header.Set("X-API-Key", tt.key)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestAuthMiddleware_APIKeyAuth_DefaultHeader(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "api_key"
	config.Auth.APIKey.Header = "" // Empty, should use default
	config.Auth.APIKey.Keys = []string{"valid-key"}
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-API-Key", "valid-key") // Default header name
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthMiddleware_UnknownAuthType(t *testing.T) {
	config := createDefaultConfig()
	config.Auth.Enabled = true
	config.Auth.Type = "unknown"
	ext := newTestExtension(t, config)

	handler := ext.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

type hijackableResponseWriter struct {
	header http.Header
}

func (h *hijackableResponseWriter) Header() http.Header {
	if h.header == nil {
		h.header = make(http.Header)
	}
	return h.header
}

func (h *hijackableResponseWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (h *hijackableResponseWriter) WriteHeader(int) {}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func TestResponseWriter(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, statusCode: http.StatusOK}

	// Default status code
	assert.Equal(t, http.StatusOK, rw.statusCode)

	// Write header
	rw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rw.statusCode)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestResponseWriter_HijackerPassthrough(t *testing.T) {
	h := &hijackableResponseWriter{}
	rw := &responseWriter{ResponseWriter: h, statusCode: http.StatusOK}

	hj, ok := any(rw).(http.Hijacker)
	assert.True(t, ok)

	conn, _, err := hj.Hijack()
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	_ = conn.Close()
}
