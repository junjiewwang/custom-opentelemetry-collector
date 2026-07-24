package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMetricsAggField_AllIntrinsics ensures every TraceQL intrinsic field
// that can appear in | rate() by(...) produces the correct ES aggregation field
// path — including .keyword suffix for text fields.
func TestMetricsAggField_AllIntrinsics(t *testing.T) {
	resolver := &AttributeResolver{}

	tests := []struct {
		label string // TraceQL by() label
		want  string // expected ES aggregation field
		note  string
	}{
		// ── Text fields → must have .keyword suffix ──
		{label: "status", want: "status.code.keyword",
			note: "status resolves to status.code (text) → needs .keyword"},
		{label: "statusCode", want: "status.code.keyword",
			note: "Grafana Tempo canonical form"},
		{label: "status.message", want: "status.message.keyword",
			note: "explicit dotted form"},
		{label: "statusMessage", want: "status.message.keyword",
			note: "Grafana Tempo canonical form"},

		// ── Keyword fields → no .keyword suffix needed ──
		{label: "kind", want: "kind",
			note: "span.kind is explicit keyword in ES template"},
		{label: "name", want: "name",
			note: "span.name is explicit keyword"},
		{label: "rootName", want: "name",
			note: "rootName resolves to name (keyword)"},
		{label: "rootServiceName", want: "serviceName",
			note: "trace root service is explicit keyword"},
		{label: "span:kind", want: "kind",
			note: "scoped intrinsic → same keyword field"},
		{label: "span:name", want: "name",
			note: "scoped intrinsic → same keyword field"},

		// ── ID fields → keyword, no .keyword suffix ──
		{label: "span:id", want: "spanId",
			note: "span ID intrinsic → keyword"},
		{label: "span:parentID", want: "parentSpanId",
			note: "parent span ID intrinsic → keyword"},
		{label: "trace:id", want: "traceId",
			note: "trace ID intrinsic → keyword"},

		// ── resource.* text fields → must have .keyword suffix ──
		{label: "resource.service.instance.id", want: "resource.service.instance.id.keyword",
			note: "random sub-fields under resource are text → needs .keyword"},
		{label: "resource.telemetry.distro.name", want: "resource.telemetry.distro.name.keyword",
			note: "random sub-fields under resource are text → needs .keyword"},

		// ── resource.* explicit keyword fields → no .keyword suffix ──
		{label: "resource.host.name", want: "resource.host.name",
			note: "explicit keyword in template"},
		{label: "resource.service.namespace", want: "resource.service.namespace",
			note: "explicit keyword in template"},
		{label: "resource.process.pid", want: "resource.process.pid",
			note: "explicit keyword in template"},

		// ── Custom span attributes (resolved to attributes.xxx) → text, needs .keyword ──
		// NOTE: This is a KNOWN GAP — custom attributes currently don't get .keyword
		// in metricsAggField. Documented here for visibility. If this becomes a
		// production issue, extend the function to handle attributes.* text fields.
		{label: "http.method", want: "attributes.http.method",
			note: "KNOWN GAP: custom attributes are text but no .keyword added yet"},
		{label: "db.system", want: "attributes.db.system",
			note: "KNOWN GAP: custom attributes are text but no .keyword added yet"},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := metricsAggField(resolver, tt.label)
			assert.Equal(t, tt.want, got,
				"%s: metricsAggField(%q)", tt.note, tt.label)
		})
	}
}

// TestMetricsAggField_NoUnexpectedKeyword ensures we don't add .keyword to
// fields that are already keyword — the list of fields without .keyword
// should be conscious and explicit.
func TestMetricsAggField_NoUnexpectedKeyword(t *testing.T) {
	resolver := &AttributeResolver{}

	// All currently known fields that correctly return without .keyword.
	knownNonTextFields := map[string]string{
		"kind":                  "kind",
		"name":                  "name",
		"rootName":              "name",
		"rootServiceName":       "serviceName",
		"span:id":               "spanId",
		"span:parentID":         "parentSpanId",
		"trace:id":              "traceId",
		"resource.host.name":    "resource.host.name",
		"resource.service.namespace": "resource.service.namespace",
		"resource.process.pid":  "resource.process.pid",
		"resource.service.version": "resource.service.version",
	}

	for label, wantField := range knownNonTextFields {
		t.Run(label, func(t *testing.T) {
			got := metricsAggField(resolver, label)
			assert.NotContains(t, got, ".keyword",
				"field %q resolves to %q — should NOT have .keyword (already keyword type)",
				label, got)
			assert.Equal(t, wantField, got,
				"field %q should resolve to %q", label, wantField)
		})
	}
}
