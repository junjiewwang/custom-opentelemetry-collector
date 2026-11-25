// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// The tenant-aware authentication implementation lives in the shared
// tenantauth package so the MCP endpoint can reuse the exact same
// sk_/ok_/tk_ and impersonation semantics. The names below are kept for
// compatibility with existing admin handlers, middleware and tests.
const (
	KeyTypeSuper    = tenantauth.KeyTypeSuper
	KeyTypeOperator = tenantauth.KeyTypeOperator
	KeyTypeTenant   = tenantauth.KeyTypeTenant
)

func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return tenantauth.WithTenantID(ctx, tenantID)
}

func TenantIDFromContext(ctx context.Context) string {
	return tenantauth.TenantIDFromContext(ctx)
}

func WithKeyType(ctx context.Context, keyType string) context.Context {
	return tenantauth.WithKeyType(ctx, keyType)
}

func KeyTypeFromContext(ctx context.Context) string {
	return tenantauth.KeyTypeFromContext(ctx)
}

// KeyValidator is the legacy narrow interface retained for compatibility.
// New callers should use tenantauth.Validator, which also carries KeyID and
// Scopes for auditing.
type KeyValidator interface {
	ValidateAPIKey(ctx context.Context, key string) (tenantID, keyType string, ok bool)
}

type KeyValidatorFunc func(ctx context.Context, key string) (tenantID, keyType string, ok bool)

func (f KeyValidatorFunc) ValidateAPIKey(ctx context.Context, key string) (string, string, bool) {
	return f(ctx, key)
}

func NewTenantAuthMiddleware(staticKeys []string, validator KeyValidator, logger *zap.Logger) func(http.Handler) http.Handler {
	return NewTenantAuthMiddlewareWithIdentity(staticKeys, adaptKeyValidator(validator), logger)
}

// NewTenantAuthMiddlewareWithIdentity wires the shared middleware with a
// validator that can supply KeyID and Scopes.
func NewTenantAuthMiddlewareWithIdentity(staticKeys []string, validator tenantauth.Validator, logger *zap.Logger) func(http.Handler) http.Handler {
	return tenantauth.New(staticKeys, validator, logger)
}

func adaptKeyValidator(validator KeyValidator) tenantauth.Validator {
	if validator == nil {
		return nil
	}
	return tenantauth.ValidatorFunc(func(ctx context.Context, key string) (tenantauth.Identity, bool) {
		tenantID, keyType, ok := validator.ValidateAPIKey(ctx, key)
		if !ok {
			return tenantauth.Identity{}, false
		}
		return tenantauth.Identity{
			TenantID: tenantID,
			KeyType:  tenantauth.NormalizeKeyType(keyType),
		}, true
	})
}

// requireAdminMiddleware rejects requests that carry a tenant identity (tk_
// keys). Management API endpoints (apps/tenants/keys/...) are admin-only; a
// tenant key must be limited to the read-path query endpoints (Prometheus /
// Tempo / Loki), where TenantID is used for data isolation instead.
func requireAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Key-type-aware: block only genuine tenant (tk_) keys. Admin/operator
		// keys stay authorized even when impersonating a tenant (which injects a
		// tenant ID into the context but keeps the key type super/operator).
		if KeyTypeFromContext(r.Context()) == KeyTypeTenant {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
