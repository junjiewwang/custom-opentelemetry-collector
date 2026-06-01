// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ==================== Integration Test Configuration ====================
//
// These tests require a running Elasticsearch instance. They are gated by
// the ES_INTEGRATION_TEST environment variable to prevent accidental execution.
//
// Configuration is passed entirely through environment variables:
//
//   ES_INTEGRATION_TEST=true     # Required: enables integration tests
//   ES_HOST=localhost            # ES host (default: localhost)
//   ES_PORT=9200                 # ES port (default: 9200)
//   ES_USERNAME=                 # ES username (default: empty, no auth)
//   ES_PASSWORD=                 # ES password (default: empty)
//   ES_SCHEME=http              # URL scheme (default: http)
//   ES_INDEX_PREFIX=test-otel   # Index prefix for test isolation (default: test-otel)
//
// Example:
//
//   ES_INTEGRATION_TEST=true ES_HOST=localhost ES_PORT=9200 \
//     go test ./extension/observabilitystorageext/provider/elasticsearch/... -run TestIntegration -v
//
// With authentication:
//
//   ES_INTEGRATION_TEST=true ES_HOST=my-es.example.com ES_PORT=9200 \
//     ES_USERNAME=elastic ES_PASSWORD=changeme \
//     go test ./extension/observabilitystorageext/provider/elasticsearch/... -run TestIntegration -v

// ==================== Test Helpers ====================

// skipIfNoES skips the test if ES_INTEGRATION_TEST is not set.
func skipIfNoES(t *testing.T) {
	t.Helper()
	if os.Getenv("ES_INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test: set ES_INTEGRATION_TEST=true to enable")
	}
}

// envOrDefault returns the environment variable value or a default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// integrationConfig builds the ES provider config from environment variables.
// All sensitive values come from environment variables, no hardcoded credentials.
func integrationConfig() *Config {
	scheme := envOrDefault("ES_SCHEME", "http")
	host := envOrDefault("ES_HOST", "localhost")
	port := envOrDefault("ES_PORT", "9200")
	username := envOrDefault("ES_USERNAME", "")
	password := envOrDefault("ES_PASSWORD", "")
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")

	return &Config{
		Addresses:     []string{fmt.Sprintf("%s://%s:%s", scheme, host, port)},
		Username:      username,
		Password:      password,
		BatchSize:     100,
		FlushInterval: 1 * time.Second,
		MaxRetries:    3,
		Traces: IndexConfig{
			IndexPrefix:     indexPrefix + "-traces",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       24 * time.Hour,
			RefreshInterval: "1s",
		},
		Metrics: IndexConfig{
			IndexPrefix:     indexPrefix + "-metrics",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       24 * time.Hour,
			RefreshInterval: "1s",
		},
		Logs: IndexConfig{
			IndexPrefix:     indexPrefix + "-logs",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       24 * time.Hour,
			RefreshInterval: "1s",
		},
	}
}

// setupTestClient creates and verifies an ES client for integration tests.
func setupTestClient(t *testing.T) (*Client, *Config) {
	t.Helper()
	skipIfNoES(t)

	cfg := integrationConfig()
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err, "failed to create ES client")

	// Verify connectivity before running tests.
	err = client.Ping(context.Background())
	require.NoError(t, err, "ES cluster is not reachable — check ES_HOST/ES_PORT/ES_USERNAME/ES_PASSWORD")

	return client, cfg
}

// cleanupTestIndices deletes all test indices for a clean slate.
// Uses ListIndices + DeleteIndex to work around ES action.destructive_requires_name=true.
func cleanupTestIndices(t *testing.T, client *Client, indexPrefix string) {
	t.Helper()
	ctx := context.Background()
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-traces-*")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-metrics-*")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-logs-*")
}

// ==================== Connectivity Tests ====================

func TestIntegration_Ping(t *testing.T) {
	client, _ := setupTestClient(t)
	err := client.Ping(context.Background())
	require.NoError(t, err, "should be able to ping ES cluster")
	t.Log("✅ ES cluster is reachable")
}

func TestIntegration_ClusterHealth(t *testing.T) {
	client, _ := setupTestClient(t)

	health, err := client.ClusterHealth(context.Background())
	require.NoError(t, err)

	status, _ := health["status"].(string)
	clusterName, _ := health["cluster_name"].(string)
	t.Logf("✅ Cluster: %s, Status: %s", clusterName, status)
	assert.Contains(t, []string{"green", "yellow", "red"}, status)
}

