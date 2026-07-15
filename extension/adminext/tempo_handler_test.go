// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	v1common "go.opentelemetry.io/proto/otlp/common/v1"
	v1resource "go.opentelemetry.io/proto/otlp/resource/v1"
	v1trace "go.opentelemetry.io/proto/otlp/trace/v1"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
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
// OR results are now wrapped in a single outer group (TagsOr is [][]map[string]string).
func TestParseTraceQLOrFilter(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantAnd         map[string]string
		wantOrGroupCount int            // number of outer OR groups
		wantOrBranchCount int           // number of branches in the first (only) group
		wantOrTags      []map[string]string // expected OR branches
	}{
		{
			name:            "Grafana OR with parentheses — span.kind variants",
			input:           `{(span.span.kind="internal" || span.span.kind="client" || span.span.kind="server" || span.span.kind="producer" || span.span.kind="consumer")}`,
			wantAnd:         nil,
			wantOrGroupCount: 1,
			wantOrBranchCount: 5,
			wantOrTags: []map[string]string{
				{"span.kind": "internal"},
				{"span.kind": "client"},
				{"span.kind": "server"},
				{"span.kind": "producer"},
				{"span.kind": "consumer"},
			},
		},
		{
			name:            "OR without parentheses",
			input:           `{span.span.kind="internal" || span.span.kind="client"}`,
			wantAnd:         nil,
			wantOrGroupCount: 1,
			wantOrBranchCount: 2,
			wantOrTags: []map[string]string{
				{"span.kind": "internal"},
				{"span.kind": "client"},
			},
		},
		{
			name:            "Simple AND query (no OR)",
			input:           `{span.http.method="GET" && resource.service.name="my-svc"}`,
			wantAnd:         map[string]string{"http.method": "GET", "service.name": "my-svc"},
			wantOrGroupCount: 0,
		},
		{
			name:            "Empty query",
			input:           `{}`,
			wantAnd:         nil,
			wantOrGroupCount: 0,
		},
		{
			name:            "Single condition",
			input:           `{span.http.method="GET"}`,
			wantAnd:         map[string]string{"http.method": "GET"},
			wantOrGroupCount: 0,
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

			// orTags is now [][]map[string]string
			assert.Len(t, orTags, tt.wantOrGroupCount, "outer group count")
			if tt.wantOrGroupCount > 0 {
				firstGroup := orTags[0]
				assert.Len(t, firstGroup, tt.wantOrBranchCount, "branch count in group 0")
				if tt.wantOrTags != nil {
					for i, wantMap := range tt.wantOrTags {
						assert.Equal(t, wantMap, firstGroup[i], "branch[%d] mismatch", i)
					}
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
			wantTags:          map[string]string{"span.http.method": "GET"},
		},
		{
			name:            "service.name removed from Tags",
			queryQ:          `{resource.service.name="tapm-api" && span.http.url="/health"}`,
			wantServiceName: "tapm-api",
			wantTags:        map[string]string{"span.http.url": "/health"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := url.Values{}
			params.Set("q", tt.queryQ)
			params.Set("start", "1783330816")
			params.Set("end", "1783935616")
			req, _ := http.NewRequest("GET", "/api/search?"+params.Encode(), nil)

			_, query, err := parseTempoSearchParams(req)
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

// TestParseTempoSearchParams_DurationFilter verifies that duration range operators
// (>, <, >=, <=) in TraceQL queries are correctly extracted via the unified AST parser.
// This was a known bug where the legacy simple parser silently dropped duration>X conditions.
func TestParseTempoSearchParams_DurationFilter(t *testing.T) {
	tests := []struct {
		name            string
		queryQ          string
		wantMinDuration time.Duration
		wantMaxDuration time.Duration
		wantServiceName string
	}{
		{
			name:            "duration > 1.2s extracted as MinDuration",
			queryQ:          `{resource.service.name="customcol" && duration>1.2s && name="GET /api/v2/tempo/api/metrics/query_range"}`,
			wantMinDuration: 1200 * time.Millisecond,
			wantServiceName: "customcol",
		},
		{
			name:            "duration < 500ms extracted as MaxDuration",
			queryQ:          `{resource.service.name="my-svc" && duration<500ms}`,
			wantMaxDuration: 500 * time.Millisecond,
			wantServiceName: "my-svc",
		},
		{
			name:            "both min and max duration",
			queryQ:          `{duration>100ms && duration<5s}`,
			wantMinDuration: 100 * time.Millisecond,
			wantMaxDuration: 5 * time.Second,
		},
		{
			name:            "duration >= 2s",
			queryQ:          `{resource.service.name="svc" && duration>=2s}`,
			wantMinDuration: 2 * time.Second,
			wantServiceName: "svc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := url.Values{}
			params.Set("q", tt.queryQ)
			params.Set("start", "1783330816")
			params.Set("end", "1783935616")
			req, _ := http.NewRequest("GET", "/api/search?"+params.Encode(), nil)

			_, query, err := parseTempoSearchParams(req)
			require.NoError(t, err)

			assert.Equal(t, tt.wantMinDuration, query.MinDuration, "MinDuration mismatch")
			assert.Equal(t, tt.wantMaxDuration, query.MaxDuration, "MaxDuration mismatch")
			if tt.wantServiceName != "" {
				assert.Equal(t, tt.wantServiceName, query.ServiceName, "ServiceName mismatch")
			}
		})
	}
}

// ═══════════════════════════════════════════════════
// Select Projection Tests
// ═══════════════════════════════════════════════════

func makeTestSpan(name, kind, service, statusCode string, attrs map[string]string) observabilitystorageext.Span {
	span := observabilitystorageext.Span{
		SpanID:      "test-span-1",
		Name:        name,
		Kind:        observabilitystorageext.SpanKind(kind),
		ServiceName: service,
		Status: observabilitystorageext.SpanStatus{
			Code: observabilitystorageext.StatusCode(statusCode),
		},
	}

	for k, v := range attrs {
		sv := v
		span.Attributes = append(span.Attributes, observabilitystorageext.KeyValue{
			Key:   k,
			Value: observabilitystorageext.AnyValue{StringValue: &sv},
		})
	}

	return span
}

func TestProjectSpanWithSelect_AllFields(t *testing.T) {
	span := makeTestSpan("/api/v1", "SPAN_KIND_SERVER", "tapm-api", "STATUS_CODE_OK", map[string]string{
		"http.method": "GET",
		"http.url":    "/api/v1/users",
	})

	fields := []string{"name", "kind", "status", "duration", "resource.service.name"}
	result := projectSpanWithSelect(span, fields, nil)

	// "name" is now extracted as top-level Name, remaining 4 fields in Attributes
	assert.Equal(t, "/api/v1", result.Name)
	require.Len(t, result.Attributes, 4)

	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
	}

	assert.Equal(t, "SPAN_KIND_SERVER", attrMap["kind"])
	assert.Equal(t, "STATUS_CODE_OK", attrMap["status"])
	assert.Equal(t, "tapm-api", attrMap["service.name"])
}

func TestProjectSpanWithSelect_Attributes(t *testing.T) {
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "svc", "STATUS_CODE_OK", map[string]string{
		"http.method": "POST",
		"http.url":    "/api/create",
	})

	fields := []string{"http.method", "http.url"}
	result := projectSpanWithSelect(span, fields, nil)

	require.Len(t, result.Attributes, 2)

	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
	}

	assert.Equal(t, "POST", attrMap["http.method"])
	assert.Equal(t, "/api/create", attrMap["http.url"])
}

func TestProjectSpanWithSelect_MixedFields(t *testing.T) {
	span := makeTestSpan("/api/delete", "SPAN_KIND_CLIENT", "my-svc", "STATUS_CODE_ERROR", map[string]string{
		"http.method": "DELETE",
	})

	fields := []string{"name", "http.method", "status"}
	result := projectSpanWithSelect(span, fields, nil)

	// "name" extracted as top-level Name, remaining 2 in Attributes
	assert.Equal(t, "/api/delete", result.Name)
	require.Len(t, result.Attributes, 2)

	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
	}

	assert.Equal(t, "DELETE", attrMap["http.method"])
	assert.Equal(t, "STATUS_CODE_ERROR", attrMap["status"])
}

