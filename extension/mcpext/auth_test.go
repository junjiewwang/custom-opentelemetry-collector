// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "bearer token",
			headers:  map[string]string{"Authorization": "Bearer sk-test-key"},
			expected: "sk-test-key",
		},
		{
			name:     "bearer token case insensitive",
			headers:  map[string]string{"Authorization": "bearer sk-test-key"},
			expected: "sk-test-key",
		},
		{
			name:     "x-api-key header",
			headers:  map[string]string{"X-API-Key": "sk-test-key"},
			expected: "sk-test-key",
		},
		{
			name:     "bearer takes priority",
			headers:  map[string]string{"Authorization": "Bearer bearer-key", "X-API-Key": "header-key"},
			expected: "bearer-key",
		},
		{
			name:     "no auth headers",
			headers:  map[string]string{},
			expected: "",
		},
		{
			name:     "invalid authorization format",
			headers:  map[string]string{"Authorization": "Basic abc123"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			result := extractAPIKey(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAPIKeyAuthMiddleware(t *testing.T) {
	logger := zap.NewNop()
	validKeys := []string{"sk-valid-key-1", "sk-valid-key-2"}

	// Create a simple handler that returns 200
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	middleware := newAPIKeyAuthMiddleware(validKeys, logger)
	handler := middleware(okHandler)

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
	}{
		{
			name:           "valid bearer key",
			headers:        map[string]string{"Authorization": "Bearer sk-valid-key-1"},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid x-api-key",
			headers:        map[string]string{"X-API-Key": "sk-valid-key-2"},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid key",
			headers:        map[string]string{"Authorization": "Bearer sk-invalid"},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "no key",
			headers:        map[string]string{},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}
