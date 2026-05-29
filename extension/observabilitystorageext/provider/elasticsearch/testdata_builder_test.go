// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// ==================== Test Data Builders ====================
//
// Shared builders for constructing OTel pdata test fixtures.
// Used by both integration_test.go and unit tests.

// buildTestTraces creates test trace data with the current timestamp.
func buildTestTraces(serviceName, appID string) ptrace.Traces {
	return buildTestTracesWithTimestamp(serviceName, appID, time.Now())
}

// buildTestTracesWithTimestamp creates test trace data with a specific timestamp.
func buildTestTracesWithTimestamp(serviceName, appID string, ts time.Time) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", serviceName)
	rs.Resource().Attributes().PutStr("host.name", "test-host")
	rs.Resource().Attributes().PutStr("app_id", appID)

	ss := rs.ScopeSpans().AppendEmpty()

	// Root span
	span1 := ss.Spans().AppendEmpty()
	span1.SetTraceID(pcommon.TraceID([16]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}))
	span1.SetSpanID(pcommon.SpanID([8]byte{0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}))
	span1.SetName("GET /api/test")
	span1.SetKind(ptrace.SpanKindServer)
	span1.Status().SetCode(ptrace.StatusCodeOk)
	span1.SetStartTimestamp(pcommon.NewTimestampFromTime(ts.Add(-100 * time.Millisecond)))
	span1.SetEndTimestamp(pcommon.NewTimestampFromTime(ts))
	span1.Attributes().PutStr("http.method", "GET")
	span1.Attributes().PutInt("http.status_code", 200)

	// Add an event
	event := span1.Events().AppendEmpty()
	event.SetName("db.query")
	event.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-50 * time.Millisecond)))
	event.Attributes().PutStr("db.statement", "SELECT 1")

	// Child span
	span2 := ss.Spans().AppendEmpty()
	span2.SetTraceID(pcommon.TraceID([16]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}))
	span2.SetSpanID(pcommon.SpanID([8]byte{0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}))
	span2.SetParentSpanID(pcommon.SpanID([8]byte{0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}))
	span2.SetName("mysql.query")
	span2.SetKind(ptrace.SpanKindClient)
	span2.Status().SetCode(ptrace.StatusCodeOk)
	span2.SetStartTimestamp(pcommon.NewTimestampFromTime(ts.Add(-80 * time.Millisecond)))
	span2.SetEndTimestamp(pcommon.NewTimestampFromTime(ts.Add(-30 * time.Millisecond)))
	span2.Attributes().PutStr("db.system", "mysql")

	return td
}

// buildTestMetrics creates test metric data with the current timestamp.
func buildTestMetrics(serviceName, appID string) pmetric.Metrics {
	return buildTestMetricsWithTimestamp(serviceName, appID, time.Now())
}

// buildTestMetricsWithTimestamp creates test metric data with a specific timestamp.
func buildTestMetricsWithTimestamp(serviceName, appID string, ts time.Time) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", serviceName)
	rm.Resource().Attributes().PutStr("app_id", appID)

	sm := rm.ScopeMetrics().AppendEmpty()

	// Gauge metric
	gauge := sm.Metrics().AppendEmpty()
	gauge.SetName("system.cpu.usage")
	gauge.SetEmptyGauge()
	gaugeDp := gauge.Gauge().DataPoints().AppendEmpty()
	gaugeDp.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	gaugeDp.SetDoubleValue(0.73)
	gaugeDp.Attributes().PutStr("cpu", "cpu0")

	// Counter metric
	counter := sm.Metrics().AppendEmpty()
	counter.SetName("http.server.request.total")
	counter.SetEmptySum()
	counter.Sum().SetIsMonotonic(true)
	counterDp := counter.Sum().DataPoints().AppendEmpty()
	counterDp.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	counterDp.SetIntValue(98765)
	counterDp.Attributes().PutStr("method", "GET")

	// Histogram metric
	histo := sm.Metrics().AppendEmpty()
	histo.SetName("http.server.duration")
	histo.SetEmptyHistogram()
	histoDp := histo.Histogram().DataPoints().AppendEmpty()
	histoDp.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	histoDp.SetSum(15678.5)
	histoDp.SetCount(500)
	histoDp.ExplicitBounds().Append(5, 10, 25, 50, 100, 250, 500, 1000)
	histoDp.BucketCounts().Append(10, 50, 100, 150, 100, 50, 25, 10, 5)

	return md
}

// buildTestLogs creates test log data with the current timestamp.
func buildTestLogs(serviceName, appID string) plog.Logs {
	return buildTestLogsWithTimestamp(serviceName, appID, time.Now())
}

// buildTestLogsWithTimestamp creates test log data with a specific timestamp.
func buildTestLogsWithTimestamp(serviceName, appID string, ts time.Time) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", serviceName)
	rl.Resource().Attributes().PutStr("app_id", appID)

	sl := rl.ScopeLogs().AppendEmpty()

	// INFO log with trace context
	lr1 := sl.LogRecords().AppendEmpty()
	lr1.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-2 * time.Second)))
	lr1.SetObservedTimestamp(pcommon.NewTimestampFromTime(ts.Add(-2 * time.Second)))
	lr1.SetSeverityText("INFO")
	lr1.SetSeverityNumber(plog.SeverityNumberInfo)
	lr1.Body().SetStr("Test log entry for integration test")
	lr1.SetTraceID(pcommon.TraceID([16]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}))
	lr1.SetSpanID(pcommon.SpanID([8]byte{0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}))
	lr1.Attributes().PutStr("user.id", "user-test")

	// ERROR log without trace context
	lr2 := sl.LogRecords().AppendEmpty()
	lr2.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-1 * time.Second)))
	lr2.SetObservedTimestamp(pcommon.NewTimestampFromTime(ts.Add(-1 * time.Second)))
	lr2.SetSeverityText("ERROR")
	lr2.SetSeverityNumber(plog.SeverityNumberError)
	lr2.Body().SetStr("Connection timeout error for integration test")
	lr2.Attributes().PutStr("error.type", "TimeoutException")

	// WARN log
	lr3 := sl.LogRecords().AppendEmpty()
	lr3.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	lr3.SetObservedTimestamp(pcommon.NewTimestampFromTime(ts))
	lr3.SetSeverityText("WARN")
	lr3.SetSeverityNumber(plog.SeverityNumberWarn)
	lr3.Body().SetStr("High memory usage warning")
	lr3.Attributes().PutDouble("memory.usage_percent", 85.3)

	return ld
}