func TestProjectSpanWithSelect_EmptyFields(t *testing.T) {
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "svc", "STATUS_CODE_OK", map[string]string{
		"http.method": "GET",
	})

	result := projectSpanWithSelect(span, nil, nil)
	assert.NotEmpty(t, result.Attributes)
	assert.Equal(t, "test", result.Name)

	result = projectSpanWithSelect(span, []string{}, nil)
	assert.NotEmpty(t, result.Attributes)
	assert.Equal(t, "test", result.Name)
}

func TestProjectSpanWithSelect_UnknownField(t *testing.T) {
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "svc", "STATUS_CODE_OK", nil)

	result := projectSpanWithSelect(span, []string{"nonexistent.field"}, nil)
	assert.Empty(t, result.Attributes)
}

func TestProjectSpanWithSelect_ScopedFields(t *testing.T) {
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "tapm-api", "STATUS_CODE_OK", map[string]string{
		"http.method": "GET",
	})

	appID := "apm-app"
	span.Resource = append(span.Resource, observabilitystorageext.KeyValue{
		Key:   "service.name",
		Value: observabilitystorageext.AnyValue{StringValue: &appID},
	})

	fields := []string{"resource.service.name", "span.http.method"}
	result := projectSpanWithSelect(span, fields, nil)

	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
	}

	// Keys should have scope prefix stripped for Grafana compatibility
	assert.Equal(t, "tapm-api", attrMap["service.name"])
	assert.Equal(t, "GET", attrMap["http.method"])
}

