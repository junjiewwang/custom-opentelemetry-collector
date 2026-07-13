// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	v1common "go.opentelemetry.io/proto/otlp/common/v1"
	v1resource "go.opentelemetry.io/proto/otlp/resource/v1"
	v1trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestWrapAsTraceByIDResponse verifies that wrapAsTraceByIDResponse produces
// valid protobuf wire format compatible with tempopb.TraceByIDResponse.
//
// The Grafana 12+ Tempo plugin does:
//
//	var tr tempopb.TraceByIDResponse
//	err = proto.Unmarshal(body, &tr)
//	frame, err = TraceToFrame(tr.Trace.ResourceSpans)
//
// TraceByIDResponse wire format:
//
//	field 1 (Trace): tag=0x0A, length-delimited, body=Trace bytes
//
// Trace wire format (identical to TracesData):
//
//	field 1 (repeated ResourceSpans): tag=0x0A, length-delimited, body=ResourceSpans bytes
func TestWrapAsTraceByIDResponse(t *testing.T) {
	// Build a real TracesData with known content
	td := &v1trace.TracesData{
		ResourceSpans: []*v1trace.ResourceSpans{
			{
				Resource: &v1resource.Resource{
					Attributes: []*v1common.KeyValue{
						{
							Key:   "service.name",
							Value: &v1common.AnyValue{Value: &v1common.AnyValue_StringValue{StringValue: "test-service"}},
						},
					},
				},
				ScopeSpans: []*v1trace.ScopeSpans{
					{
						Scope: &v1common.InstrumentationScope{Name: "test"},
						Spans: []*v1trace.Span{
							{
								TraceId: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
								SpanId:  []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
								Name:    "GET /test",
							},
						},
					},
				},
			},
		},
	}

	tracesDataBytes, err := proto.Marshal(td)
	require.NoError(t, err)
	require.NotEmpty(t, tracesDataBytes)

	// Wrap as TraceByIDResponse
	responseBytes := wrapAsTraceByIDResponse(tracesDataBytes)

	// Verify the wire format is correct:
	// 1. First byte should be the tag for field 1, wire type LEN (bytes)
	// 2. Followed by varint length of the inner TracesData
	// 3. Followed by the TracesData bytes themselves

	// Parse the outer envelope manually
	remaining := responseBytes
	fieldNum, wireType, n := protowire.ConsumeTag(remaining)
	require.True(t, n > 0, "failed to consume outer tag")
	assert.Equal(t, protowire.Number(1), fieldNum, "outer field number should be 1 (TraceByIDResponse.trace)")
	assert.Equal(t, protowire.BytesType, wireType, "outer wire type should be LEN (bytes)")
	remaining = remaining[n:]

	// Consume the length-delimited bytes (the inner Trace/TracesData)
	innerBytes, n := protowire.ConsumeBytes(remaining)
	require.True(t, n > 0, "failed to consume inner bytes")
	assert.Equal(t, tracesDataBytes, innerBytes, "inner bytes should be the original TracesData")

	// Verify we consumed everything
	remaining = remaining[n:]
	assert.Empty(t, remaining, "should have no remaining bytes after consuming TraceByIDResponse")

	// Verify the inner bytes can be deserialized back as TracesData/Trace
	// (simulating what Grafana does: unmarshal TraceByIDResponse.trace → Trace → ResourceSpans)
	var recovered v1trace.TracesData
	err = proto.Unmarshal(innerBytes, &recovered)
	require.NoError(t, err)
	require.Len(t, recovered.ResourceSpans, 1)
	require.Len(t, recovered.ResourceSpans[0].ScopeSpans, 1)
	require.Len(t, recovered.ResourceSpans[0].ScopeSpans[0].Spans, 1)
	assert.Equal(t, "GET /test", recovered.ResourceSpans[0].ScopeSpans[0].Spans[0].Name)
}

