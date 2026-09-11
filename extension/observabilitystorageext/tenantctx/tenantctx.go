// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package tenantctx carries the authenticated tenant ID through the request
// context. The admin extension's tenant auth middleware writes it (via
// WithTenantID); storage providers read it (via TenantIDFromContext) to scope
// enumeration queries — metric names, label names/values, service names, and
// log fields — to a single tenant.
//
// It lives in its own leaf package (not the observabilitystorageext root)
// because provider/elasticsearch cannot import the root package without an
// import cycle, but both it and provider/victoriametrics can import this one.
package tenantctx

import "context"

// tenantIDContextKey is the context key for the authenticated tenant ID.
type tenantIDContextKey struct{}

// WithTenantID injects the tenant ID into the context. Empty means global
// (super/operator), i.e. no tenant scoping.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDContextKey{}, tenantID)
}

// TenantIDFromContext returns the authenticated tenant ID, or "" for global
// (super/operator) requests.
func TenantIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// AccountResolver maps a tenant ID to a VictoriaMetrics account ID for native
// multitenancy (vm-cluster). It is injected by the extension layer, which owns
// the tenant→account mapping (tenantmanager.ResolveAccountID). A nil resolver
// means account scoping is unwired; an empty tenantID always resolves to
// account 0 (the default / global account).
//
// It lives here (not in provider/victoriametrics) because both the VM provider
// and the extension layer must name the type, and the extension cannot import
// the provider without an import cycle — the same reason tenantIDContextKey
// lives in this leaf package.
type AccountResolver func(ctx context.Context, tenantID string) (uint32, error)

// impersonatingContextKey is the context key for the impersonation flag.
type impersonatingContextKey struct{}

// WithImpersonating marks the request as impersonating another tenant. The auth
// middleware sets this when an admin/operator key supplies an X-Tenant-Id scope;
// storage providers and audit paths read it via ImpersonatingFromContext to
// distinguish a real tenant request from an admin acting on a tenant's behalf.
func WithImpersonating(ctx context.Context, impersonating bool) context.Context {
	return context.WithValue(ctx, impersonatingContextKey{}, impersonating)
}

// ImpersonatingFromContext reports whether the request is impersonating another
// tenant (false for unmarked requests and for genuine tenant-key requests).
func ImpersonatingFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(impersonatingContextKey{}).(bool)
	return v
}
