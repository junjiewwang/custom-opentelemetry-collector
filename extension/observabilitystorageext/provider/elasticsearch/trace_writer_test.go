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
	cfg.BatchSize = 100

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	rs.Resource().Attributes().PutStr("host.name", "test-host")

	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("go.opentelemetry.contrib")
	ss.Scope().SetVersion("v1.0.0")

	span := ss.Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span.SetParentSpanID(pcommon.SpanID([8]byte{0, 0, 0, 0, 0, 0, 0, 1}))
	span.SetName("GET /api/users")
	span.SetKind(ptrace.SpanKindServer)
	span.Status().SetCode(ptrace.StatusCodeOk)
	span.Status().SetMessage("success")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 500000000, time.UTC)))
	span.Attributes().PutStr("http.method", "GET")
	span.Attributes().PutInt("http.status_code", 200)

	doc := writer.convertSpan(span, ss, rs.Resource())

	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", doc.TraceID)
	assert.Equal(t, "0102030405060708", doc.SpanID)
	assert.Equal(t, "0000000000000001", doc.ParentSpanID)
	assert.Equal(t, "GET /api/users", doc.Name)
	assert.Equal(t, "test-service", doc.ServiceName)
	assert.Equal(t, "Server", doc.Kind)
	assert.Equal(t, "Ok", doc.Status.Code)
	assert.Equal(t, "success", doc.Status.Message)
	assert.Equal(t, int64(500000000), doc.DurationNano)

	assert.Equal(t, "test-service", doc.Resource["service.name"])
	assert.Equal(t, "test-host", doc.Resource["host.name"])

	assert.Equal(t, "GET", doc.Attributes["http.method"])
	assert.Equal(t, int64(200), doc.Attributes["http.status_code"])

	// Scope info
	assert.Equal(t, "go.opentelemetry.contrib", doc.Scope.Name)
	assert.Equal(t, "v1.0.0", doc.Scope.Version)
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

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetName("root-span")

	doc := writer.convertSpan(span, ss, rs.Resource())
	assert.Empty(t, doc.ParentSpanID, "root span should not have parentSpanId")
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

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetName("span-with-events")

	event1 := span.Events().AppendEmpty()
	event1.SetName("exception")
	event1.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))
	event1.Attributes().PutStr("exception.type", "NullPointerException")
	event1.Attributes().PutStr("exception.message", "null ref")

	event2 := span.Events().AppendEmpty()
	event2.SetName("log")
	event2.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 2, 0, time.UTC)))

	doc := writer.convertSpan(span, ss, rs.Resource())

	require.Len(t, doc.Events, 2)
	assert.Equal(t, "exception", doc.Events[0].Name)
	assert.Equal(t, "NullPointerException", doc.Events[0].Attributes["exception.type"])
	assert.Equal(t, "null ref", doc.Events[0].Attributes["exception.message"])
	assert.Equal(t, "log", doc.Events[1].Name)
	assert.Nil(t, doc.Events[1].Attributes)
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

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetName("span-with-links")

	link := span.Links().AppendEmpty()
	link.SetTraceID(pcommon.TraceID([16]byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160}))
	link.SetSpanID(pcommon.SpanID([8]byte{11, 22, 33, 44, 55, 66, 77, 88}))

	doc := writer.convertSpan(span, ss, rs.Resource())

	require.Len(t, doc.Links, 1)
	assert.Equal(t, "0a141e28323c46505a646e78828c96a0", doc.Links[0].TraceID)
	assert.Equal(t, "0b16212c37424d58", doc.Links[0].SpanID)
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
	cfg.BatchSize = 1

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "payment-svc")
	rs.Resource().Attributes().PutStr("app_id", "payment-app")

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span.SetName("process-payment")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))

	err = writer.WriteTraces(context.Background(), td)
	require.NoError(t, err)

	require.NotEmpty(t, receivedBody)
	lines := strings.Split(strings.TrimSpace(string(receivedBody)), "\n")
	require.Len(t, lines, 2)

	var action map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &action))
	indexAction := action["index"].(map[string]any)
	assert.Equal(t, "otel-traces-payment-app-2026.05.29", indexAction["_index"])

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &doc))
	// New field names
	assert.Equal(t, "process-payment", doc["name"])
	assert.Equal(t, "payment-svc", doc["serviceName"])
	assert.Equal(t, float64(1000000000), doc["durationNano"]) // 1s = 1e9 nanos
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
	cfg.BatchSize = 2

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewTraceWriter(client, cfg, zaptest.NewLogger(t))

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
	assert.Equal(t, 1, bulkCount)

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

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "payment-svc")

	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetName("process-payment")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 1, 0, time.UTC)))

	err = writer.WriteTraces(context.Background(), td)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app_id is required")
}
