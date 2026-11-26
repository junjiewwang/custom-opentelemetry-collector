// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
)

// TestWithTenantFilter is the security-critical test for ES tenant isolation:
// a tenantId term filter must be ANDed onto every query so a tenant never sees
// another tenant's documents.
func TestWithTenantFilter(t *testing.T) {
	// Non-empty query → wrapped with bool.filter containing the tenant term.
	q := withTenantFilter(map[string]any{"match_all": map[string]any{}}, "tn-1")
	boolQ, ok := q["bool"].(map[string]any)
	if !ok {
		t.Fatalf("expected bool wrapper, got %T", q)
	}
	filters, ok := boolQ["filter"].([]any)
	if !ok || len(filters) != 2 {
		t.Fatalf("expected 2 filters (query + tenant term), got %#v", boolQ["filter"])
	}
	term, ok := filters[1].(map[string]any)
	if !ok {
		t.Fatalf("expected term filter, got %T", filters[1])
	}
	inner, ok := term["term"].(map[string]any)
	if !ok || inner["tenantId.keyword"] != "tn-1" {
		t.Fatalf("expected tenantId.keyword term, got %#v", term)
	}

	// Empty query → bare tenant filter (match-all becomes tenant filter).
	q = withTenantFilter(map[string]any{}, "tn-1")
	boolQ = q["bool"].(map[string]any)
	filters = boolQ["filter"].([]any)
	if len(filters) != 1 {
		t.Fatalf("expected 1 filter for empty query, got %#v", filters)
	}
}

// TestSearchTenantID verifies the abstraction-layer fallback: reader methods
// whose local query types have no TenantID field still get isolated from ctx,
// while an explicit SearchRequest.TenantID always wins.
func TestSearchTenantID(t *testing.T) {
	tenantCtx := tenantctx.WithTenantID(context.Background(), "tn-ctx")

	cases := []struct {
		name string
		ctx  context.Context
		req  *SearchRequest
		want string
	}{
		{"explicit wins over ctx", tenantCtx, &SearchRequest{TenantID: "tn-explicit"}, "tn-explicit"},
		{"ctx fallback", tenantCtx, &SearchRequest{}, "tn-ctx"},
		{"background stays global", context.Background(), &SearchRequest{}, ""},
		{"background with explicit", context.Background(), &SearchRequest{TenantID: "tn-explicit"}, "tn-explicit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchTenantID(tc.ctx, tc.req); got != tc.want {
				t.Fatalf("searchTenantID=%q, want %q", got, tc.want)
			}
		})
	}
}
