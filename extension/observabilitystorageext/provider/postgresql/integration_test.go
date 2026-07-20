// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap/zaptest"
)

// ==================== Integration Test Configuration ====================
//
// These tests require a running PostgreSQL instance. They are gated by
// the PG_INTEGRATION_TEST environment variable to prevent accidental execution.
//
// Configuration is passed entirely through environment variables:
//
//   PG_INTEGRATION_TEST=true     # Required: enables integration tests
//   PG_HOST=localhost            # PG host (default: localhost)
//   PG_PORT=5432                 # PG port (default: 5432)
//   PG_USER=postgres             # PG username (default: postgres)
//   PG_PASSWORD=                 # PG password (default: empty)
//   PG_DATABASE=otel_test        # PG database name (default: otel_test)
//   PG_SSLMODE=disable           # SSL mode (default: disable)
//
// Example:
//
//   PG_INTEGRATION_TEST=true PG_HOST=localhost PG_PORT=5432 \
//     PG_USER=postgres PG_PASSWORD=mypass PG_DATABASE=otel_test \
//     go test ./extension/observabilitystorageext/provider/postgresql/... -run TestIntegration -v
//
// With DSN override (takes precedence over individual variables):
//
//   PG_INTEGRATION_TEST=true PG_DSN="postgres://user:pass@host:5432/db?sslmode=disable" \
//     go test ./extension/observabilitystorageext/provider/postgresql/... -run TestIntegration -v

// ==================== Test Helpers ====================

// skipIfNoPG skips the test if PG_INTEGRATION_TEST is not set.
func skipIfNoPG(t *testing.T) {
	t.Helper()
	if os.Getenv("PG_INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test: set PG_INTEGRATION_TEST=true to enable")
	}
}

// envOrDefault returns the environment variable value or a default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// integrationConfig builds the PG provider config from environment variables.
// All sensitive values come from environment variables, no hardcoded credentials.
func integrationConfig() *Config {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		host := envOrDefault("PG_HOST", "localhost")
		port := envOrDefault("PG_PORT", "5432")
		user := envOrDefault("PG_USER", "postgres")
		password := envOrDefault("PG_PASSWORD", "")
		database := envOrDefault("PG_DATABASE", "otel_test")
		sslmode := envOrDefault("PG_SSLMODE", "disable")
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
			user, password, host, port, database, sslmode)
	}

	return &Config{
		DSN:             dsn,
		MaxConns:        5,
		MinConns:        2,
		MaxConnLifetime: 5 * time.Minute,
		MaxConnIdleTime: 1 * time.Minute,
		BatchSize:       100,
		FlushInterval:   1 * time.Second,
		MaxRetries:      2,
		UseTimescaleDB:  false,
		Traces: TableConfig{
			TableName:         "otel_traces",
			PartitionInterval: 24 * time.Hour,
		},
		Metrics: TableConfig{
			TableName:         "otel_metrics",
			PartitionInterval: 6 * time.Hour,
		},
		Logs: TableConfig{
			TableName:         "otel_logs",
			PartitionInterval: 24 * time.Hour,
		},
	}
}

// setupTestProvider creates and starts a PG provider for integration tests.
func setupTestProvider(t *testing.T) *Provider {
	t.Helper()
	skipIfNoPG(t)

	config := integrationConfig()
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	provider, err := NewProvider(config, logger)
	require.NoError(t, err, "NewProvider should not fail")

	err = provider.Start(ctx)
	require.NoError(t, err, "Provider.Start should not fail")

	t.Cleanup(func() {
		err := provider.Shutdown(ctx)
		assert.NoError(t, err, "Provider.Shutdown should not fail")
	})

	return provider
}

// ═══════════════════════════════════════════════════
// Test: Connectivity
// ═══════════════════════════════════════════════════