func TestProjectSpanWithSelect_StatusMessage(t *testing.T) {
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "svc", "STATUS_CODE_ERROR", nil)
	span.Status.Message = "something went wrong"

	fields := []string{"status.code", "status.message"}
	result := projectSpanWithSelect(span, fields, nil)

	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
	}

	assert.Equal(t, "STATUS_CODE_ERROR", attrMap["status.code"])
	assert.Equal(t, "something went wrong", attrMap["status.message"])
}

// ═══════════════════════════════════════════════════
// computeNestedSet / needsNestedSet / intVal — nested set model
// ═══════════════════════════════════════════════════

func TestIntVal(t *testing.T) {
	v := intVal(42)
	require.NotNil(t, v.IntValue)
	assert.Equal(t, "42", *v.IntValue)
	assert.Nil(t, v.StringValue)

	v = intVal(-1)
	require.NotNil(t, v.IntValue)
	assert.Equal(t, "-1", *v.IntValue)
}

func TestNeedsNestedSet(t *testing.T) {
	assert.True(t, needsNestedSet([]string{"nestedSetParent"}))
	assert.True(t, needsNestedSet([]string{"nestedSetLeft"}))
	assert.True(t, needsNestedSet([]string{"nestedSetRight"}))
	assert.True(t, needsNestedSet([]string{"name", "nestedSetParent"}))
	assert.False(t, needsNestedSet(nil))
	assert.False(t, needsNestedSet([]string{}))
	assert.False(t, needsNestedSet([]string{"name", "kind", "duration"}))
}

func TestComputeNestedSet_SingleRoot(t *testing.T) {
	spans := []observabilitystorageext.Span{
		{SpanID: "root", ParentSpanID: ""},
	}
	result := computeNestedSet(spans)

	require.Len(t, result, 1)
	info := result["root"]
	assert.Equal(t, -1, info.Parent)
	assert.Equal(t, 1, info.Left)
	assert.Equal(t, 2, info.Right)
}

