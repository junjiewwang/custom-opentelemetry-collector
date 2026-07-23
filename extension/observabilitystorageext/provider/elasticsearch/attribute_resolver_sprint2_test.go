// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
)

// TestResolve_Sprint2_Intrinsics verifies the Sprint 2 intrinsic mappings:
// span:parentID → parentSpanId, trace:id → traceId.
func TestResolve_Sprint2_Intrinsics(t *testing.T) {
	r := &AttributeResolver{}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		// Span ID intrinsics (Sprint 1 + 2)
		{name: "span:id → spanId", raw: "span:id", want: FieldSpanID},
		{name: "span:spanID → spanId", raw: "span:spanID", want: FieldSpanID},
		{name: "id unscoped → spanId", raw: "id", want: FieldSpanID},

		// Parent ID intrinsics (Sprint 2)
		{name: "span:parentID → parentSpanId", raw: "span:parentID", want: FieldParentSpanID},
		{name: "span:parentId → parentSpanId", raw: "span:parentId", want: FieldParentSpanID},
		{name: "parentID unscoped → parentSpanId", raw: "parentID", want: FieldParentSpanID},

		// Trace ID intrinsics (Sprint 2)
		{name: "trace:id → traceID", raw: "trace:id", want: FieldTraceID},
		{name: "trace:traceID → traceID", raw: "trace:traceID", want: FieldTraceID},
		{name: "traceID unscoped → traceID", raw: "traceID", want: FieldTraceID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Resolve(tt.raw)
			if got.ESField != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.raw, got.ESField, tt.want)
			}
		})
	}
}
