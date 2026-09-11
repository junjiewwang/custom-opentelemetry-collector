// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
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

// echoIdentityHandler writes "key_type|tenant_id|impersonating" for asserting
// the actor/scope separation of impersonation.
func echoIdentityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s|%s|%t",
			KeyTypeFromContext(r.Context()),
			TenantIDFromContext(r.Context()),
			tenantctx.ImpersonatingFromContext(r.Context()),
		)
	})
}

func TestTenantAuthMiddleware_ImpersonateAsTenant(t *testing.T) {
	h := NewTenantAuthMiddleware([]string{"sk_super"}, nil, nil)(echoIdentityHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "sk_super")
	req.Header.Set("X-Tenant-Id", "tn-acme")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "super|tn-acme|true", rr.Body.String())
}

func TestTenantAuthMiddleware_TenantKeyIgnoresImpersonation(t *testing.T) {
	validator := KeyValidatorFunc(func(_ context.Context, key string) (string, string, bool) {
		if key == "tk_valid" {
			return "tn-own", "tk", true
		}
		return "", "", false
	})
	h := NewTenantAuthMiddleware([]string{"sk_super"}, validator, nil)(echoIdentityHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "tk_valid")
	req.Header.Set("X-Tenant-Id", "tn-other")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "tenant|tn-own|false", rr.Body.String(),
		"tk_ key must ignore the impersonation header and stay bound to its own tenant")
}

func TestRequireAdminMiddleware_KeyTypeAware(t *testing.T) {
	allow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Genuine tenant key → forbidden.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithTenantID(WithKeyType(req.Context(), KeyTypeTenant), "tn"))
	rr := httptest.NewRecorder()
	requireAdminMiddleware(allow).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	// Impersonating super key (tenant ID present, key type super) → allowed.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(tenantctx.WithImpersonating(WithTenantID(WithKeyType(req.Context(), KeyTypeSuper), "tn"), true))
	rr = httptest.NewRecorder()
	requireAdminMiddleware(allow).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