func TestComputeNestedSet_LinearChain(t *testing.T) {
	// A → B → C
	spans := []observabilitystorageext.Span{
		{SpanID: "A", ParentSpanID: "", StartTimeUnixNano: "1"},
		{SpanID: "B", ParentSpanID: "A", StartTimeUnixNano: "2"},
		{SpanID: "C", ParentSpanID: "B", StartTimeUnixNano: "3"},
	}
	result := computeNestedSet(spans)

	require.Len(t, result, 3)

	// Root A
	a := result["A"]
	assert.Equal(t, -1, a.Parent, "root parent should be -1")
	assert.Equal(t, 1, a.Left)
	assert.Equal(t, 6, a.Right)

	// Child B: parent=left(A)=1
	b := result["B"]
	assert.Equal(t, 1, b.Parent, "B parent should be A.Left=1")
	assert.Equal(t, 2, b.Left)
	assert.Equal(t, 5, b.Right)

	// Grandchild C: parent=left(B)=2
	c := result["C"]
	assert.Equal(t, 2, c.Parent, "C parent should be B.Left=2")
	assert.Equal(t, 3, c.Left)
	assert.Equal(t, 4, c.Right)

	// Verify ancestor containment
	assert.True(t, a.Left < b.Left && a.Right > b.Right, "A should contain B")
	assert.True(t, b.Left < c.Left && b.Right > c.Right, "B should contain C")
}

func TestComputeNestedSet_MultiChild(t *testing.T) {
	// Root with 3 children (sorted by start time)
	spans := []observabilitystorageext.Span{
		{SpanID: "root", ParentSpanID: "", StartTimeUnixNano: "0"},
		{SpanID: "C1", ParentSpanID: "root", StartTimeUnixNano: "3"},
		{SpanID: "C2", ParentSpanID: "root", StartTimeUnixNano: "1"},
		{SpanID: "C3", ParentSpanID: "root", StartTimeUnixNano: "2"},
	}
	result := computeNestedSet(spans)

	require.Len(t, result, 4)

	root := result["root"]
	assert.Equal(t, -1, root.Parent)

	// All children should point to root.Left
	for _, cid := range []string{"C1", "C2", "C3"} {
		c := result[cid]
		assert.Equal(t, root.Left, c.Parent, "child %s parent should be root.Left", cid)
		assert.True(t, c.Left < c.Right, "%s left < right", cid)
		assert.True(t, root.Left < c.Left && root.Right > c.Right, "root should contain %s", cid)
	}
}

func TestComputeNestedSet_OrphanSpans(t *testing.T) {
	// Span B has parent A, but A is not in this span set
	spans := []observabilitystorageext.Span{
		{SpanID: "B", ParentSpanID: "A", StartTimeUnixNano: "1"},
		{SpanID: "C", ParentSpanID: "", StartTimeUnixNano: "2"},
	}
	result := computeNestedSet(spans)

	require.Len(t, result, 2)

	// C is a proper root
	c := result["C"]
	assert.Equal(t, -1, c.Parent)

	// B is orphan, treated as additional root
	b := result["B"]
	assert.Equal(t, -1, b.Parent, "orphan span should be treated as root")
	assert.True(t, b.Left < b.Right)
}

func TestComputeNestedSet_Empty(t *testing.T) {
	result := computeNestedSet(nil)
	assert.Nil(t, result)

	result = computeNestedSet([]observabilitystorageext.Span{})
	assert.Nil(t, result)
}

// ═══════════════════════════════════════════════════
// projectSpanWithSelect / resolveSelectField — nested set fields
// ═══════════════════════════════════════════════════

func TestProjectSpanWithSelect_NestedSetFields(t *testing.T) {
	// Build a simple trace: root → child
	spans := []observabilitystorageext.Span{
		{SpanID: "root", ParentSpanID: "", StartTimeUnixNano: "1"},
		{SpanID: "child", ParentSpanID: "root", StartTimeUnixNano: "2"},
	}
	nsInfo := computeNestedSet(spans)

	// Test root span
	rootSpan := spans[0]
	fields := []string{"nestedSetParent", "nestedSetLeft", "nestedSetRight"}
	result := projectSpanWithSelect(rootSpan, fields, nsInfo)

	require.Len(t, result.Attributes, 3)
	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.IntValue != nil {
			attrMap[a.Key] = *a.Value.IntValue
		}
	}
	assert.Equal(t, "-1", attrMap["nestedSetParent"], "root parent should be -1")
	rootLeft := attrMap["nestedSetLeft"]
	rootRight := attrMap["nestedSetRight"]
	assert.NotEmpty(t, rootLeft)
	assert.NotEmpty(t, rootRight)

	// Test child span
	childSpan := spans[1]
	result = projectSpanWithSelect(childSpan, fields, nsInfo)

	require.Len(t, result.Attributes, 3)
	attrMap = make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.IntValue != nil {
			attrMap[a.Key] = *a.Value.IntValue
		}
	}
	assert.Equal(t, rootLeft, attrMap["nestedSetParent"], "child parent should equal root.left")
	assert.NotEmpty(t, attrMap["nestedSetLeft"])
	assert.NotEmpty(t, attrMap["nestedSetRight"])
}

