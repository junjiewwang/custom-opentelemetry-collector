// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	observabilitystorageext "go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// spanWithException builds a span carrying an OTel "exception" event, matching
// what the Exceptions tab queries.
func spanWithException() observabilitystorageext.Span {
	return observabilitystorageext.Span{
		SpanID:      "abc",
		Name:        "GET /checkout",
		ServiceName: "gateway",
		Status:      observabilitystorageext.SpanStatus{Code: "Error"},
		Attributes: []observabilitystorageext.KeyValue{
			{Key: "http.method", Value: anyStr("GET")},
		},
		Events: []observabilitystorageext.SpanEvent{
			{
				Name: "exception",
				Attributes: []observabilitystorageext.KeyValue{
					{Key: "exception.type", Value: anyStr("java.lang.IllegalStateException")},
					{Key: "exception.message", Value: anyStr("boom")},
					{Key: "exception.stacktrace", Value: anyStr("at Foo.bar(Foo.java:1)")},
				},
			},
		},
	}
}

func anyStr(s string) observabilitystorageext.AnyValue {
	return observabilitystorageext.AnyValue{StringValue: &s}
}

func attrMap(kvs []tempoKeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv.Value.StringValue != nil {
			out[kv.Key] = *kv.Value.StringValue
		} else {
			out[kv.Key] = ""
		}
	}
	return out
}

// TestProjectSpanWithSelect_EventAttributes covers the Exceptions tab query:
// select(resource.service.name, event.exception.message, event.exception.stacktrace,
// event.exception.type). Event attributes previously resolved to nil, so the tab
// rendered with only service.name and no exception detail.
func TestProjectSpanWithSelect_EventAttributes(t *testing.T) {
	got := projectSpanWithSelect(spanWithException(), []string{
		"resource.service.name",
		"event.exception.message",
		"event.exception.stacktrace",
		"event.exception.type",
	}, nil)

	attrs := attrMap(got.Attributes)
	assert.Equal(t, "gateway", attrs["service.name"])
	assert.Equal(t, "boom", attrs["event.exception.message"])
	assert.Equal(t, "java.lang.IllegalStateException", attrs["event.exception.type"])
	assert.Equal(t, "at Foo.bar(Foo.java:1)", attrs["event.exception.stacktrace"])
}

// TestResolveSelectField_EventScopeIsolated verifies the event scope only reads
// event attributes, and that span/resource scopes do not leak into it.
func TestResolveSelectField_EventScopeIsolated(t *testing.T) {
	span := spanWithException()

	// An event-scoped key that exists only as a span attribute must not resolve.
	assert.Nil(t, resolveSelectField(span, "event.http.method", nil))

	// A span-scoped key must not pick up an event attribute.
	assert.Nil(t, resolveSelectField(span, "span.exception.type", nil))

	// event:name (colon intrinsic) resolves to the event's name.
	v := resolveSelectField(span, "event:name", nil)
	require.NotNil(t, v)
	require.NotNil(t, v.StringValue)
	assert.Equal(t, "exception", *v.StringValue)
}

// TestResolveSelectField_EventMissingReturnsNil verifies spans without events
// resolve event-scoped selects to nil rather than erroring.
func TestResolveSelectField_EventMissingReturnsNil(t *testing.T) {
	span := observabilitystorageext.Span{SpanID: "x", Name: "noop"}
	assert.Nil(t, resolveSelectField(span, "event.exception.type", nil))
	assert.Nil(t, resolveSelectField(span, "event:name", nil))
}

// TestStringToTempoAnyValue_NoRedundantFallback guards the metrics label
// serialization: traces-drilldown reads Value.string_value only on the
// trace-merge path, so emitting it for every metrics label just doubled payload.
func TestStringToTempoAnyValue_NoRedundantFallback(t *testing.T) {
	v := stringToTempoAnyValue("gateway")
	require.NotNil(t, v.StringValue)
	assert.Equal(t, "gateway", *v.StringValue)
	assert.Nil(t, v.Value, "metrics labels must not carry the snake_case fallback")
}

// TestAnyToTempoValue_KeepsFallback verifies span attributes still emit the
// snake_case fallback, which the Structure tab's trace-merge path reads.
func TestAnyToTempoValue_KeepsFallback(t *testing.T) {
	v := anyToTempoValue("gateway")
	require.NotNil(t, v.StringValue)
	require.NotNil(t, v.Value, "span attributes still need the fallback")
	require.NotNil(t, v.Value.StringValue)
	assert.Equal(t, "gateway", *v.Value.StringValue)
}