// ==================== Writer Tests ====================

func TestIntegration_TraceWriter(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)

	writer := NewTraceWriter(client, cfg, logger)

	td := buildTestTraces("integration-trace-svc", "test-app-001")

	err := writer.WriteTraces(context.Background(), td)
	require.NoError(t, err, "WriteTraces should succeed")

	err = writer.Flush(context.Background())
	require.NoError(t, err, "Flush should succeed")

	t.Logf("✅ Successfully wrote trace spans to ES (index prefix: %s)", cfg.Traces.IndexPrefix)
}

func TestIntegration_MetricWriter(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)

	writer := NewMetricWriter(client, cfg, logger)

	md := buildTestMetrics("integration-metric-svc", "test-app-001")

	err := writer.WriteMetrics(context.Background(), md)
	require.NoError(t, err, "WriteMetrics should succeed")

	err = writer.Flush(context.Background())
	require.NoError(t, err, "Flush should succeed")

	t.Logf("✅ Successfully wrote metric docs to ES (index prefix: %s)", cfg.Metrics.IndexPrefix)
}

func TestIntegration_LogWriter(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)

	writer := NewLogWriter(client, cfg, logger)

	ld := buildTestLogs("integration-log-svc", "test-app-001")

	err := writer.WriteLogs(context.Background(), ld)
	require.NoError(t, err, "WriteLogs should succeed")

	err = writer.Flush(context.Background())
	require.NoError(t, err, "Flush should succeed")

	t.Logf("✅ Successfully wrote log records to ES (index prefix: %s)", cfg.Logs.IndexPrefix)
}

// ==================== Provider Lifecycle Tests ====================

func TestIntegration_ProviderFullLifecycle(t *testing.T) {
	skipIfNoES(t)

	cfg := integrationConfig()
	logger, _ := zap.NewDevelopment()

	provider, err := NewProvider(cfg, logger)
	require.NoError(t, err, "NewProvider should succeed")

	// Start
	err = provider.Start(context.Background())
	require.NoError(t, err, "Provider.Start should succeed")
	t.Log("✅ Provider started: Ping OK, Schema initialized")

	// Health check
	healthy, msg, details := provider.HealthCheck(context.Background())
	require.True(t, healthy, "HealthCheck should be healthy")
	t.Logf("✅ HealthCheck: %s (details: %v)", msg, details)

	// Write traces
	td := buildTestTraces("lifecycle-test-svc", "lifecycle-app")
	err = provider.WriteTraces(context.Background(), td)
	require.NoError(t, err, "WriteTraces should succeed")
	t.Log("✅ WriteTraces through Provider succeeded")

	// Write metrics
	md := buildTestMetrics("lifecycle-test-svc", "lifecycle-app")
	err = provider.WriteMetrics(context.Background(), md)
	require.NoError(t, err, "WriteMetrics should succeed")
	t.Log("✅ WriteMetrics through Provider succeeded")

	// Write logs
	ld := buildTestLogs("lifecycle-test-svc", "lifecycle-app")
	err = provider.WriteLogs(context.Background(), ld)
	require.NoError(t, err, "WriteLogs should succeed")
	t.Log("✅ WriteLogs through Provider succeeded")

	// Shutdown (flushes all buffers)
	err = provider.Shutdown(context.Background())
	require.NoError(t, err, "Provider.Shutdown should succeed")
	t.Log("✅ Provider shutdown completed (all buffers flushed)")
}

// ==================== Retention / Purge Tests ====================

