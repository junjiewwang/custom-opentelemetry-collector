// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identityEcho() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			_, _ = w.Write([]byte("none"))
			return
		}
		_, _ = fmt.Fprintf(w, "%s|%s|%s|%t", id.KeyType, id.TenantID, id.KeyID, id.Impersonating)
	})
}

func serve(h http.Handler, key string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestMiddleware_StaticSuperKeyIsGlobal(t *testing.T) {
	h := New([]string{"sk_super"}, nil, nil)(identityEcho())
	rr := serve(h, "sk_super", nil)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "super||"+ShortDigest("sk_super")+"|false", rr.Body.String())
}

func TestMiddleware_TenantKeyHardBoundToTenant(t *testing.T) {
	validator := ValidatorFunc(func(_ context.Context, key string) (Identity, bool) {
		if key != "tk_valid" {
			return Identity{}, false
		}
		return Identity{TenantID: "tn-own", KeyType: "tk", KeyID: "key-1"}, true
	})
	h := New([]string{"sk_super"}, validator, nil)(identityEcho())
	rr := serve(h, "tk_valid", map[string]string{ImpersonateTenantHeader: "tn-other"})

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "tenant|tn-own|key-1|false", rr.Body.String(),
		"tk_ keys must ignore the impersonation header")
}

func TestMiddleware_OperatorKeyIsGlobal(t *testing.T) {
	validator := ValidatorFunc(func(_ context.Context, key string) (Identity, bool) {
		return Identity{TenantID: "tn-leak", KeyType: "ok", KeyID: "key-2"}, true
	})
	h := New(nil, validator, nil)(identityEcho())
	rr := serve(h, "ok_valid", nil)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "operator||key-2|false", rr.Body.String(),
		"operator keys must not inherit a tenant scope from the validator")
}

func TestMiddleware_SuperKeyImpersonation(t *testing.T) {
	h := New([]string{"sk_super"}, nil, nil)(identityEcho())
	rr := serve(h, "sk_super", map[string]string{ImpersonateTenantHeader: "tn-a"})

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "super|tn-a|"+ShortDigest("sk_super")+"|true", rr.Body.String())
}

func TestMiddleware_RejectsMissingAndInvalidKeys(t *testing.T) {
	validator := ValidatorFunc(func(_ context.Context, _ string) (Identity, bool) {
		return Identity{}, false
	})
	h := New([]string{"sk_super"}, validator, nil)(identityEcho())

	assert.Equal(t, http.StatusUnauthorized, serve(h, "", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, serve(h, "tk_bad", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, serve(h, "sk_wrong", nil).Code)
}

func TestMiddleware_TenantKeyWithoutTenantRejected(t *testing.T) {
	validator := ValidatorFunc(func(_ context.Context, _ string) (Identity, bool) {
		return Identity{KeyType: "tk", KeyID: "key-3"}, true
	})
	h := New(nil, validator, nil)(identityEcho())
	assert.Equal(t, http.StatusUnauthorized, serve(h, "tk_no_tenant", nil).Code)
}

func TestIdentityFromContextRequiresKeyType(t *testing.T) {
	_, ok := IdentityFromContext(context.Background())
	assert.False(t, ok)
	_, ok = IdentityFromContext(nil)
	assert.False(t, ok)
}

// R1: the Bearer scheme must remain case-insensitive for MCP clients.
func TestMiddleware_BearerSchemeCaseInsensitive(t *testing.T) {
	h := New([]string{"sk_super"}, nil, nil)(identityEcho())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer sk_super")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "super|")
}

// R2: MCP keeps Bearer-first priority; admin keeps X-API-Key-first.
func TestMiddleware_HeaderPriorityOptions(t *testing.T) {
	send := func(opts ...Option) int {
		h := New([]string{"sk_super"}, nil, nil, opts...)(identityEcho())
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("X-API-Key", "bogus-key")
		req.Header.Set("Authorization", "Bearer sk_super")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	assert.Equal(t, http.StatusUnauthorized, send(), "admin default keeps X-API-Key priority")
	assert.Equal(t, http.StatusOK, send(WithBearerPriority()), "MCP keeps Bearer priority")
}

// R3: MCP error bodies/status codes are preserved; admin keeps plain 401.
func TestMiddleware_MCPErrorResponses(t *testing.T) {
	mcp := New([]string{"sk_super"}, nil, nil, WithMCPErrorResponses())(identityEcho())

	missing := serve(mcp, "", nil)
	assert.Equal(t, http.StatusUnauthorized, missing.Code)
	assert.Equal(t, "application/json", missing.Header().Get("Content-Type"))
	assert.Contains(t, missing.Body.String(), `"error":"unauthorized"`)

	invalid := serve(mcp, "bogus", nil)
	assert.Equal(t, http.StatusForbidden, invalid.Code)
	assert.Contains(t, invalid.Body.String(), `"error":"forbidden"`)

	admin := New([]string{"sk_super"}, nil, nil)(identityEcho())
	adminInvalid := serve(admin, "bogus", nil)
	assert.Equal(t, http.StatusUnauthorized, adminInvalid.Code)
	assert.NotContains(t, adminInvalid.Body.String(), `"error"`)
}
