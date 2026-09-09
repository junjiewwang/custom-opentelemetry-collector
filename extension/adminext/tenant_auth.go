// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
	"go.uber.org/zap"
)

// API key type prefixes (read-path credentials). The super key (sk_) is static
// config; ok_ (operator) and tk_ (tenant) keys are validated dynamically.
const (
	apiKeyPrefixSuper    = "sk_"
	apiKeyPrefixOperator = "ok_"
	apiKeyPrefixTenant   = "tk_"
)

// KeyValidator is the narrow, consumer-side interface the tenant auth
// middleware needs to validate ok_/tk_ dynamic keys. It is satisfied by an
// adapter over tenantmanager.APIKeyService, keeping adminext decoupled from the
// tenantmanager package (ISP / dependency inversion).
type KeyValidator interface {
	// ValidateAPIKey returns (tenantID, keyType, ok). keyType is "ok" or "tk".
	ValidateAPIKey(ctx context.Context, key string) (tenantID, keyType string, ok bool)
}

// KeyValidatorFunc adapts a function to KeyValidator.
type KeyValidatorFunc func(ctx context.Context, key string) (tenantID, keyType string, ok bool)

// ValidateAPIKey implements KeyValidator.
func (f KeyValidatorFunc) ValidateAPIKey(ctx context.Context, key string) (string, string, bool) {
	return f(ctx, key)
}

// WithTenantID injects the tenant ID into the context. Empty means global.
// Delegates to the shared tenantctx key so storage providers can read the same
// tenant scope from the request context.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return tenantctx.WithTenantID(ctx, tenantID)
}

// TenantIDFromContext returns the authenticated tenant ID, or "" for global
// (super/operator) requests.
func TenantIDFromContext(ctx context.Context) string {
	return tenantctx.TenantIDFromContext(ctx)
}

// keyTypeContextKey is the context key for the authenticated key type.
type keyTypeContextKey struct{}

// Key type values injected by the tenant auth middleware, exposed to the
// frontend via /auth/me so it can render admin vs tenant views.
const (
	KeyTypeSuper    = "super"
	KeyTypeOperator = "operator"
	KeyTypeTenant   = "tenant"
)

// WithKeyType injects the key type into the context.
func WithKeyType(ctx context.Context, keyType string) context.Context {
	return context.WithValue(ctx, keyTypeContextKey{}, keyType)
}

// KeyTypeFromContext returns the authenticated key type ("super" / "operator" /
// "tenant"), or "" when not injected.
func KeyTypeFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(keyTypeContextKey{}).(string); ok {
		return v
	}
	return ""
}

// NewTenantAuthMiddleware returns middleware that authenticates via prefixed
// API keys:
//
//	sk_ → static super key (global, no TenantID)
//	ok_ → dynamic operator key (global, no TenantID)
//	tk_ → dynamic tenant key (injects TenantID into context)
//	<unprefixed> → backward-compatible static match, then dynamic fallback
//
// staticKeys are the configured super keys (constant-time compared). validator
// may be nil (dynamic keys are then rejected until wired).
func NewTenantAuthMiddleware(staticKeys []string, validator KeyValidator, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for health / CORS preflight / WebSocket (mirrors
			// NewAuthMiddleware so this is a drop-in replacement for api_key).
			if r.URL.Path == "/health" ||
				r.Method == http.MethodOptions ||
				(isWebSocketRequest(r) && strings.HasSuffix(r.URL.Path, "/ws")) {
				next.ServeHTTP(w, r)
				return
			}

			key := extractAPIKey(r)
			if key == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			switch {
			case strings.HasPrefix(key, apiKeyPrefixSuper):
				if isStaticAPIKey(key, staticKeys) {
					next.ServeHTTP(w, r.WithContext(WithKeyType(r.Context(), KeyTypeSuper))) // global
					return
				}

			case strings.HasPrefix(key, apiKeyPrefixOperator), strings.HasPrefix(key, apiKeyPrefixTenant):
				if validator != nil {
					if ctx, ok := authenticateDynamic(r, key, validator); ok {
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}

			default:
				// Backward compatibility: unprefixed static key first.
				if isStaticAPIKey(key, staticKeys) {
					next.ServeHTTP(w, r.WithContext(WithKeyType(r.Context(), KeyTypeSuper)))
					return
				}
				if validator != nil {
					if ctx, ok := authenticateDynamic(r, key, validator); ok {
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		})
	}
}

// authenticateDynamic validates an ok_/tk_ key and returns a context with
// TenantID injected for tk_ keys (ok_ keys are global).
func authenticateDynamic(r *http.Request, key string, validator KeyValidator) (context.Context, bool) {
	tenantID, keyType, ok := validator.ValidateAPIKey(r.Context(), key)
	if !ok {
		return nil, false
	}
	ctx := r.Context()
	switch keyType {
	case "tk":
		ctx = WithTenantID(ctx, tenantID)
		ctx = WithKeyType(ctx, KeyTypeTenant)
	case "ok":
		ctx = WithKeyType(ctx, KeyTypeOperator)
	}
	return ctx, true
}

// extractAPIKey pulls the key from X-API-Key or Authorization: Bearer.
func extractAPIKey(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

// isStaticAPIKey reports whether key matches a configured static super key.
func isStaticAPIKey(key string, staticKeys []string) bool {
	for _, k := range staticKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
			return true
		}
	}
	return false
}

// requireAdminMiddleware rejects requests that carry a tenant identity (tk_
// keys). Management API endpoints (apps/tenants/keys/...) are admin-only; a
// tenant key must be limited to the read-path query endpoints (Prometheus /
// Tempo / Loki), where TenantID is used for data isolation instead.
func requireAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TenantIDFromContext(r.Context()) != "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