func TestIntegration_Purge(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up test indices first for a known state
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	cleanupTestIndices(t, client, indexPrefix)

	// Re-create index templates after cleanup
	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err, "InitSchema should succeed")

	// Write some trace data with known timestamps
	writer := NewTraceWriter(client, cfg, logger)

	// Write "old" traces (2 hours ago)
	oldTraces := buildTestTracesWithTimestamp("purge-test-svc", "purge-app", time.Now().Add(-2*time.Hour))
	err = writer.WriteTraces(ctx, oldTraces)
	require.NoError(t, err, "WriteTraces (old) should succeed")

	// Write "recent" traces (just now)
	recentTraces := buildTestTracesWithTimestamp("purge-test-svc", "purge-app", time.Now())
	err = writer.WriteTraces(ctx, recentTraces)
	require.NoError(t, err, "WriteTraces (recent) should succeed")

	// Flush and refresh to make data searchable
	err = writer.Flush(ctx)
	require.NoError(t, err, "Flush should succeed")

	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err, "RefreshIndex should succeed")

	// Verify both docs are present
	totalBefore, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Total trace docs before purge: %d", totalBefore)
	require.GreaterOrEqual(t, totalBefore, int64(2), "should have at least 2 trace docs")

	// Purge data older than 1 hour
	provider, err := NewProvider(cfg, logger)
	require.NoError(t, err)
	provider.client = client // Reuse the already-connected client

	purgeThreshold := time.Now().Add(-1 * time.Hour)
	deleted, err := provider.Purge(ctx, cfg.Traces.IndexPrefix+"-*", "start_time", purgeThreshold)
	require.NoError(t, err, "Purge should succeed")
	t.Logf("🗑️ Purge deleted %d documents (threshold: %s)", deleted, formatTimestamp(purgeThreshold))

	// Refresh and verify
	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err)

	totalAfter, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Total trace docs after purge: %d", totalAfter)

	// After purge: old docs should be removed, recent docs should remain
	assert.Less(t, totalAfter, totalBefore, "purge should have reduced document count")
	assert.Greater(t, deleted, int64(0), "at least one document should have been deleted")
	t.Logf("✅ Purge verified: %d docs before → %d docs after (%d deleted)", totalBefore, totalAfter, deleted)
}

func TestIntegration_PurgeByApp(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up test indices first
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	cleanupTestIndices(t, client, indexPrefix)

	// Re-create index templates
	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err, "InitSchema should succeed")

	writer := NewTraceWriter(client, cfg, logger)

	// Write traces for app-A (old data)
	tracesAppA := buildTestTracesWithTimestamp("svc-app-a", "app-a", time.Now().Add(-3*time.Hour))
	err = writer.WriteTraces(ctx, tracesAppA)
	require.NoError(t, err)

	// Write traces for app-B (old data)
	tracesAppB := buildTestTracesWithTimestamp("svc-app-b", "app-b", time.Now().Add(-3*time.Hour))
	err = writer.WriteTraces(ctx, tracesAppB)
	require.NoError(t, err)

	// Write traces for app-A (recent data)
	tracesAppARecent := buildTestTracesWithTimestamp("svc-app-a", "app-a", time.Now())
	err = writer.WriteTraces(ctx, tracesAppARecent)
	require.NoError(t, err)

	// Flush and refresh
	err = writer.Flush(ctx)
	require.NoError(t, err)
	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err)

	// Count docs before purge
	totalBefore, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Total trace docs before PurgeByApp: %d", totalBefore)
	require.GreaterOrEqual(t, totalBefore, int64(3), "should have at least 3 trace docs")

	// Purge only app-A's old data
	provider, err := NewProvider(cfg, logger)
	require.NoError(t, err)
	provider.client = client

	purgeThreshold := time.Now().Add(-1 * time.Hour)
	deleted, err := provider.PurgeByApp(ctx, "app-a", cfg.Traces.IndexPrefix+"-*", "start_time", purgeThreshold)
	require.NoError(t, err, "PurgeByApp should succeed")
	t.Logf("🗑️ PurgeByApp(app-a) deleted %d documents", deleted)

	// Refresh and verify
	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err)

	totalAfter, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Total trace docs after PurgeByApp: %d", totalAfter)

	// app-B's data should be intact, app-A's old data removed, app-A's recent data intact
	assert.Greater(t, deleted, int64(0), "at least one document from app-a should have been deleted")
	assert.Less(t, totalAfter, totalBefore, "total count should decrease")

	// Verify app-B data is still intact by counting with a filter
	appBQuery := map[string]any{
		"term": map[string]any{"resource.app_id": "app-b"},
	}
	appBCount, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", appBQuery)
	require.NoError(t, err)
	assert.Greater(t, appBCount, int64(0), "app-B documents should remain untouched")
	t.Logf("✅ PurgeByApp verified: app-B docs remain=%d, app-A old deleted=%d", appBCount, deleted)
}

