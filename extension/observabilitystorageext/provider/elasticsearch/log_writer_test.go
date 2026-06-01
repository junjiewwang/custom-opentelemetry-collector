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
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap/zaptest"
)

func TestLogWriter_LogRecordToDoc_BasicFields(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// Create log data
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "auth-service")
	rl.Resource().Attributes().PutStr("app_id", "app-auth")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 100000000, time.UTC)))
	lr.SetSeverityText("ERROR")
	lr.SetSeverityNumber(plog.SeverityNumberError)
	lr.Body().SetStr("User login failed: invalid credentials")
	lr.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	lr.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	lr.Attributes().PutStr("user.id", "user-123")
	lr.Attributes().PutStr("error.type", "AuthenticationError")

	resource := extractResourceAttributes(rl.Resource())
	serviceName := getServiceNameFromResourceLogs(rl.Resource())
	doc := writer.logRecordToDoc(lr, resource, serviceName)

	// Verify basic fields
	assert.Equal(t, "ERROR", doc["severity"])
	assert.Equal(t, int32(17), doc["severity_number"]) // SeverityNumberError = 17
	assert.Equal(t, "auth-service", doc["service_name"])
	assert.Equal(t, "User login failed: invalid credentials", doc["body"])

	// app_id is set at WriteLogs level, not in logRecordToDoc;
	// however it is still present in the resource map
	res := doc["resource"].(map[string]any)
	assert.Equal(t, "app-auth", res["app_id"])

	// Trace context
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", doc["trace_id"])
	assert.Equal(t, "0102030405060708", doc["span_id"])

	// Attributes
	attrs := doc["attributes"].(map[string]any)
	assert.Equal(t, "user-123", attrs["user.id"])
	assert.Equal(t, "AuthenticationError", attrs["error.type"])
}

func TestLogWriter_LogRecordToDoc_NoTraceContext(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "standalone-svc")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	lr.SetSeverityText("INFO")
	lr.Body().SetStr("Application started")
	// No trace ID / span ID set (all zeros)

	resource := extractResourceAttributes(rl.Resource())
	doc := writer.logRecordToDoc(lr, resource, "standalone-svc")

	// Should NOT have trace_id/span_id fields when they are all zeros
	_, hasTraceID := doc["trace_id"]
	_, hasSpanID := doc["span_id"]
	assert.False(t, hasTraceID, "should not have trace_id for non-traced log")
	assert.False(t, hasSpanID, "should not have span_id for non-traced log")
}

func TestLogWriter_LogRecordToDoc_EmptyBody(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "svc")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	lr.SetSeverityText("DEBUG")
	// Body is left empty (ValueTypeEmpty)

	resource := extractResourceAttributes(rl.Resource())
	doc := writer.logRecordToDoc(lr, resource, "svc")

	_, hasBody := doc["body"]
	assert.False(t, hasBody, "empty body should not be included in doc")
}

func TestLogWriter_LogRecordToDoc_NoAttributes(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "svc")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	lr.Body().SetStr("simple log")
	// No attributes

	resource := extractResourceAttributes(rl.Resource())
	doc := writer.logRecordToDoc(lr, resource, "svc")

	_, hasAttrs := doc["attributes"]
	assert.False(t, hasAttrs, "should not include attributes field when empty")
}

func TestLogWriter_GetIndexName(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	ts := time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC)
	indexName := writer.getIndexName("auth-app", ts)
	assert.Equal(t, "otel-logs-auth-app-2026.01.15", indexName)
}

func TestLogWriter_GetIndexName_ZeroTime(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// Zero time should use current time (not panic)
	indexName := writer.getIndexName("fallback-app", time.Time{})
	assert.Contains(t, indexName, "otel-logs-fallback-app-")
	// Should be today's date
	today := time.Now().UTC().Format("2006.01.02")
	assert.Contains(t, indexName, today)
}

func TestLogWriter_WriteLogs_EndToEnd(t *testing.T) {
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

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// Create log data
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "web-server")
	rl.Resource().Attributes().PutStr("app_id", "web-app")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)))
	lr.SetSeverityText("WARN")
	lr.Body().SetStr("High memory usage detected")

	err = writer.WriteLogs(context.Background(), ld)
	require.NoError(t, err)

	// Verify bulk body
	require.NotEmpty(t, receivedBody)
	lines := strings.Split(strings.TrimSpace(string(receivedBody)), "\n")
	require.Len(t, lines, 2)

	var action map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &action))
	indexAction := action["index"].(map[string]any)
	assert.Equal(t, "otel-logs-web-app-2026.05.29", indexAction["_index"])

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &doc))
	assert.Equal(t, "WARN", doc["severity"])
	assert.Equal(t, "High memory usage detected", doc["body"])
	assert.Equal(t, "web-server", doc["service_name"])
}

func TestLogWriter_WriteLogs_MultipleRecords(t *testing.T) {
	var bulkCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_bulk" {
			bulkCalls++
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

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// Create multiple log records
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "multi-log")
	rl.Resource().Attributes().PutStr("app_id", "multi-log-app")

	sl := rl.ScopeLogs().AppendEmpty()
	for i := 0; i < 5; i++ {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, i, 0, time.UTC)))
		lr.Body().SetStr("log message " + string(rune('0'+i)))
	}

	err = writer.WriteLogs(context.Background(), ld)
	require.NoError(t, err)

	// 5 records with batch_size 2: 2 bulk calls triggered
	assert.Equal(t, 2, bulkCalls)

	// Flush remaining 1
	require.NoError(t, writer.Flush(context.Background()))
	assert.Equal(t, 3, bulkCalls)
}

func TestLogWriter_ServiceNameDefault(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// No service.name in resource
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	// Only set non-service.name attributes
	rl.Resource().Attributes().PutStr("host.name", "server-1")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	lr.Body().SetStr("test")

	resource := extractResourceAttributes(rl.Resource())
	serviceName := getServiceNameFromResourceLogs(rl.Resource())
	doc := writer.logRecordToDoc(lr, resource, serviceName)

	// Should default to "unknown"
	assert.Equal(t, "unknown", doc["service_name"])
}

func TestLogWriter_WriteLogs_RejectsWithoutAppID(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, zaptest.NewLogger(t))

	// Create log without app_id in resource attributes
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "web-server")
	// No app_id set

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)))
	lr.SetSeverityText("WARN")
	lr.Body().SetStr("High memory usage detected")

	err = writer.WriteLogs(context.Background(), ld)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app_id is required")
	assert.Contains(t, err.Error(), "app-level data isolation")
}
