// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/adminext/traceql"
)

// TestPlanToTagFilter covers the plan → tag-filter adapter in isolation.
// It is a pure-function test (no *Extension, no storage).
func TestPlanToTagFilter(t *testing.T) {
	tests := []struct {
		name string
		plan *traceql.ExecutionPlan
		want map[string]string
	}{
		{
			name: "nil plan",
			plan: nil,
			want: nil,
		},
		{
			name: "empty plan",
			plan: &traceql.ExecutionPlan{},
			want: nil,
		},
		{
			name: "service.name intrinsic projected to unscoped key",
			plan: &traceql.ExecutionPlan{ServiceName: "tapm-api"},
			want: map[string]string{"service.name": "tapm-api"},
		},
		{
			name: "all intrinsics projected",
			plan: &traceql.ExecutionPlan{
				ServiceName:   "svc",
				OperationName: "GET",
				SpanKind:      "server",
				Status:        "error",
			},
			want: map[string]string{
				"service.name": "svc",
				"name":         "GET",
				"kind":         "server",
				"status":       "error",
			},
		},
		{
			name: "custom attributes keep scoped key",
			plan: &traceql.ExecutionPlan{
				Tags: map[string]string{
					"span.http.method": "GET",
					"http.route":       "/api",
				},
			},
			want: map[string]string{
				"span.http.method": "GET",
				"http.route":       "/api",
			},
		},
		{
			name: "intrinsics + custom attrs combined",
			plan: &traceql.ExecutionPlan{
				ServiceName: "my-svc",
				Tags:        map[string]string{"span.http.method": "GET"},
			},
			want: map[string]string{
				"service.name":     "my-svc",
				"span.http.method": "GET",
			},
		},
		{
			name: "empty intrinsic values are skipped",
			plan: &traceql.ExecutionPlan{
				ServiceName:   "svc",
				OperationName: "", // empty → omitted
				SpanKind:      "",
			},
			want: map[string]string{"service.name": "svc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planToTagFilter(tt.plan)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestPlanToTagFilter_FromParse verifies the adapter against real parsed plans
// (end-to-end: TraceQL string → AST → Plan → tag filter).
func TestPlanToTagFilter_FromParse(t *testing.T) {
	tests := []struct {
		q    string
		want map[string]string
	}{
		{`{resource.service.name="tapm-api"}`, map[string]string{"service.name": "tapm-api"}},
		{`{service.name="x"}`, map[string]string{"service.name": "x"}},
		{`{name="GET"}`, map[string]string{"name": "GET"}},
		{`{status="error"}`, map[string]string{"status": "error"}},
		{`{span.http.method="GET"}`, map[string]string{"span.http.method": "GET"}},
		{`{resource.service.name="s" && span.http.method="GET"}`, map[string]string{
			"service.name": "s", "span.http.method": "GET"}},
		{`{}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			ast, err := traceql.Parse(tt.q)
			assert.NoError(t, err)
			got := planToTagFilter(traceql.Plan(ast))
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