func TestIntegration_Connectivity(t *testing.T) {
	skipIfNoPG(t)

	config := integrationConfig()
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	client, err := NewClient(config, logger)
	require.NoError(t, err, "NewClient should not fail")
	defer client.Close()

	// Ping
	err = client.Ping(ctx)
	require.NoError(t, err, "Ping should succeed")
	t.Log("✅ Ping successful")

	// Version
	version, err := client.GetVersion(ctx)
	require.NoError(t, err, "GetVersion should succeed")
	assert.Contains(t, version, "PostgreSQL")
	t.Logf("✅ PostgreSQL version: %s", version)

	// TimescaleDB check
	hasTS, err := client.HasTimescaleDB(ctx)
	require.NoError(t, err, "HasTimescaleDB check should not error")
	t.Logf("✅ TimescaleDB available: %v", hasTS)

	// Database size
	size, err := client.DatabaseSize(ctx)
	require.NoError(t, err, "DatabaseSize should succeed")
	t.Logf("✅ Database size: %d bytes (%.2f MB)", size, float64(size)/1024/1024)
}

// ═══════════════════════════════════════════════════
// Test: Schema Migration
// ═══════════════════════════════════════════════════

func TestIntegration_SchemaMigration(t *testing.T) {
	skipIfNoPG(t)

	config := integrationConfig()
	logger := zaptest.NewLogger(t)

	migrator := NewMigrator(config.DSN, logger)

	err := migrator.Up()
	require.NoError(t, err, "Migration Up should succeed")

	version, dirty, err := migrator.Version()
	require.NoError(t, err, "Version check should succeed")
	assert.False(t, dirty, "Schema should not be dirty")
	t.Logf("✅ Migration version: %d, dirty: %v", version, dirty)
}

// ═══════════════════════════════════════════════════
// Test: Provider Full Lifecycle
// ═══════════════════════════════════════════════════

func TestIntegration_ProviderFullLifecycle(t *testing.T) {
	skipIfNoPG(t)

	config := integrationConfig()
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	provider, err := NewProvider(config, logger)
	require.NoError(t, err)

	// Start
	err = provider.Start(ctx)
	require.NoError(t, err, "Provider.Start should succeed")

	// HealthCheck
	healthy, msg, details := provider.HealthCheck(ctx)
	assert.True(t, healthy, "HealthCheck should report healthy")
	assert.Equal(t, "connected", msg)
	assert.NotEmpty(t, details)
	t.Logf("✅ HealthCheck: healthy=%v, msg=%s, details=%v", healthy, msg, details)

	// Shutdown
	err = provider.Shutdown(ctx)
	require.NoError(t, err, "Provider.Shutdown should succeed")
	t.Log("✅ Full lifecycle completed")
}

// ═══════════════════════════════════════════════════
// Test: Trace Write + Read
// ═══════════════════════════════════════════════════

func TestIntegration_TraceWriter(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	td := buildTestTraces("integration-test-svc", "app-001")

	err := provider.WriteTraces(ctx, td)
	require.NoError(t, err, "WriteTraces should succeed")

	err = provider.FlushTraces(ctx)
	require.NoError(t, err, "FlushTraces should succeed")
	t.Log("✅ Traces written and flushed")
}

