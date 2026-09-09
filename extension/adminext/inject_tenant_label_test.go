// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"strings"
	"testing"
)

// TestInjectTenantLabel is the security-critical test for tenant isolation on
// the native VM PromQL path: the tenant_id matcher must be injected into every
// vector selector, otherwise tenant-scoped queries silently leak across tenants.
func TestInjectTenantLabel(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"simple selector", "rate(http_requests_total[5m])"},
		{"aggregation", "sum(rate(m[5m]))"},
		{"histogram quantile", `histogram_quantile(0.99, sum by (le) (rate(http_dur_bucket[5m])))`},
		{"bare selector with labels", `m{service_name="svc"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectTenantLabel(tt.expr, "tn-1")
			if !strings.Contains(got, `tenant_id="tn-1"`) {
				t.Fatalf("expected tenant_id matcher, got %q", got)
			}
		})
	}
}

func TestInjectTenantLabel_NoOp(t *testing.T) {
	// Empty tenantID (global/admin) must not alter the expression.
	if got := injectTenantLabel("rate(m[5m])", ""); got != "rate(m[5m])" {
		t.Fatalf("empty tenantID should be a no-op, got %q", got)
	}

	// An unparseable expression must be returned unchanged (best effort).
	if got := injectTenantLabel("(", "tn-1"); got != "(" {
		t.Fatalf("invalid expr should be a no-op, got %q", got)
	}
}
