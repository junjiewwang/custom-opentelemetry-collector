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