func TestIntegration_TraceReader_WriteAndQuery(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()
	now := time.Now()

	// Write test data
	td := buildTestTracesWithTimestamp("trace-reader-svc", "app-002", now.Add(-30*time.Second))
	err := provider.WriteTraces(ctx, td)
	require.NoError(t, err)
	err = provider.FlushTraces(ctx)
	require.NoError(t, err)

	// SearchTraces
	result, err := provider.TraceReader().SearchTraces(ctx, TraceQuery{
		ServiceName: "trace-reader-svc",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		},
		Limit: 10,
	})
	require.NoError(t, err, "SearchTraces should succeed")
	assert.Greater(t, result.Total, int64(0), "Should find at least one trace")
	t.Logf("✅ SearchTraces: %d traces (total: %d)", len(result.Traces), result.Total)

	// GetTrace
	if result.Total > 0 {
		traceID := result.Traces[0].TraceID
		trace, err := provider.TraceReader().GetTrace(ctx, traceID)
		require.NoError(t, err, "GetTrace should succeed")
		assert.NotEmpty(t, trace.Spans, "Trace should have spans")
		t.Logf("✅ GetTrace: %d spans", len(trace.Spans))
	}

	// GetServices
	services, err := provider.TraceReader().GetServices(ctx, TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	})
	require.NoError(t, err, "GetServices should succeed")
	assert.NotEmpty(t, services, "Should find at least one service")
	t.Logf("✅ GetServices: %d services", len(services))

	// GetOperations
	operations, err := provider.TraceReader().GetOperations(ctx, "trace-reader-svc", TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	})
	require.NoError(t, err, "GetOperations should succeed")
	t.Logf("✅ GetOperations: %d operations", len(operations))

	// GetDependencies
	deps, err := provider.TraceReader().GetDependencies(ctx, TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	})
	require.NoError(t, err, "GetDependencies should succeed")
	t.Logf("✅ GetDependencies: %d dependencies", len(deps))
}

// ═══════════════════════════════════════════════════
// Test: Metric Write + Read
// ═══════════════════════════════════════════════════

func TestIntegration_MetricWriter(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	md := buildTestMetrics("integration-test-svc", "app-001")

	err := provider.WriteMetrics(ctx, md)
	require.NoError(t, err, "WriteMetrics should succeed")

	err = provider.FlushMetrics(ctx)
	require.NoError(t, err, "FlushMetrics should succeed")
	t.Log("✅ Metrics written and flushed")
}

func TestIntegration_MetricReader_WriteAndQuery(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()
	now := time.Now()

	// Write test data
	md := buildTestMetricsWithTimestamp("metric-reader-svc", "app-003", now.Add(-30*time.Second))
	err := provider.WriteMetrics(ctx, md)
	require.NoError(t, err)
	err = provider.FlushMetrics(ctx)
	require.NoError(t, err)

	// ListMetricNames
	names, err := provider.MetricReader().ListMetricNames(ctx, TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	})
	require.NoError(t, err, "ListMetricNames should succeed")
	assert.NotEmpty(t, names, "Should find metric names")
	t.Logf("✅ ListMetricNames: %v", names)

	// Query (instant)
	if len(names) > 0 {
		queryResult, err := provider.MetricReader().Query(ctx, MetricQuery{
			MetricName: names[0],
			Time:       now.Add(1 * time.Hour),
		})
		require.NoError(t, err, "MetricQuery should succeed")
		t.Logf("✅ MetricQuery: %d samples", len(queryResult.Samples))
	}

	// QueryRange
	if len(names) > 0 {
		rangeResult, err := provider.MetricReader().QueryRange(ctx, MetricRangeQuery{
			MetricName: names[0],
			TimeRange: TimeRange{
				Start: now.Add(-1 * time.Hour),
				End:   now.Add(1 * time.Hour),
			},
			Step: 1 * time.Minute,
		})
		require.NoError(t, err, "MetricQueryRange should succeed")
		t.Logf("✅ MetricQueryRange: %d series", len(rangeResult.Series))
	}

	// ListLabelNames
	labelNames, err := provider.MetricReader().ListLabelNames(ctx, TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	}, "")
	require.NoError(t, err, "ListLabelNames should succeed")
	t.Logf("✅ ListLabelNames: %v", labelNames)

	// ListLabelValues
	if len(labelNames) > 0 {
		values, err := provider.MetricReader().ListLabelValues(ctx, labelNames[0], TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		})
		require.NoError(t, err, "ListLabelValues should succeed")
		t.Logf("✅ ListLabelValues(%s): %v", labelNames[0], values)
	}
}

// ═══════════════════════════════════════════════════
// Test: Log Write + Read
// ═══════════════════════════════════════════════════

