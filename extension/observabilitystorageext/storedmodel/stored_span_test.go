// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestToParentID(t *testing.T) {
	tests := []struct {
		name     string
		parentID pcommon.SpanID
		want     string
	}{
		{
			name:     "zero span ID (root span) returns empty",
			parentID: pcommon.NewSpanIDEmpty(),
			want:     "",
		},
		{
			name:     "non-zero span ID returns hex string",
			parentID: pcommon.SpanID([8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
			want:     "0102030405060708",
		},
		{
			name:     "span ID with leading zeros returns full hex",
			parentID: pcommon.SpanID([8]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}),
			want:     "0000000000000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toParentID(tt.parentID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToParentID_ZeroSpanIDString(t *testing.T) {
	// pcommon.SpanID zero value's String() returns "" (not "0000000000000000").
	// toParentID correctly identifies it as root via the s == "" check.
	zeroID := pcommon.NewSpanIDEmpty()
	assert.Equal(t, "", zeroID.String(), "zero SpanID String() returns empty")
	assert.Equal(t, "", toParentID(zeroID), "zero SpanID should be treated as root (empty parent)")
}

// TestConvertOTLPSpan_SanitizeDottedKeys verifies the FULL write path from
// OTLP -> StoredSpan -> sanitized ES-compatible attribute keys. This is the
// end-to-end test that reproduces the production scenario: an OTel span with
// peer.service.source (3-segment key) and peer.service (2-segment key) being
// converted through the full pipeline. If this test passes locally but the
// deployed binary writes unsanitized keys, the issue is deployment.
func TestConvertOTLPSpan_SanitizeDottedKeys(t *testing.T) {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()

	// Resource attributes with 3-segment keys (common in production).
	resource := rs.Resource()
	resource.Attributes().PutStr("service.name", "test-service")
	resource.Attributes().PutStr("telemetry.sdk.language", "java")

	// Scope
	ss := rs.ScopeSpans().AppendEmpty()
	scope := ss.Scope()
	scope.SetName("test-instrumentation")

	// Span attributes -- the exact scenario causing ES mapping conflicts.
	span := ss.Spans().AppendEmpty()
	span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	span.SetName("test-span")

	attrs := span.Attributes()
	attrs.PutStr("peer.service.source", "expired")  // 3 segments -> must become peer.service_source
	attrs.PutStr("peer.service", "order-service")    // 2 segments -> must stay peer.service
	attrs.PutStr("http.method", "GET")               // 2 segments -> must stay http.method
	attrs.PutStr("rpc.grpc.status_code", "OK")       // 3 segments -> must become rpc.grpc_status_code
	attrs.PutStr("db.operation.name", "SELECT")      // 3 segments -> must become db.operation_name
	attrs.PutInt("net.peer.port", 8080)              // 3 segments -> must become net.peer_port

	// Full conversion through the same function used in production.
	converted := ConvertOTLPSpan(span, ss, resource)

	// === Verify sanitization on span attributes ===
	attrMap := converted.Attributes

	// 3-segment keys must be sanitized.
	assert.Equal(t, "expired", attrMap["peer.service_source"],
		"peer.service.source must be sanitized to peer.service_source")
	assert.Equal(t, "OK", attrMap["rpc.grpc_status_code"],
		"rpc.grpc.status_code must be sanitized to rpc.grpc_status_code")
	assert.Equal(t, "SELECT", attrMap["db.operation_name"],
		"db.operation.name must be sanitized to db.operation_name")
	assert.Equal(t, int64(8080), attrMap["net.peer_port"],
		"net.peer.port must be sanitized to net.peer_port")

	// 2-segment keys must be unchanged.
	assert.Equal(t, "GET", attrMap["http.method"],
		"http.method (2-segment) must be unchanged")
	assert.Equal(t, "order-service", attrMap["peer.service"],
		"peer.service (2-segment) must be unchanged")

	// CRITICAL: original 3-segment keys must NOT appear in the output.
	require.NotContains(t, attrMap, "peer.service.source",
		"peer.service.source MUST NOT appear in output -- it would cause ES mapping conflict")
	require.NotContains(t, attrMap, "rpc.grpc.status_code",
		"rpc.grpc.status_code MUST NOT appear in output")
	require.NotContains(t, attrMap, "db.operation.name",
		"db.operation.name MUST NOT appear in output")
	require.NotContains(t, attrMap, "net.peer.port",
		"net.peer.port MUST NOT appear in output")

	// No key in the map should have more than 1 dot.
	for k := range attrMap {
		dots := countDots(k)
		assert.LessOrEqual(t, dots, 1,
			"key %q has %d dots, max allowed is 1 (ES mapping conflict prevention)", k, dots)
	}
}

// TestConvertOTLPSpan_ResourceAttributesSanitized verifies resource attributes
// are also sanitized (they share the same ES resource.* field path).
func TestConvertOTLPSpan_ResourceAttributesSanitized(t *testing.T) {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	resource := rs.Resource()
	resource.Attributes().PutStr("service.name", "my-app")
	resource.Attributes().PutStr("host.arch.name", "amd64") // 3 segments -> host.arch_name

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	span.SetName("test")

	converted := ConvertOTLPSpan(span, ss, resource)

	resMap := converted.Resource
	assert.Equal(t, "my-app", resMap["service.name"])
	assert.Equal(t, "amd64", resMap["host.arch_name"])
	require.NotContains(t, resMap, "host.arch.name")
}

// TestConvertOTLPSpan_NoCollisionBetweenTwoAndThreeSegment verifies the core
// conflict scenario: peer.service_source (from 3-segment peer.service.source)
// and peer.service (2-segment) must BOTH exist as sibling keys without overlap.
func TestConvertOTLPSpan_NoCollisionBetweenTwoAndThreeSegment(t *testing.T) {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	resource := rs.Resource()

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	span.SetName("test")

	attrs := span.Attributes()
	attrs.PutStr("peer.service.source", "expired")
	attrs.PutStr("peer.service", "order-service")
	attrs.PutStr("peer.service.target", "another") // 3-seg

	converted := ConvertOTLPSpan(span, ss, resource)
	attrMap := converted.Attributes

	// All three must coexist as siblings under the same first segment.
	assert.Equal(t, "expired", attrMap["peer.service_source"])
	assert.Equal(t, "order-service", attrMap["peer.service"])
	assert.Equal(t, "another", attrMap["peer.service_target"])

	// None should conflict with each other.
	assert.NotContains(t, attrMap, "peer.service.source")
	assert.NotContains(t, attrMap, "peer.service.target")
}

// countDots returns the number of '.' in s.
func countDots(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			n++
		}
	}
	return n
}
