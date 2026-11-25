// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package tenantauth provides the shared tenant-aware API key authentication
// used by both the admin HTTP API and the MCP endpoint. Keeping the identity
// model and middleware here avoids duplicating sk_/ok_/tk_ semantics.
package tenantauth

import (
	"context"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
)

const (
	KeyTypeSuper    = "super"
	KeyTypeOperator = "operator"
	KeyTypeTenant   = "tenant"

	KeyPrefixSuper    = "sk_"
	KeyPrefixOperator = "ok_"
	KeyPrefixTenant   = "tk_"

	ImpersonateTenantHeader = "X-Tenant-Id"
)

// Identity is the authenticated caller. KeyID must be a non-sensitive
// identifier (a key record ID or a digest), never the plaintext key.
type Identity struct {
	TenantID      string
	KeyType       string
	KeyID         string
	Scopes        []string
	Impersonating bool
}

// IsGlobal reports whether the identity may act across tenants.
func (i Identity) IsGlobal() bool {
	return i.KeyType == KeyTypeSuper || i.KeyType == KeyTypeOperator
}

// NormalizeKeyType maps repository key types ("ok"/"tk"/"sk") and display
// names ("operator"/"tenant"/"super") to the canonical values.
func NormalizeKeyType(raw string) string {
	switch raw {
	case "sk", KeyTypeSuper:
		return KeyTypeSuper
	case "ok", KeyTypeOperator:
		return KeyTypeOperator
	case "tk", KeyTypeTenant:
		return KeyTypeTenant
	default:
		return ""
	}
}

type identityContextKey struct{}

func rawIdentity(ctx context.Context) Identity {
	if ctx == nil {
		return Identity{}
	}
	id, _ := ctx.Value(identityContextKey{}).(Identity)
	return id
}

// WithIdentity stores the identity and mirrors the tenant scope into the
// shared tenantctx keys used by the read path for data isolation.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id.TenantID != "" {
		ctx = tenantctx.WithTenantID(ctx, id.TenantID)
	}
	if id.Impersonating {
		ctx = tenantctx.WithImpersonating(ctx, true)
	}
	return context.WithValue(ctx, identityContextKey{}, id)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id := rawIdentity(ctx)
	if id.KeyType == "" {
		return Identity{}, false
	}
	return id, true
}

func WithKeyType(ctx context.Context, keyType string) context.Context {
	id := rawIdentity(ctx)
	id.KeyType = keyType
	return WithIdentity(ctx, id)
}

func KeyTypeFromContext(ctx context.Context) string {
	return rawIdentity(ctx).KeyType
}

func WithTenantID(ctx context.Context, tenantID string) context.Context {
	id := rawIdentity(ctx)
	id.TenantID = tenantID
	return WithIdentity(ctx, id)
}

func TenantIDFromContext(ctx context.Context) string {
	return tenantctx.TenantIDFromContext(ctx)
}

func KeyIDFromContext(ctx context.Context) string {
	return rawIdentity(ctx).KeyID
}

func ScopesFromContext(ctx context.Context) []string {
	return rawIdentity(ctx).Scopes
}

// Validator resolves a plaintext dynamic key to an identity. Implementations
// must not return the plaintext key in the Identity.
type Validator interface {
	Validate(ctx context.Context, key string) (Identity, bool)
}

type ValidatorFunc func(ctx context.Context, key string) (Identity, bool)

func (f ValidatorFunc) Validate(ctx context.Context, key string) (Identity, bool) {
	return f(ctx, key)
}