func TestIntegration_LogWriter(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	ld := buildTestLogs("integration-test-svc", "app-001")

	err := provider.WriteLogs(ctx, ld)
	require.NoError(t, err, "WriteLogs should succeed")

	err = provider.FlushLogs(ctx)
	require.NoError(t, err, "FlushLogs should succeed")
	t.Log("✅ Logs written and flushed")
}

func TestIntegration_LogReader_WriteAndQuery(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()
	now := time.Now()

	// Write test data
	ld := buildTestLogsWithTimestamp("log-reader-svc", "app-004", now.Add(-30*time.Second))
	err := provider.WriteLogs(ctx, ld)
	require.NoError(t, err)
	err = provider.FlushLogs(ctx)
	require.NoError(t, err)

	// SearchLogs
	result, err := provider.LogReader().SearchLogs(ctx, LogQuery{
		ServiceName: "log-reader-svc",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		},
		Limit: 10,
	})
	require.NoError(t, err, "SearchLogs should succeed")
	assert.Greater(t, result.Total, int64(0), "Should find logs")
	t.Logf("✅ SearchLogs: %d logs (total: %d)", len(result.Logs), result.Total)

	// Full-text search
	ftsResult, err := provider.LogReader().SearchLogs(ctx, LogQuery{
		Query: "connection timeout",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		},
		Limit: 10,
	})
	require.NoError(t, err, "SearchLogs (FTS) should succeed")
	t.Logf("✅ FTS 'connection timeout': %d logs", ftsResult.Total)

	// GetLogStats
	stats, err := provider.LogReader().GetLogStats(ctx, LogStatsQuery{
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		},
	})
	require.NoError(t, err, "GetLogStats should succeed")
	t.Logf("✅ LogStats: total=%d, severity=%v", stats.TotalCount, stats.SeverityBreakdown)

	// ListLogFields
	fields, err := provider.LogReader().ListLogFields(ctx, TimeRange{
		Start: now.Add(-1 * time.Hour),
		End:   now.Add(1 * time.Hour),
	})
	require.NoError(t, err, "ListLogFields should succeed")
	t.Logf("✅ ListLogFields: %d fields", len(fields))
}

// ═══════════════════════════════════════════════════
// Test: Admin Operations
// ═══════════════════════════════════════════════════

func TestIntegration_Admin_GetStatus(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	admin := provider.Admin()
	require.NotNil(t, admin, "Admin should not be nil")

	status, err := admin.GetStatus(ctx)
	require.NoError(t, err, "GetStatus should succeed")
	assert.NotEmpty(t, status)
	t.Logf("✅ GetStatus: %v", status)
}

func TestIntegration_Admin_GetIndicesStats(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	admin := provider.Admin()
	require.NotNil(t, admin)

	stats, err := admin.GetIndicesStats(ctx)
	require.NoError(t, err, "GetIndicesStats should succeed")
	t.Logf("✅ GetIndicesStats: %d entries", len(stats))
	for _, s := range stats {
		t.Logf("   - %v", s)
	}
}

// ═══════════════════════════════════════════════════
// Test: Partition Management
// ═══════════════════════════════════════════════════

func TestIntegration_PartitionManagement(t *testing.T) {
	provider := setupTestProvider(t)
	ctx := context.Background()

	sql := `
		SELECT schemaname || '.' || tablename AS full_name
		FROM pg_tables
		WHERE tablename LIKE 'otel_%_p%'
		ORDER BY tablename
	`
	rows, err := provider.client.Query(ctx, sql)
	require.NoError(t, err, "Query partitions should succeed")
	defer rows.Close()

	var partitions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			partitions = append(partitions, name)
		}
	}
	assert.NotEmpty(t, partitions, "Should have at least one partition")
	t.Logf("✅ Partitions (%d):", len(partitions))
	for _, p := range partitions {
		t.Logf("   - %s", p)
	}
}

