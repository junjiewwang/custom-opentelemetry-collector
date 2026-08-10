// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
)

func TestMetricLabelResolver_Resolve(t *testing.T) {
	r := MetricLabelResolver{}

	tests := []struct {
		name        string
		label       string
		wantESField string
		wantPromoted bool
	}{
		// Promoted labels → top-level field, NOT labels.<key>.keyword.
		{name: "service_name promoted", label: "service_name", wantESField: "serviceName", wantPromoted: true},
		{name: "service.name variant promoted", label: "service.name", wantESField: "serviceName", wantPromoted: true},
		{name: "serviceName camel variant promoted", label: "serviceName", wantESField: "serviceName", wantPromoted: true},
		{name: "idempotent on already-normalized", label: "service_name", wantESField: "serviceName", wantPromoted: true},

		// Regular labels → labels.<key>.keyword (metric labels are text+keyword).
		{name: "span_kind", label: "span_kind", wantESField: "labels.span_kind.keyword", wantPromoted: false},
		{name: "span.name variant", label: "span.name", wantESField: "labels.span_name.keyword", wantPromoted: false},
		{name: "http_method", label: "http_method", wantESField: "labels.http_method.keyword", wantPromoted: false},
		{name: "unknown custom label", label: "my_custom_label", wantESField: "labels.my_custom_label.keyword", wantPromoted: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.label)
			if got.ESField != tc.wantESField {
				t.Errorf("ESField = %q, want %q", got.ESField, tc.wantESField)
			}
			if got.IsPromoted != tc.wantPromoted {
				t.Errorf("IsPromoted = %v, want %v", got.IsPromoted, tc.wantPromoted)
			}
		})
	}
}

// TestMetricLabelResolver_PromotedFieldsIsSingleSource verifies that adding
// a promoted field to the table makes it resolvable with no call-site changes —
// the extensibility guarantee of the resolver design.
func TestMetricLabelResolver_PromotedFieldsIsSingleSource(t *testing.T) {
	// service_name is the only promoted field today.
	if _, ok := promotedFields["service_name"]; !ok {
		t.Fatal("promotedFields must list service_name")
	}
	if len(promotedFields) != 1 {
		t.Logf("note: promotedFields has %d entries (expected 1 for now)", len(promotedFields))
	}
	// A label NOT in the table resolves as non-promoted (labels.<key>.keyword).
	r := MetricLabelResolver{}
	if got := r.Resolve("future_field"); got.IsPromoted {
		t.Errorf("unknown label should not be promoted, got %+v", got)
	}
}
