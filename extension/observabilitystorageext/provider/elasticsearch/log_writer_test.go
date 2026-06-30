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

	doc := writer.logRecordToDoc(lr, rl.Resource())

	assert.Equal(t, "ERROR", doc.SeverityText)
	assert.Equal(t, int32(17), doc.SeverityNumber)
	assert.Equal(t, "auth-service", doc.ServiceName)
	assert.Equal(t, "User login failed: invalid credentials", doc.Body)
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", doc.TraceID)
	assert.Equal(t, "0102030405060708", doc.SpanID)
	assert.Equal(t, "app-auth", doc.Resource["app_id"])
	assert.Equal(t, "user-123", doc.Attributes["user.id"])
	assert.Equal(t, "AuthenticationError", doc.Attributes["error.type"])
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
	rl.Resource().Attributes().PutStr("service.name", "test-svc")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetSeverityText("INFO")
	lr.Body().SetStr("simple log")

	doc := writer.logRecordToDoc(lr, rl.Resource())

	assert.Empty(t, doc.TraceID, "no trace context set")
	assert.Empty(t, doc.SpanID, "no span context set")
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

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "payment-svc")
	rl.Resource().Attributes().PutStr("app_id", "payment-app")

	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	lr.SetSeverityText("WARN")
	lr.Body().SetStr("Payment timeout")

	err = writer.WriteLogs(context.Background(), ld)
	require.NoError(t, err)

	require.NotEmpty(t, receivedBody)
	lines := strings.Split(strings.TrimSpace(string(receivedBody)), "\n")
	require.Len(t, lines, 2)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &doc))
	assert.Equal(t, "WARN", doc["severityText"])
	assert.Equal(t, "payment-svc", doc["serviceName"])
	assert.Equal(t, "Payment timeout", doc["body"])
}