// ═══════════════════════════════════════════════════
// Test: Write Performance
// ═══════════════════════════════════════════════════

func TestIntegration_WritePerformance(t *testing.T) {
	skipIfNoPG(t)

	config := integrationConfig()
	config.BatchSize = 1000
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	provider, err := NewProvider(config, logger)
	require.NoError(t, err)
	require.NoError(t, provider.Start(ctx))
	defer provider.Shutdown(ctx)

	// Bulk write: 10 batches x 5 spans = 50 spans
	batchCount := 10
	spansPerBatch := 5
	start := time.Now()
	for i := 0; i < batchCount; i++ {
		td := buildBulkTraces(i, spansPerBatch)
		err := provider.WriteTraces(ctx, td)
		require.NoError(t, err, "WriteTraces batch %d should succeed", i)
	}
	require.NoError(t, provider.FlushTraces(ctx))
	writeDuration := time.Since(start)

	totalSpans := batchCount * spansPerBatch
	t.Logf("✅ Write %d spans: %v (%.0f spans/sec)",
		totalSpans, writeDuration, float64(totalSpans)/writeDuration.Seconds())

	// Read performance
	start = time.Now()
	now := time.Now()
	_, err = provider.TraceReader().SearchTraces(ctx, TraceQuery{
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now.Add(1 * time.Hour),
		},
		Limit: 20,
	})
	require.NoError(t, err, "SearchTraces should succeed")
	readDuration := time.Since(start)
	t.Logf("✅ SearchTraces (top 20): %v", readDuration)
}

// ═══════════════════════════════════════════════════
// Test Data Builders (shared by all integration tests)
// ═══════════════════════════════════════════════════

func buildTestTraces(serviceName, appID string) ptrace.Traces {
	return buildTestTracesWithTimestamp(serviceName, appID, time.Now())
}

func buildTestTracesWithTimestamp(serviceName, appID string, ts time.Time) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", serviceName)
	rs.Resource().Attributes().PutStr("app.id", appID)

	ss := rs.ScopeSpans().AppendEmpty()

	// Root span
	root := ss.Spans().AppendEmpty()
	root.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	root.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	root.SetName("HTTP GET /api/users")
	root.SetKind(ptrace.SpanKindServer)
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(ts.Add(-100 * time.Millisecond)))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(ts))
	root.Status().SetCode(ptrace.StatusCodeOk)
	root.Attributes().PutStr("http.method", "GET")
	root.Attributes().PutStr("http.url", "/api/users")
	root.Attributes().PutInt("http.status_code", 200)

	// Child span
	child := ss.Spans().AppendEmpty()
	child.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	child.SetSpanID(pcommon.SpanID([8]byte{2, 3, 4, 5, 6, 7, 8, 9}))
	child.SetParentSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	child.SetName("DB SELECT users")
	child.SetKind(ptrace.SpanKindClient)
	child.SetStartTimestamp(pcommon.NewTimestampFromTime(ts.Add(-80 * time.Millisecond)))
	child.SetEndTimestamp(pcommon.NewTimestampFromTime(ts.Add(-10 * time.Millisecond)))
	child.Status().SetCode(ptrace.StatusCodeOk)
	child.Attributes().PutStr("db.system", "postgresql")

	return td
}

func buildTestMetrics(serviceName, appID string) pmetric.Metrics {
	return buildTestMetricsWithTimestamp(serviceName, appID, time.Now())
}