// TestWrapAsTraceByIDResponse_EmptyTrace tests wrapping an empty TracesData
// (edge case: TracesData with no ResourceSpans serializes to 0 bytes).
func TestWrapAsTraceByIDResponse_EmptyTrace(t *testing.T) {
	td := &v1trace.TracesData{}
	tracesDataBytes, err := proto.Marshal(td)
	require.NoError(t, err)
	// Empty message serializes to 0 bytes in proto3
	assert.Empty(t, tracesDataBytes)

	// Even with 0-byte payload, the wrapper should be valid
	responseBytes := wrapAsTraceByIDResponse(tracesDataBytes)

	// For 0-byte payload, the wrapper is: tag(1 byte) + varint(0)(1 byte) = 2 bytes
	// tag = 0x0A, length = 0x00
	assert.Equal(t, []byte{0x0A, 0x00}, responseBytes)
}

// TestWrapAsTraceByIDResponse_FullRoundTrip simulates the complete Grafana
// deserialization path by manually parsing the outer TraceByIDResponse envelope
// and then deserializing the inner Trace message.
func TestWrapAsTraceByIDResponse_FullRoundTrip(t *testing.T) {
	// Create TracesData with multiple ResourceSpans (more realistic scenario)
	td := &v1trace.TracesData{
		ResourceSpans: []*v1trace.ResourceSpans{
			{
				Resource: &v1resource.Resource{
					Attributes: []*v1common.KeyValue{
						{Key: "service.name", Value: &v1common.AnyValue{Value: &v1common.AnyValue_StringValue{StringValue: "frontend"}}},
					},
				},
				ScopeSpans: []*v1trace.ScopeSpans{{
					Spans: []*v1trace.Span{
						{
							TraceId: make([]byte, 16),
							SpanId:  make([]byte, 8),
							Name:    "HTTP GET /",
						},
					},
				}},
			},
			{
				Resource: &v1resource.Resource{
					Attributes: []*v1common.KeyValue{
						{Key: "service.name", Value: &v1common.AnyValue{Value: &v1common.AnyValue_StringValue{StringValue: "backend"}}},
					},
				},
				ScopeSpans: []*v1trace.ScopeSpans{{
					Spans: []*v1trace.Span{
						{
							TraceId: make([]byte, 16),
							SpanId:  make([]byte, 8),
							Name:    "SQL SELECT",
						},
					},
				}},
			},
		},
	}

	tracesDataBytes, err := proto.Marshal(td)
	require.NoError(t, err)

	responseBytes := wrapAsTraceByIDResponse(tracesDataBytes)

	// Simulate Grafana's deserialization:
	// Step 1: Parse outer message to extract field 1 (Trace)
	traceBytes := extractField1Bytes(t, responseBytes)

	// Step 2: Parse inner Trace message (same as TracesData)
	var trace v1trace.TracesData
	err = proto.Unmarshal(traceBytes, &trace)
	require.NoError(t, err)

	// Verify all data survived the round-trip
	require.Len(t, trace.ResourceSpans, 2)
	assert.Equal(t, "frontend", trace.ResourceSpans[0].Resource.Attributes[0].Value.GetStringValue())
	assert.Equal(t, "HTTP GET /", trace.ResourceSpans[0].ScopeSpans[0].Spans[0].Name)
	assert.Equal(t, "backend", trace.ResourceSpans[1].Resource.Attributes[0].Value.GetStringValue())
	assert.Equal(t, "SQL SELECT", trace.ResourceSpans[1].ScopeSpans[0].Spans[0].Name)
}