func TestProjectSpanWithSelect_NestedSetFallback(t *testing.T) {
	// When nsInfo is nil, use fallback values
	span := makeTestSpan("test", "SPAN_KIND_INTERNAL", "svc", "STATUS_CODE_OK", nil)
	// Set parentSpanID to empty (root span)
	span.ParentSpanID = ""

	fields := []string{"nestedSetParent", "nestedSetLeft", "nestedSetRight"}
	result := projectSpanWithSelect(span, fields, nil)

	require.Len(t, result.Attributes, 3)
	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.IntValue != nil {
			attrMap[a.Key] = *a.Value.IntValue
		}
	}
	// Root span fallback: parent=-1
	assert.Equal(t, "-1", attrMap["nestedSetParent"])
	assert.Equal(t, "1", attrMap["nestedSetLeft"])
	assert.Equal(t, "2", attrMap["nestedSetRight"])

	// Non-root span fallback: parent=1
	span.ParentSpanID = "some-parent"
	result = projectSpanWithSelect(span, fields, nil)

	require.Len(t, result.Attributes, 3)
	attrMap = make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.IntValue != nil {
			attrMap[a.Key] = *a.Value.IntValue
		}
	}
	assert.Equal(t, "1", attrMap["nestedSetParent"])
}

func TestProjectSpanWithSelect_NoNestedSetFields(t *testing.T) {
	// When selectFields don't include nested set, nsInfo is not computed
	assert.False(t, needsNestedSet([]string{"name", "kind"}))

	span := makeTestSpan("op", "SPAN_KIND_SERVER", "svc", "STATUS_CODE_OK", nil)
	// Pass nil nsInfo — should still work for non-nested fields
	result := projectSpanWithSelect(span, []string{"name", "kind"}, nil)
	// "name" is extracted as top-level Name, only "kind" remains in Attributes
	assert.Equal(t, "op", result.Name)
	require.Len(t, result.Attributes, 1)
}

func TestProjectSpanWithSelect_NestedSetMixedFields(t *testing.T) {
	spans := []observabilitystorageext.Span{
		{SpanID: "root", ParentSpanID: "", Name: "root-op",
			Kind: observabilitystorageext.SpanKindServer, StartTimeUnixNano: "1",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk}},
	}
	nsInfo := computeNestedSet(spans)

	fields := []string{"name", "nestedSetParent", "nestedSetLeft", "status"}
	result := projectSpanWithSelect(spans[0], fields, nsInfo)

	// "name" is extracted as top-level Name, remaining 3 fields in Attributes
	assert.Equal(t, "root-op", result.Name)
	require.Len(t, result.Attributes, 3)
	attrMap := make(map[string]string)
	for _, a := range result.Attributes {
		if a.Value.StringValue != nil {
			attrMap[a.Key] = *a.Value.StringValue
		}
		if a.Value.IntValue != nil {
			attrMap[a.Key] = *a.Value.IntValue
		}
	}
	assert.Equal(t, "-1", attrMap["nestedSetParent"])
	assert.Equal(t, "1", attrMap["nestedSetLeft"])
	assert.Equal(t, "STATUS_CODE_OK", attrMap["status"])
}

// ═══════════════════════════════════════════════════
// spanKindToString / spanStatusToString — multi-format handling
// ═══════════════════════════════════════════════════

