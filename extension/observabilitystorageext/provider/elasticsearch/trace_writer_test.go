// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap/zaptest"
)

func TestTraceWriter_SpanToDoc_BasicFields(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 100 // prevent auto-flush

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	// Create a trace with one span
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	rs.Resource().Attributes().PutStr("host.name", "test-host")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span.SetParentSpanID(pcommon.SpanID([8]byte{0, 0, 0, 0, 0, 0, 0, 1}))
	span.SetName("GET /api/users")
	span.SetKind(ptrace.SpanKindServer)
	span.Status().SetCode(ptrace.StatusCodeOk)
	span.Status().SetMessage("success")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 500000000, time.UTC)))

	// Attributes
	span.Attributes().PutStr("http.method", "GET")
	span.Attributes().PutInt("http.status_code", 200)

	// Extract the doc using internal method
	resource := extractResourceAttributes(rs.Resource())
	serviceName := getServiceName(rs.Resource())
	doc := writer.spanToDoc(span, resource, serviceName)

	// Verify basic fields
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", doc["trace_id"])
	assert.Equal(t, "0102030405060708", doc["span_id"])
	assert.Equal(t, "0000000000000001", doc["parent_span_id"])
	assert.Equal(t, "GET /api/users", doc["operation_name"])
	assert.Equal(t, "test-service", doc["service_name"])
	assert.Equal(t, "Server", doc["span_kind"])
	assert.Equal(t, "Ok", doc["status_code"])
	assert.Equal(t, "success", doc["status_message"])

	// Duration: 500ms = 500000 us
	assert.Equal(t, int64(500000), doc["duration_us"])

	// Resource
	res, ok := doc["resource"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "test-service", res["service.name"])
	assert.Equal(t, "test-host", res["host.name"])

	// Attributes
	attrs, ok := doc["attributes"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "GET", attrs["http.method"])
	assert.Equal(t, int64(200), attrs["http.status_code"])
}

func TestTraceWriter_SpanToDoc_NoParentSpan(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "root-service")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("root-span")
	// Don't set ParentSpanID at all - it defaults to all zeros

	resource := extractResourceAttributes(rs.Resource())
	doc := writer.spanToDoc(span, resource, "root-service")

	// Root span should NOT have parent_span_id field
	_, hasParent := doc["parent_span_id"]
	assert.False(t, hasParent, "root span should not have parent_span_id")
}

func TestTraceWriter_SpanToDoc_Events(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "svc")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("span-with-events")

	// Add events
	event1 := span.Events().AppendEmpty()
	event1.SetName("exception")
	event1.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))
	event1.Attributes().PutStr("exception.type", "NullPointerException")
	event1.Attributes().PutStr("exception.message", "null ref")

	event2 := span.Events().AppendEmpty()
	event2.SetName("log")
	event2.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 2, 0, time.UTC)))

	resource := extractResourceAttributes(rs.Resource())
	doc := writer.spanToDoc(span, resource, "svc")

	events, ok := doc["events"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, events, 2)

	// First event
	assert.Equal(t, "exception", events[0]["name"])
	eventAttrs := events[0]["attributes"].(map[string]any)
	assert.Equal(t, "NullPointerException", eventAttrs["exception.type"])
	assert.Equal(t, "null ref", eventAttrs["exception.message"])

	// Second event - no attributes
	assert.Equal(t, "log", events[1]["name"])
	_, hasAttrs := events[1]["attributes"]
	assert.False(t, hasAttrs)
}

func TestTraceWriter_SpanToDoc_Links(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "svc")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("span-with-links")

	link := span.Links().AppendEmpty()
	link.SetTraceID(pcommon.TraceID([16]byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160}))
	link.SetSpanID(pcommon.SpanID([8]byte{11, 22, 33, 44, 55, 66, 77, 88}))

	resource := extractResourceAttributes(rs.Resource())
	doc := writer.spanToDoc(span, resource, "svc")

	links, ok := doc["links"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, links, 1)
	assert.Equal(t, "0a141e28323c46505a646e78828c96a0", links[0]["trace_id"])
	assert.Equal(t, "0b16212c37424d58", links[0]["span_id"])
}

func TestTraceWriter_GetIndexName(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	ts := time.Date(2026, 5, 29, 15, 30, 0, 0, time.UTC)
	indexName := writer.getIndexName("payment-app", ts)
	assert.Equal(t, "otel-traces-payment-app-2026.05.29", indexName)
}

func TestTraceWriter_WriteTraces_EndToEnd(t *testing.T) {
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_bulk" {
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			receivedBody = body
		}
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 1 // flush immediately

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	// Create trace data
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "payment-svc")
	rs.Resource().Attributes().PutStr("app_id", "payment-app")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span.SetName("process-payment")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))

	err = writer.WriteTraces(context.Background(), td)
	require.NoError(t, err)

	// Verify the bulk request body
	require.NotEmpty(t, receivedBody)
	lines := strings.Split(strings.TrimSpace(string(receivedBody)), "\n")
	require.Len(t, lines, 2, "should have action + doc lines")

	// Verify action line
	var action map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &action))
	indexAction := action["index"].(map[string]any)
	assert.Equal(t, "otel-traces-payment-app-2026.05.29", indexAction["_index"])

	// Verify document line
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &doc))
	assert.Equal(t, "process-payment", doc["operation_name"])
	assert.Equal(t, "payment-svc", doc["service_name"])
	assert.Equal(t, float64(1000000), doc["duration_us"]) // 1s = 1000000 us (JSON numbers are float64)
}

func TestTraceWriter_WriteTraces_MultipleSpans(t *testing.T) {
	var bulkCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_bulk" {
			bulkCount++
		}
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 2 // flush every 2 docs

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	// Create trace with 3 spans
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "multi-svc")
	rs.Resource().Attributes().PutStr("app_id", "multi-app")

	ss := rs.ScopeSpans().AppendEmpty()
	for i := 0; i < 3; i++ {
		span := ss.Spans().AppendEmpty()
		span.SetName("span-" + string(rune('A'+i)))
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 100000000, time.UTC)))
	}

	err = writer.WriteTraces(context.Background(), td)
	require.NoError(t, err)

	// 3 spans with batch_size 2 = 1 bulk call (at 2nd span) + remaining 1 span buffered
	assert.Equal(t, 1, bulkCount)

	// Flush remaining
	require.NoError(t, writer.Flush(context.Background()))
	assert.Equal(t, 2, bulkCount)
}

func TestTraceWriter_WriteTraces_RejectsWithoutAppID(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	// Create trace without app_id in resource attributes
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "payment-svc")
	// No app_id set

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("process-payment")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))

	err = writer.WriteTraces(context.Background(), td)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app_id is required")
	assert.Contains(t, err.Error(), "app-level data isolation")
}