// TestParseScopedTagName verifies that Grafana V2 tag names with scope prefixes
// are correctly split into (scope, key) pairs.
func TestParseScopedTagName(t *testing.T) {
	tests := []struct {
		input     string
		wantScope string
		wantKey   string
	}{
		// Resource-scoped tags
		{"resource.service.name", "resource", "service.name"},
		{"resource.service.namespace", "resource", "service.namespace"},
		{"resource.host.name", "resource", "host.name"},
		{"resource.deployment.environment", "resource", "deployment.environment"},

		// Span-scoped tags
		{"span.http.method", "span", "http.method"},
		{"span.http.url", "span", "http.url"},
		{"span.http.status_code", "span", "http.status_code"},
		{"span.db.system", "span", "db.system"},

		// No scope prefix — V1 style or intrinsic tags
		{"service.name", "", "service.name"},
		{"http.method", "", "http.method"},
		{"name", "", "name"},
		{"status", "", "status"},
		// Note: "span.kind" is parsed as scope="span", key="kind" —
		// this is fine because resolveTagValues handles "kind" case correctly.
		{"span.kind", "span", "kind"},

		// Edge cases
		{"resource.", "resource", ""},
		{"span.", "span", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			scope, key := parseScopedTagName(tt.input)
			assert.Equal(t, tt.wantScope, scope, "scope mismatch")
			assert.Equal(t, tt.wantKey, key, "key mismatch")
		})
	}
}