func TestSpanKindToString(t *testing.T) {
	tests := []struct {
		name     string
		input    observabilitystorageext.SpanKind
		expected string
	}{
		// Standard OTel enum format (SPAN_KIND_XXX).
		{"enum_server", observabilitystorageext.SpanKindServer, "server"},
		{"enum_client", observabilitystorageext.SpanKindClient, "client"},
		{"enum_internal", observabilitystorageext.SpanKindInternal, "internal"},
		{"enum_producer", observabilitystorageext.SpanKindProducer, "producer"},
		{"enum_consumer", observabilitystorageext.SpanKindConsumer, "consumer"},
		{"enum_unspecified", observabilitystorageext.SpanKindUnspecified, "unspecified"},

		// ptrace.SpanKind.String() format stored in ES (capitalized first letter).
		{"es_Server", observabilitystorageext.SpanKind("Server"), "server"},
		{"es_Client", observabilitystorageext.SpanKind("Client"), "client"},
		{"es_Internal", observabilitystorageext.SpanKind("Internal"), "internal"},
		{"es_Producer", observabilitystorageext.SpanKind("Producer"), "producer"},
		{"es_Consumer", observabilitystorageext.SpanKind("Consumer"), "consumer"},

		// Lowercase format (just in case).
		{"lower_server", observabilitystorageext.SpanKind("server"), "server"},
		{"lower_client", observabilitystorageext.SpanKind("client"), "client"},

		// Unknown format.
		{"unknown", observabilitystorageext.SpanKind("UNKNOWN"), "unspecified"},
		{"empty", observabilitystorageext.SpanKind(""), "unspecified"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := spanKindToString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSpanStatusToString(t *testing.T) {
	tests := []struct {
		name     string
		input    observabilitystorageext.SpanStatus
		expected string
	}{
		// Standard OTel enum format.
		{"enum_ok", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk}, "ok"},
		{"enum_error", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeError}, "error"},
		{"enum_unset", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeUnset}, "unset"},

		// ptrace.StatusCode.String() format stored in ES.
		{"es_Ok", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCode("Ok")}, "ok"},
		{"es_Error", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCode("Error")}, "error"},
		{"es_Unset", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCode("Unset")}, "unset"},

		// Unknown format.
		{"unknown", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCode("GARBAGE")}, "unset"},
		{"empty", observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCode("")}, "unset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := spanStatusToString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ═══════════════════════════════════════════════════
// convertStructuralResultToTempoSearchTrace Tests
// ═══════════════════════════════════════════════════

func TestConvertStructuralResult_NestedSetReturnsAllSpans(t *testing.T) {
	// Simulate a trace with root → server → db (3 spans).
	// Only root and server are "matched" by structural query.
	// When selectFields includes nestedSet fields, ALL spans should be returned
	// so Grafana can build the full Service Structure hierarchy.
	fullSpans := []observabilitystorageext.Span{
		{SpanID: "root-1", ParentSpanID: "", Name: "HTTP GET /",
			Kind: observabilitystorageext.SpanKindServer, ServiceName: "frontend",
			StartTimeUnixNano: "1000", DurationNano: "5000",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk}},
		{SpanID: "child-1", ParentSpanID: "root-1", Name: "grpc.call",
			Kind: observabilitystorageext.SpanKindClient, ServiceName: "frontend",
			StartTimeUnixNano: "2000", DurationNano: "3000",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk}},
		{SpanID: "child-2", ParentSpanID: "child-1", Name: "SELECT *",
			Kind: observabilitystorageext.SpanKindClient, ServiceName: "backend",
			StartTimeUnixNano: "3000", DurationNano: "1000",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk}},
	}

	// Only root and child-1 are "structurally matched" (e.g., root &>> server pair).
	matchedIDs := map[string]bool{
		"root-1":  true,
		"child-1": true,
	}

	sr := structuralVerifyResult{
		summary: observabilitystorageext.TraceSummary{
			TraceID:           "abc123",
			RootServiceName:   "frontend",
			RootSpanName:      "HTTP GET /",
			StartTimeUnixNano: "1000",
			DurationMs:        5,
		},
		fullSpans:      fullSpans,
		matchedSpanIDs: matchedIDs,
	}

	// Case 1: With nestedSet fields → should return ALL spans.
	selectFields := []string{"status", "resource.service.name", "name", "nestedSetParent", "nestedSetLeft", "nestedSetRight"}
	result := convertStructuralResultToTempoSearchTrace(sr, selectFields, 20)

	assert.Equal(t, "abc123", result.TraceID)
	require.Len(t, result.SpanSets, 1)
	// All 3 spans should be included (not just the 2 matched ones).
	assert.Len(t, result.SpanSets[0].Spans, 3, "nestedSet select should return ALL trace spans")
	assert.Equal(t, 3, result.SpanSets[0].Matched, "matched count should reflect total span count")

	// Verify each span has nestedSet attributes.
	for _, sp := range result.SpanSets[0].Spans {
		hasNS := false
		for _, attr := range sp.Attributes {
			if attr.Key == "nestedSetParent" || attr.Key == "nestedSetLeft" || attr.Key == "nestedSetRight" {
				hasNS = true
				break
			}
		}
		assert.True(t, hasNS, "span %s should have nestedSet attributes", sp.SpanID)
	}

	// Case 2: Without nestedSet fields → should return only matched spans.
	selectFieldsNoNS := []string{"status", "resource.service.name", "name"}
	result2 := convertStructuralResultToTempoSearchTrace(sr, selectFieldsNoNS, 20)

	require.Len(t, result2.SpanSets, 1)
	assert.Len(t, result2.SpanSets[0].Spans, 2, "without nestedSet select, only matched spans should be returned")
	assert.Equal(t, 2, result2.SpanSets[0].Matched)
}

