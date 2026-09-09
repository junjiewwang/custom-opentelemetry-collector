// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// echoTenantHandler writes the tenant ID from the request context.
func echoTenantHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(TenantIDFromContext(r.Context())))
	})
}

func TestTenantAuthMiddleware_StaticSuperKey(t *testing.T) {
	h := NewTenantAuthMiddleware([]string{"sk_superkey"}, nil, nil)(echoTenantHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "sk_superkey")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "", rr.Body.String(), "super key must be global (no tenant)")
}

func TestTenantAuthMiddleware_TenantKeyInjectsTenantID(t *testing.T) {
	validator := KeyValidatorFunc(func(_ context.Context, key string) (string, string, bool) {
		if key == "tk_valid" {
			return "tn-acme", "tk", true
		}
		return "", "", false
	})
	h := NewTenantAuthMiddleware([]string{"sk_super"}, validator, nil)(echoTenantHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "tk_valid")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "tn-acme", rr.Body.String())
}

func TestTenantAuthMiddleware_OperatorKeyIsGlobal(t *testing.T) {
	validator := KeyValidatorFunc(func(_ context.Context, key string) (string, string, bool) {
		if key == "ok_valid" {
			return "admin", "ok", true
		}
		return "", "", false
	})
	h := NewTenantAuthMiddleware([]string{"sk_super"}, validator, nil)(echoTenantHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "ok_valid")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "", rr.Body.String(), "operator key must be global (no tenant)")
}

func TestTenantAuthMiddleware_InvalidKeyRejected(t *testing.T) {
	validator := KeyValidatorFunc(func(_ context.Context, _ string) (string, string, bool) {
		return "", "", false
	})
	h := NewTenantAuthMiddleware([]string{"sk_super"}, validator, nil)(echoTenantHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "tk_bad")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestTenantAuthMiddleware_NoKeyRejected(t *testing.T) {
	h := NewTenantAuthMiddleware([]string{"sk_super"}, nil, nil)(echoTenantHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestTenantAuthMiddleware_UnprefixedStaticFallback(t *testing.T) {
	h := NewTenantAuthMiddleware([]string{"legacy-key"}, nil, nil)(echoTenantHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "legacy-key")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "", rr.Body.String())
}

func TestTenantAuthMiddleware_BearerToken(t *testing.T) {
	validator := KeyValidatorFunc(func(_ context.Context, key string) (string, string, bool) {
		if key == "tk_valid" {
			return "tn-acme", "tk", true
		}
		return "", "", false
	})
	h := NewTenantAuthMiddleware([]string{"sk_super"}, validator, nil)(echoTenantHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tk_valid")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "tn-acme", rr.Body.String())
}