// extractField1Bytes extracts the bytes value of field 1 from a protobuf message.
// This simulates how proto.Unmarshal for TraceByIDResponse extracts the Trace field.
func extractField1Bytes(t *testing.T, data []byte) []byte {
	t.Helper()
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		require.True(t, n > 0, "failed to consume tag")
		data = data[n:]

		switch typ {
		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			require.True(t, n > 0, "failed to consume bytes field")
			if num == 1 {
				return val
			}
			data = data[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(data)
			require.True(t, n > 0)
			data = data[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	t.Fatal("field 1 not found in message")
	return nil
}

// TestParseTraceQLOrFilter verifies that TraceQL OR conditions are correctly parsed,
// including Grafana's parenthesized format: {(cond1 || cond2 || cond3)}.
func TestParseTraceQLOrFilter(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantAnd    map[string]string
		wantOrLen  int
		wantOrTags []map[string]string // expected OR groups (nil means don't check values)
	}{
		{
			name:      "Grafana OR with parentheses — span.kind variants",
			input:     `{(span.span.kind="internal" || span.span.kind="client" || span.span.kind="server" || span.span.kind="producer" || span.span.kind="consumer")}`,
			wantAnd:   nil,
			wantOrLen: 5,
			wantOrTags: []map[string]string{
				{"span.kind": "internal"},
				{"span.kind": "client"},
				{"span.kind": "server"},
				{"span.kind": "producer"},
				{"span.kind": "consumer"},
			},
		},
		{
			name:      "OR without parentheses",
			input:     `{span.span.kind="internal" || span.span.kind="client"}`,
			wantAnd:   nil,
			wantOrLen: 2,
			wantOrTags: []map[string]string{
				{"span.kind": "internal"},
				{"span.kind": "client"},
			},
		},
		{
			name:      "Simple AND query (no OR)",
			input:     `{span.http.method="GET" && resource.service.name="my-svc"}`,
			wantAnd:   map[string]string{"http.method": "GET", "service.name": "my-svc"},
			wantOrLen: 0,
		},
		{
			name:      "Empty query",
			input:     `{}`,
			wantAnd:   nil,
			wantOrLen: 0,
		},
		{
			name:      "Single condition",
			input:     `{span.http.method="GET"}`,
			wantAnd:   map[string]string{"http.method": "GET"},
			wantOrLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			andTags, orTags := parseTraceQLOrFilter(tt.input)

			if tt.wantAnd == nil {
				assert.Empty(t, andTags)
			} else {
				assert.Equal(t, tt.wantAnd, andTags)
			}

			assert.Len(t, orTags, tt.wantOrLen)

			if tt.wantOrTags != nil {
				for i, wantGroup := range tt.wantOrTags {
					assert.Equal(t, wantGroup, orTags[i], "OR group[%d] mismatch", i)
				}
			}
		})
	}
}

// TestStripOuterParens verifies edge cases of parenthesis stripping.
func TestStripOuterParens(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"(a || b)", "a || b"},
		{"(a) || (b)", "(a) || (b)"},       // Not fully wrapped — no strip
		{"a || b", "a || b"},               // No parens — no strip
		{"((a || b))", "(a || b)"},          // Only strips one layer
		{"()", ""},                          // Empty parens
		{"(a && b) || (c && d)", "(a && b) || (c && d)"}, // Not fully wrapped
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripOuterParens(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseTagValuesFilter verifies that the q parameter in tag values requests
// is correctly parsed into filter conditions.
func TestParseTagValuesFilter(t *testing.T) {
	tests := []struct {
		name   string
		queryQ string
		want   map[string]string
	}{
		{
			name:   "service name filter",
			queryQ: `{resource.service.name="tapm-api"}`,
			want:   map[string]string{"service.name": "tapm-api"},
		},
		{
			name:   "multiple AND filters",
			queryQ: `{resource.service.name="my-svc" && span.http.method="GET"}`,
			want:   map[string]string{"service.name": "my-svc", "http.method": "GET"},
		},
		{
			name:   "empty q parameter",
			queryQ: "",
			want:   nil,
		},
		{
			name:   "empty braces",
			queryQ: "{}",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use url.Values to properly encode q parameter (real requests are URL-encoded).
			params := url.Values{}
			if tt.queryQ != "" {
				params.Set("q", tt.queryQ)
			}
			req, _ := http.NewRequest("GET", "/api/v2/search/tag/name/values?"+params.Encode(), nil)
			got := parseTagValuesFilter(req)
			if tt.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParseTempoSearchParams_IntrinsicFields(t *testing.T) {
	tests := []struct {
		name              string
		queryQ            string
		wantServiceName   string
		wantOperationName string
		wantSpanKind      string
		wantStatus        string
		wantTags          map[string]string
	}{
		{
			name:              "name extracted as OperationName and removed from Tags",
			queryQ:            `{resource.service.name="tapm-api" && name="/api/v1/DescribeApmInfoByAppId"}`,
			wantServiceName:   "tapm-api",
			wantOperationName: "/api/v1/DescribeApmInfoByAppId",
			wantTags:          map[string]string{},
		},
		{
			name:              "name only",
			queryQ:            `{name="/api/v1/GetUser"}`,
			wantOperationName: "/api/v1/GetUser",
			wantTags:          map[string]string{},
		},
		{
			name:            "kind extracted and removed from Tags",
			queryQ:          `{resource.service.name="my-svc" && kind="server"}`,
			wantServiceName: "my-svc",
			wantSpanKind:    "server",
			wantTags:        map[string]string{},
		},
		{
			name:            "status extracted and removed from Tags",
			queryQ:          `{resource.service.name="my-svc" && status="error"}`,
			wantServiceName: "my-svc",
			wantStatus:      "error",
			wantTags:        map[string]string{},
		},
		{
			name:              "all intrinsic fields together with regular attribute",
			queryQ:            `{resource.service.name="my-svc" && name="/api" && kind="client" && status="ok" && span.http.method="GET"}`,
			wantServiceName:   "my-svc",
			wantOperationName: "/api",
			wantSpanKind:      "client",
			wantStatus:        "ok",
			wantTags:          map[string]string{"http.method": "GET"},
		},
		{
			name:            "service.name removed from Tags",
			queryQ:          `{resource.service.name="tapm-api" && span.http.url="/health"}`,
			wantServiceName: "tapm-api",
			wantTags:        map[string]string{"http.url": "/health"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := url.Values{}
			params.Set("q", tt.queryQ)
			params.Set("start", "1783330816")
			params.Set("end", "1783935616")
			req, _ := http.NewRequest("GET", "/api/search?"+params.Encode(), nil)

			query, err := parseTempoSearchParams(req)
			require.NoError(t, err)

			assert.Equal(t, tt.wantServiceName, query.ServiceName, "ServiceName mismatch")
			assert.Equal(t, tt.wantOperationName, query.OperationName, "OperationName mismatch")
			assert.Equal(t, tt.wantSpanKind, query.SpanKind, "SpanKind mismatch")
			assert.Equal(t, tt.wantStatus, query.Status, "Status mismatch")
			if tt.wantTags != nil {
				assert.Equal(t, tt.wantTags, query.Tags, "Tags mismatch")
			}
		})
	}
}