func TestConvertStructuralResult_SpssLimitsOutput(t *testing.T) {
	// Create a trace with many spans.
	fullSpans := make([]observabilitystorageext.Span, 10)
	matchedIDs := make(map[string]bool)
	for i := 0; i < 10; i++ {
		id := "span-" + string(rune('A'+i))
		parentID := ""
		if i > 0 {
			parentID = "span-" + string(rune('A'))
		}
		fullSpans[i] = observabilitystorageext.Span{
			SpanID: id, ParentSpanID: parentID, Name: "op-" + id,
			Kind: observabilitystorageext.SpanKindServer, ServiceName: "svc",
			StartTimeUnixNano: "1000", DurationNano: "100",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk},
		}
		matchedIDs[id] = true
	}

	sr := structuralVerifyResult{
		summary: observabilitystorageext.TraceSummary{
			TraceID:           "trace-spss",
			RootServiceName:   "svc",
			RootSpanName:      "op",
			StartTimeUnixNano: "1000",
			DurationMs:        1,
		},
		fullSpans:      fullSpans,
		matchedSpanIDs: matchedIDs,
	}

	// spss=5 should limit output to 5 spans even with nestedSet select.
	selectFields := []string{"name", "nestedSetParent", "nestedSetLeft", "nestedSetRight"}
	result := convertStructuralResultToTempoSearchTrace(sr, selectFields, 5)

	require.Len(t, result.SpanSets, 1)
	assert.Len(t, result.SpanSets[0].Spans, 5, "spss should limit number of returned spans")
}

// TestParseTempoSearchParams_StructuralQueryRelaxesConditions verifies that
// structural queries don't apply SpanKind/Status as AND conditions on the ES query.
func TestParseTempoSearchParams_StructuralQueryRelaxesConditions(t *testing.T) {
	// Grafana sends this exact query for Service Structure view.
	traceQL := `({nestedSetParent<0 && true } &>> { kind = server }) || ({nestedSetParent<0 && true }) | select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)`

	params := url.Values{}
	params.Set("q", traceQL)
	params.Set("start", "1784008043")
	params.Set("end", "1784009843")
	params.Set("limit", "200")
	params.Set("spss", "20")
	req, _ := http.NewRequest("GET", "/api/search?"+params.Encode(), nil)

	plan, query, err := parseTempoSearchParams(req)
	require.NoError(t, err)
	require.NotNil(t, plan)

	// Structural query should be detected.
	assert.True(t, plan.HasStructural, "should detect structural operator")
	assert.True(t, plan.IsRoot, "should detect root span condition")

	// SpanKind should NOT be pushed to ES query for structural queries.
	assert.Empty(t, query.SpanKind, "structural query should NOT push SpanKind to ES")
	assert.Empty(t, query.Status, "structural query should NOT push Status to ES")

	// IsRoot should still be applied (helps narrow candidates).
	assert.True(t, query.IsRoot, "IsRoot should be preserved for candidate filtering")
}