func buildTestMetricsWithTimestamp(serviceName, appID string, ts time.Time) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", serviceName)
	rm.Resource().Attributes().PutStr("app.id", appID)

	sm := rm.ScopeMetrics().AppendEmpty()

	// Gauge
	gauge := sm.Metrics().AppendEmpty()
	gauge.SetName("system.cpu.usage")
	gauge.SetEmptyGauge()
	dp1 := gauge.Gauge().DataPoints().AppendEmpty()
	dp1.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	dp1.SetDoubleValue(0.75)
	dp1.Attributes().PutStr("cpu", "cpu0")
	dp1.Attributes().PutStr("state", "user")

	// Sum (counter)
	counter := sm.Metrics().AppendEmpty()
	counter.SetName("http.request.count")
	counter.SetEmptySum()
	counter.Sum().SetIsMonotonic(true)
	dp2 := counter.Sum().DataPoints().AppendEmpty()
	dp2.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	dp2.SetIntValue(42)
	dp2.Attributes().PutStr("method", "GET")
	dp2.Attributes().PutStr("path", "/api/users")

	// Histogram
	hist := sm.Metrics().AppendEmpty()
	hist.SetName("http.request.duration")
	hist.SetEmptyHistogram()
	dp3 := hist.Histogram().DataPoints().AppendEmpty()
	dp3.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	dp3.SetCount(100)
	dp3.SetSum(2500.0)
	dp3.SetMin(5.0)
	dp3.SetMax(150.0)
	dp3.Attributes().PutStr("method", "GET")

	return md
}

func buildTestLogs(serviceName, appID string) plog.Logs {
	return buildTestLogsWithTimestamp(serviceName, appID, time.Now())
}

func buildTestLogsWithTimestamp(serviceName, appID string, ts time.Time) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", serviceName)
	rl.Resource().Attributes().PutStr("app.id", appID)

	sl := rl.ScopeLogs().AppendEmpty()

	// INFO
	lr1 := sl.LogRecords().AppendEmpty()
	lr1.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-5 * time.Second)))
	lr1.SetSeverityNumber(plog.SeverityNumberInfo)
	lr1.SetSeverityText("INFO")
	lr1.Body().SetStr("Starting HTTP server on port 8080")
	lr1.Attributes().PutStr("component", "server")

	// WARN
	lr2 := sl.LogRecords().AppendEmpty()
	lr2.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-3 * time.Second)))
	lr2.SetSeverityNumber(plog.SeverityNumberWarn)
	lr2.SetSeverityText("WARN")
	lr2.Body().SetStr("Connection pool running low: 2/20 available")
	lr2.Attributes().PutStr("component", "db")

	// ERROR (for FTS testing)
	lr3 := sl.LogRecords().AppendEmpty()
	lr3.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(-1 * time.Second)))
	lr3.SetSeverityNumber(plog.SeverityNumberError)
	lr3.SetSeverityText("ERROR")
	lr3.Body().SetStr("Database connection timeout after 5000ms: failed to acquire connection")
	lr3.Attributes().PutStr("component", "db")
	lr3.Attributes().PutStr("error.type", "ConnectionTimeoutError")

	return ld
}

func buildBulkTraces(batchIdx, spansPerTrace int) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", fmt.Sprintf("perf-service-%d", batchIdx%3))
	rs.Resource().Attributes().PutStr("app.id", "perf-app")

	ss := rs.ScopeSpans().AppendEmpty()
	now := time.Now()
	traceID := pcommon.TraceID([16]byte{byte(batchIdx), 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	for i := 0; i < spansPerTrace; i++ {
		span := ss.Spans().AppendEmpty()
		span.SetTraceID(traceID)
		span.SetSpanID(pcommon.SpanID([8]byte{byte(batchIdx), byte(i), 3, 4, 5, 6, 7, 8}))
		if i > 0 {
			span.SetParentSpanID(pcommon.SpanID([8]byte{byte(batchIdx), byte(i - 1), 3, 4, 5, 6, 7, 8}))
		}
		span.SetName(fmt.Sprintf("operation-%d", i))
		span.SetKind(ptrace.SpanKindInternal)
		startTime := now.Add(-time.Duration(100-i*10) * time.Millisecond)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(startTime.Add(time.Duration(5+i*2) * time.Millisecond)))
		span.Status().SetCode(ptrace.StatusCodeOk)
	}

	return td
}