func TestIntegration_PurgeLogs(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-logs-*")

	// Re-create templates
	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	writer := NewLogWriter(client, cfg, logger)

	// Write old logs (5 hours ago)
	oldLogs := buildTestLogsWithTimestamp("purge-log-svc", "purge-log-app", time.Now().Add(-5*time.Hour))
	err = writer.WriteLogs(ctx, oldLogs)
	require.NoError(t, err)

	// Write recent logs
	recentLogs := buildTestLogsWithTimestamp("purge-log-svc", "purge-log-app", time.Now())
	err = writer.WriteLogs(ctx, recentLogs)
	require.NoError(t, err)

	// Flush and refresh
	err = writer.Flush(ctx)
	require.NoError(t, err)
	err = client.RefreshIndex(ctx, cfg.Logs.IndexPrefix+"-*")
	require.NoError(t, err)

	totalBefore, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Log docs before purge: %d", totalBefore)
	require.GreaterOrEqual(t, totalBefore, int64(2), "should have at least 2 log docs")

	// Purge logs older than 2 hours
	provider, err := NewProvider(cfg, logger)
	require.NoError(t, err)
	provider.client = client

	deleted, err := provider.Purge(ctx, cfg.Logs.IndexPrefix+"-*", "timestamp", time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	t.Logf("🗑️ Purge (logs) deleted %d documents", deleted)

	// Verify
	err = client.RefreshIndex(ctx, cfg.Logs.IndexPrefix+"-*")
	require.NoError(t, err)

	totalAfter, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Log docs after purge: %d", totalAfter)

	assert.Greater(t, deleted, int64(0), "old logs should be purged")
	assert.Less(t, totalAfter, totalBefore, "total count should decrease")
	t.Logf("✅ Log purge verified: %d before → %d after", totalBefore, totalAfter)
}

func TestIntegration_PurgeMetrics(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-metrics-*")

	// Re-create templates
	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, logger)

	// Write old metrics (4 hours ago)
	oldMetrics := buildTestMetricsWithTimestamp("purge-metric-svc", "purge-metric-app", time.Now().Add(-4*time.Hour))
	err = writer.WriteMetrics(ctx, oldMetrics)
	require.NoError(t, err)

	// Write recent metrics
	recentMetrics := buildTestMetricsWithTimestamp("purge-metric-svc", "purge-metric-app", time.Now())
	err = writer.WriteMetrics(ctx, recentMetrics)
	require.NoError(t, err)

	// Flush and refresh
	err = writer.Flush(ctx)
	require.NoError(t, err)
	err = client.RefreshIndex(ctx, cfg.Metrics.IndexPrefix+"-*")
	require.NoError(t, err)

	totalBefore, err := client.Count(ctx, cfg.Metrics.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Metric docs before purge: %d", totalBefore)
	require.GreaterOrEqual(t, totalBefore, int64(2), "should have at least 2 metric docs")

	// Purge metrics older than 2 hours
	provider, err := NewProvider(cfg, logger)
	require.NoError(t, err)
	provider.client = client

	deleted, err := provider.Purge(ctx, cfg.Metrics.IndexPrefix+"-*", "@timestamp", time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	t.Logf("🗑️ Purge (metrics) deleted %d documents", deleted)

	// Verify
	err = client.RefreshIndex(ctx, cfg.Metrics.IndexPrefix+"-*")
	require.NoError(t, err)

	totalAfter, err := client.Count(ctx, cfg.Metrics.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Metric docs after purge: %d", totalAfter)

	assert.Greater(t, deleted, int64(0), "old metrics should be purged")
	assert.Less(t, totalAfter, totalBefore, "total count should decrease")
	t.Logf("✅ Metric purge verified: %d before → %d after", totalBefore, totalAfter)
}

// ==================== Reader Tests (Write → Query Verification) ====================

func TestIntegration_TraceReader_WriteAndQuery(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up trace indices for a known state
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-traces-*")

	// Re-create templates
	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	// Write test traces
	writer := NewTraceWriter(client, cfg, logger)
	now := time.Now()

	td := buildTestTracesWithTimestamp("reader-test-svc", "reader-app", now)
	err = writer.WriteTraces(ctx, td)
	require.NoError(t, err)
	err = writer.Flush(ctx)
	require.NoError(t, err)

	// Refresh to make data immediately searchable
	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err)

	// Verify docs are indexed
	count, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Trace docs written: %d", count)
	require.GreaterOrEqual(t, count, int64(2), "should have at least 2 span docs (root + child)")

	// Create reader
	reader := NewTraceReader(client, cfg, logger)

	// === Test SearchTraces ===
	t.Run("SearchTraces", func(t *testing.T) {
		result, err := reader.SearchTraces(ctx, TraceQuery{
			ServiceName: "reader-test-svc",
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
			Limit: 10,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 SearchTraces: found %d traces (total: %d)", len(result.Traces), result.Total)
		assert.Greater(t, len(result.Traces), 0, "should find at least 1 trace")

		if len(result.Traces) > 0 {
			trace := result.Traces[0]
			t.Logf("   Trace ID: %s, Spans: %d, Duration: %dμs", trace.TraceID, len(trace.Spans), trace.Duration)
			assert.Greater(t, len(trace.Spans), 0, "trace should have spans")
		}
	})

	// === Test GetTrace ===
	t.Run("GetTrace", func(t *testing.T) {
		// The test data uses trace ID: deadbeef01020304050607080909a0b0c
		traceID := "deadbeef0102030405060708090a0b0c"
		trace, err := reader.GetTrace(ctx, traceID)
		require.NoError(t, err)
		require.NotNil(t, trace)
		t.Logf("🔍 GetTrace(%s): %d spans, duration %dμs", traceID, len(trace.Spans), trace.Duration)
		assert.Equal(t, 2, len(trace.Spans), "should have root + child span")

		// Verify span details
		for _, span := range trace.Spans {
			t.Logf("   Span: %s [%s] %s → %s (%dμs)",
				span.OperationName, span.SpanKind, span.ServiceName,
				span.StatusCode, span.DurationUS)
			assert.Equal(t, "reader-test-svc", span.ServiceName)
			assert.NotEmpty(t, span.OperationName)
		}
	})

	// === Test GetServices ===
	t.Run("GetServices", func(t *testing.T) {
		services, err := reader.GetServices(ctx, TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 GetServices: found %d services", len(services))
		assert.Greater(t, len(services), 0, "should find at least 1 service")

		found := false
		for _, s := range services {
			t.Logf("   Service: %s", s.Name)
			if s.Name == "reader-test-svc" {
				found = true
			}
		}
		assert.True(t, found, "should find 'reader-test-svc'")
	})

	// === Test GetOperations ===
	t.Run("GetOperations", func(t *testing.T) {
		ops, err := reader.GetOperations(ctx, "reader-test-svc", TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 GetOperations(reader-test-svc): found %d operations", len(ops))
		assert.Greater(t, len(ops), 0, "should find at least 1 operation")
		for _, op := range ops {
			t.Logf("   Operation: %s [%s]", op.Name, op.SpanKind)
		}
	})

	// === Test GetDependencies ===
	t.Run("GetDependencies", func(t *testing.T) {
		deps, err := reader.GetDependencies(ctx, TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 GetDependencies: found %d dependencies", len(deps))
		for _, d := range deps {
			t.Logf("   %s → %s (calls: %d)", d.Parent, d.Child, d.CallCount)
		}
		// Note: dependencies use service co-occurrence within traces.
		// Since all spans belong to the same service, there may be 0 dependencies.
	})

	t.Log("✅ TraceReader write→query verification complete")
}

func TestIntegration_LogReader_WriteAndQuery(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-logs-*")

	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	// Write test logs
	writer := NewLogWriter(client, cfg, logger)
	now := time.Now()

	ld := buildTestLogsWithTimestamp("log-reader-svc", "log-reader-app", now)
	err = writer.WriteLogs(ctx, ld)
	require.NoError(t, err)
	err = writer.Flush(ctx)
	require.NoError(t, err)

	// Refresh
	err = client.RefreshIndex(ctx, cfg.Logs.IndexPrefix+"-*")
	require.NoError(t, err)

	count, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Log docs written: %d", count)
	require.GreaterOrEqual(t, count, int64(3), "should have 3 log records (INFO + ERROR + WARN)")

	reader := NewLogReader(client, cfg, logger)

	// === Test SearchLogs (all) ===
	t.Run("SearchLogs_All", func(t *testing.T) {
		result, err := reader.SearchLogs(ctx, LogQuery{
			ServiceName: "log-reader-svc",
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
			Limit: 50,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 SearchLogs(all): found %d logs (total: %d)", len(result.Logs), result.Total)
		assert.Equal(t, 3, len(result.Logs), "should find 3 log records")

		for _, log := range result.Logs {
			t.Logf("   [%s] %s | trace=%s", log.Severity, truncate(log.Body, 50), log.TraceID)
		}
	})

	// === Test SearchLogs by severity ===
	t.Run("SearchLogs_BySeverity", func(t *testing.T) {
		result, err := reader.SearchLogs(ctx, LogQuery{
			ServiceName: "log-reader-svc",
			Severity:    []string{"ERROR"},
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 SearchLogs(ERROR): found %d logs", len(result.Logs))
		assert.Equal(t, 1, len(result.Logs), "should find 1 ERROR log")
		if len(result.Logs) > 0 {
			assert.Equal(t, "ERROR", result.Logs[0].Severity)
			assert.Contains(t, result.Logs[0].Body, "timeout")
		}
	})

	// === Test SearchLogs by trace ID ===
	t.Run("SearchLogs_ByTraceID", func(t *testing.T) {
		traceID := "deadbeef0102030405060708090a0b0c"
		result, err := reader.SearchLogs(ctx, LogQuery{
			TraceID: traceID,
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 SearchLogs(traceID=%s): found %d logs", traceID, len(result.Logs))
		assert.Equal(t, 1, len(result.Logs), "should find 1 log with this trace ID")
	})

	// === Test SearchLogs by full-text query ===
	t.Run("SearchLogs_FullText", func(t *testing.T) {
		result, err := reader.SearchLogs(ctx, LogQuery{
			Query: "integration test",
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 SearchLogs(query='integration test'): found %d logs", len(result.Logs))
		// Both INFO and ERROR log bodies contain "integration test"
		assert.GreaterOrEqual(t, len(result.Logs), 1, "should find logs matching 'integration test'")
	})

	// === Test GetLogStats ===
	t.Run("GetLogStats", func(t *testing.T) {
		stats, err := reader.GetLogStats(ctx, LogStatsQuery{
			ServiceName: "log-reader-svc",
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, stats)
		t.Logf("🔍 GetLogStats: total=%d, severities=%v, services=%v",
			stats.TotalCount, stats.SeverityCounts, stats.ServiceCounts)
		assert.Equal(t, int64(3), stats.TotalCount, "should count 3 log records")
		assert.Contains(t, stats.SeverityCounts, "ERROR")
		assert.Contains(t, stats.SeverityCounts, "INFO")
		assert.Contains(t, stats.SeverityCounts, "WARN")
	})

	// === Test ListLogFields ===
	t.Run("ListLogFields", func(t *testing.T) {
		fields, err := reader.ListLogFields(ctx, TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 ListLogFields: %d fields", len(fields))
		assert.Greater(t, len(fields), 0, "should return log fields")
		for _, f := range fields {
			t.Logf("   Field: %s (%s) count=%d", f.Name, f.Type, f.Count)
		}
	})

	t.Log("✅ LogReader write→query verification complete")
}

func TestIntegration_MetricReader_WriteAndQuery(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-metrics-*")

	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	// Write test metrics
	writer := NewMetricWriter(client, cfg, logger)
	now := time.Now()

	md := buildTestMetricsWithTimestamp("metric-reader-svc", "metric-reader-app", now)
	err = writer.WriteMetrics(ctx, md)
	require.NoError(t, err)
	err = writer.Flush(ctx)
	require.NoError(t, err)

	// Refresh
	err = client.RefreshIndex(ctx, cfg.Metrics.IndexPrefix+"-*")
	require.NoError(t, err)

	count, err := client.Count(ctx, cfg.Metrics.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Metric docs written: %d", count)
	require.GreaterOrEqual(t, count, int64(3), "should have at least 3 metric docs (gauge+counter+histogram)")

	reader := NewMetricReader(client, cfg, logger)

	// === Test ListMetricNames ===
	t.Run("ListMetricNames", func(t *testing.T) {
		names, err := reader.ListMetricNames(ctx, TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 ListMetricNames: %v", names)
		assert.GreaterOrEqual(t, len(names), 3, "should find gauge + counter + histogram")
		assert.Contains(t, names, "system.cpu.usage")
		assert.Contains(t, names, "http.server.request.total")
		assert.Contains(t, names, "http.server.duration")
	})

	// === Test Query (instant) ===
	t.Run("Query_Instant", func(t *testing.T) {
		result, err := reader.Query(ctx, MetricQuery{
			MetricName:  "system.cpu.usage",
			ServiceName: "metric-reader-svc",
			Time:        now.Add(1 * time.Minute),
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 Query(system.cpu.usage): %d data points", len(result.Data))
		assert.Greater(t, len(result.Data), 0, "should find cpu usage metric")
		if len(result.Data) > 0 {
			t.Logf("   Value: %.4f at %s", result.Data[0].Value, result.Data[0].Time)
			assert.InDelta(t, 0.73, result.Data[0].Value, 0.01, "cpu usage should be ~0.73")
		}
	})

	// === Test QueryRange ===
	t.Run("QueryRange", func(t *testing.T) {
		result, err := reader.QueryRange(ctx, MetricRangeQuery{
			MetricName:  "system.cpu.usage",
			ServiceName: "metric-reader-svc",
			TimeRange: TimeRange{
				Start: now.Add(-10 * time.Minute),
				End:   now.Add(10 * time.Minute),
			},
			Step: 1 * time.Minute,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		t.Logf("🔍 QueryRange(system.cpu.usage): %d series", len(result.Data))
		if len(result.Data) > 0 {
			t.Logf("   Series[0]: %d data points", len(result.Data[0].Values))
			for _, v := range result.Data[0].Values {
				t.Logf("     %.4f @ %s", v.Value, v.Time)
			}
		}
	})

	// === Test ListLabelNames ===
	t.Run("ListLabelNames", func(t *testing.T) {
		names, err := reader.ListLabelNames(ctx, TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 ListLabelNames: %v", names)
		// The test data has labels like "cpu", "method"
		assert.Greater(t, len(names), 0, "should find label names")
	})

	// === Test ListLabelValues ===
	t.Run("ListLabelValues", func(t *testing.T) {
		values, err := reader.ListLabelValues(ctx, "cpu", TimeRange{
			Start: now.Add(-10 * time.Minute),
			End:   now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		t.Logf("🔍 ListLabelValues(cpu): %v", values)
		assert.Contains(t, values, "cpu0", "should find 'cpu0' label value")
	})

	t.Log("✅ MetricReader write→query verification complete")
}

// truncate helper for display
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ==================== Admin Tests ====================

func TestIntegration_Admin_SetRetention(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	admin := NewAdmin(client, cfg, logger)

	// InitSchema first to create ILM policies
	err := admin.InitSchema(ctx)
	require.NoError(t, err, "InitSchema should succeed")

	// SetRetention: update trace retention to 14 days
	err = admin.SetRetention(ctx, cfg.Traces.IndexPrefix, 14*24*time.Hour)
	require.NoError(t, err, "SetRetention should succeed for traces")
	t.Log("✅ SetRetention(traces, 14d) succeeded")

	// SetRetention: update metric retention to 60 days
	err = admin.SetRetention(ctx, cfg.Metrics.IndexPrefix, 60*24*time.Hour)
	require.NoError(t, err, "SetRetention should succeed for metrics")
	t.Log("✅ SetRetention(metrics, 60d) succeeded")

	// SetRetention: update log retention to 7 days
	err = admin.SetRetention(ctx, cfg.Logs.IndexPrefix, 7*24*time.Hour)
	require.NoError(t, err, "SetRetention should succeed for logs")
	t.Log("✅ SetRetention(logs, 7d) succeeded")

	// Invalid retention should fail
	err = admin.SetRetention(ctx, cfg.Traces.IndexPrefix, -1*time.Hour)
	assert.Error(t, err, "SetRetention with negative duration should fail")
	t.Log("✅ SetRetention(negative duration) correctly rejected")
}

func TestIntegration_Admin_Purge(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up first
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-traces-*")

	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	// Write some test trace data
	writer := NewTraceWriter(client, cfg, logger)
	td := buildTestTraces("purge-test-svc", "purge-test-app")
	err = writer.WriteTraces(ctx, td)
	require.NoError(t, err)
	err = writer.Flush(ctx)
	require.NoError(t, err)

	// Wait for ES to index the data
	err = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	require.NoError(t, err)

	// Count documents before purge
	countBefore, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Documents before purge: %d", countBefore)
	require.Greater(t, countBefore, int64(0), "should have written some documents")

	// Purge with a future timestamp (should delete all docs)
	futureTime := time.Now().Add(1 * time.Hour)
	deleted, err := admin.Purge(ctx, cfg.Traces.IndexPrefix, "start_time", futureTime)
	require.NoError(t, err)
	t.Logf("🗑️ Purge deleted %d documents", deleted)
	assert.Equal(t, countBefore, deleted, "Purge should delete all documents when before is in the future")

	// Refresh and verify count is 0
	_ = client.RefreshIndex(ctx, cfg.Traces.IndexPrefix+"-*")
	countAfter, err := client.Count(ctx, cfg.Traces.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), countAfter, "all documents should be purged")
	t.Log("✅ Purge verified: all documents deleted")
}

func TestIntegration_Admin_PurgeByApp(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Clean up first
	indexPrefix := envOrDefault("ES_INDEX_PREFIX", "test-otel")
	_ = client.DeleteIndicesByPattern(ctx, indexPrefix+"-logs-*")

	admin := NewAdmin(client, cfg, logger)
	err := admin.InitSchema(ctx)
	require.NoError(t, err)

	// Write test log data with two different app_ids
	writer := NewLogWriter(client, cfg, logger)
	ld1 := buildTestLogs("purge-app-svc", "app-to-purge")
	ld2 := buildTestLogs("purge-app-svc", "app-to-keep")
	err = writer.WriteLogs(ctx, ld1)
	require.NoError(t, err)
	err = writer.WriteLogs(ctx, ld2)
	require.NoError(t, err)
	err = writer.Flush(ctx)
	require.NoError(t, err)

	// Wait for ES to index
	err = client.RefreshIndex(ctx, cfg.Logs.IndexPrefix+"-*")
	require.NoError(t, err)

	// Count total documents
	countBefore, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", nil)
	require.NoError(t, err)
	t.Logf("📊 Total log documents before purge: %d", countBefore)
	require.Greater(t, countBefore, int64(0))

	// Count documents for "app-to-purge"
	appQuery := map[string]any{"term": map[string]any{"app_id": "app-to-purge"}}
	countApp, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", appQuery)
	require.NoError(t, err)
	t.Logf("📊 Documents for app-to-purge: %d", countApp)

	// PurgeByApp: delete only "app-to-purge" data
	futureTime := time.Now().Add(1 * time.Hour)
	deleted, err := admin.PurgeByApp(ctx, cfg.Logs.IndexPrefix, "timestamp", "app-to-purge", futureTime)
	require.NoError(t, err)
	t.Logf("🗑️ PurgeByApp deleted %d documents", deleted)
	assert.Equal(t, countApp, deleted, "should delete only app-to-purge documents")

	// Verify "app-to-keep" documents still exist
	_ = client.RefreshIndex(ctx, cfg.Logs.IndexPrefix+"-*")
	keepQuery := map[string]any{"term": map[string]any{"app_id": "app-to-keep"}}
	countKeep, err := client.Count(ctx, cfg.Logs.IndexPrefix+"-*", keepQuery)
	require.NoError(t, err)
	assert.Greater(t, countKeep, int64(0), "app-to-keep documents should still exist")
	t.Logf("✅ PurgeByApp verified: app-to-keep still has %d documents", countKeep)
}

func TestIntegration_Admin_GetStatus(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	admin := NewAdmin(client, cfg, logger)
	status, err := admin.GetStatus(ctx)
	require.NoError(t, err)
	require.NotNil(t, status)

	clusterName, _ := status["cluster_name"].(string)
	clusterStatus, _ := status["status"].(string)
	t.Logf("✅ GetStatus: cluster=%s, status=%s", clusterName, clusterStatus)
	assert.Contains(t, []string{"green", "yellow", "red"}, clusterStatus)
}

func TestIntegration_Admin_GetDiskUsage(t *testing.T) {
	client, cfg := setupTestClient(t)
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	admin := NewAdmin(client, cfg, logger)

	// Make sure we have some data
	_ = admin.InitSchema(ctx)

	stats, err := admin.GetIndicesStats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats)
	t.Logf("✅ GetIndicesStats returned data")
}
