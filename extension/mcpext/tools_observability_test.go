// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

type fakeMetricReader struct {
	observabilitystorageext.MetricReader
	lastQuery      *observabilitystorageext.MetricQuery
	lastRangeQuery *observabilitystorageext.MetricRangeQuery
	names          []string
	labelValues    []string
}

func (f *fakeMetricReader) Query(_ context.Context, q observabilitystorageext.MetricQuery) (*observabilitystorageext.MetricResult, error) {
	f.lastQuery = &q
	return &observabilitystorageext.MetricResult{Data: []observabilitystorageext.MetricDataPoint{
		{Metric: q.MetricName, Value: 1.5, TimeUnixMilli: "1700000000000"},
	}}, nil
}

func (f *fakeMetricReader) QueryRange(_ context.Context, q observabilitystorageext.MetricRangeQuery) (*observabilitystorageext.MetricRangeResult, error) {
	f.lastRangeQuery = &q
	return &observabilitystorageext.MetricRangeResult{Data: []observabilitystorageext.MetricSeries{{
		Metric: q.MetricName,
		Values: []observabilitystorageext.MetricTimeValue{{TimeUnixMilli: "1700000000000", Value: 2.5}},
	}}}, nil
}

func (f *fakeMetricReader) ListMetricNames(context.Context, observabilitystorageext.TimeRange) ([]string, error) {
	return f.names, nil
}

func (f *fakeMetricReader) ListLabelValuesForMetric(_ context.Context, _, _ string, _ observabilitystorageext.TimeRange) ([]string, error) {
	return f.labelValues, nil
}

type fakeTraceReader struct {
	observabilitystorageext.TraceReader
	lastQuery *observabilitystorageext.TraceQuery
}

func (f *fakeTraceReader) SearchTraceSummaries(_ context.Context, q observabilitystorageext.TraceQuery, _ int) (*observabilitystorageext.TraceSummaryResult, error) {
	f.lastQuery = &q
	return &observabilitystorageext.TraceSummaryResult{
		Summaries: []observabilitystorageext.TraceSummary{{TraceID: "trace-1", DurationMs: 12, SpanCount: 3}},
		Total:     1,
	}, nil
}

func (f *fakeTraceReader) GetTrace(_ context.Context, traceID string) (*observabilitystorageext.Trace, error) {
	return &observabilitystorageext.Trace{
		TraceID:   traceID,
		SpanCount: 1,
		Spans: []observabilitystorageext.Span{{
			SpanID: "span-1", Name: "GET /health", ServiceName: "svc-a",
			Status: observabilitystorageext.SpanStatus{Code: observabilitystorageext.StatusCodeOk},
		}},
	}, nil
}

func (f *fakeTraceReader) GetServices(context.Context, observabilitystorageext.TimeRange) ([]observabilitystorageext.Service, error) {
	return []observabilitystorageext.Service{{Name: "svc-a", SpanCount: 3}}, nil
}

type fakeLogReader struct {
	observabilitystorageext.LogReader
	lastQuery *observabilitystorageext.LogQuery
	logs      []observabilitystorageext.LogRecord
}

func (f *fakeLogReader) SearchLogs(_ context.Context, q observabilitystorageext.LogQuery) (*observabilitystorageext.LogSearchResult, error) {
	f.lastQuery = &q
	return &observabilitystorageext.LogSearchResult{Logs: f.logs, Total: int64(len(f.logs))}, nil
}

func (f *fakeLogReader) GetLogContext(_ context.Context, logID string, _ int) (*observabilitystorageext.LogContext, error) {
	return &observabilitystorageext.LogContext{
		Target: observabilitystorageext.LogRecord{ID: logID, Body: "target line"},
		Before: []observabilitystorageext.LogRecord{{ID: "before-1", Body: "before line"}},
		After:  []observabilitystorageext.LogRecord{{ID: "after-1", Body: "after line"}},
	}, nil
}

func observabilityTestWrapper() (
	*mcpServerWrapper,
	*fakeMetricReader,
	*fakeTraceReader,
	*fakeLogReader,
) {
	cfg := createDefaultConfig()
	cfg.Auth.APIKeys = []string{"test-key"}
	cfg.Observability.Enabled = true
	cfg.Observability.Extension = "observability_storage"
	cfg.Observability.Signals = ObservabilitySignals{Metric: true, Trace: true, Log: true}

	metric := &fakeMetricReader{names: []string{"m1", "m2"}, labelValues: []string{"GET", "POST"}}
	trace := &fakeTraceReader{}
	log := &fakeLogReader{logs: []observabilitystorageext.LogRecord{
		{ID: "log-1", Body: "hello", SeverityText: "INFO", ServiceName: "svc-a"},
	}}

	ext := &Extension{
		config:       cfg,
		logger:       zap.NewNop(),
		controlPlane: &stubControlPlane{},
		metricReader: metric,
		traceReader:  trace,
		logReader:    log,
	}
	w := &mcpServerWrapper{
		ext:            ext,
		mcpServer:      server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(true)),
		logger:         zap.NewNop(),
		instrPolicy:    newInstrumentationPolicy(cfg.Instrumentation),
		sessionManager: NewSessionManager(zap.NewNop(), SessionManagerConfig{}),
	}
	return w, metric, trace, log
}

func queryIdentity(tenant string, scopes ...string) context.Context {
	keyType := tenantauth.KeyTypeTenant
	if tenant == "" {
		keyType = tenantauth.KeyTypeSuper
	}
	return tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType:  keyType,
		TenantID: tenant,
		KeyID:    "key-1",
		Scopes:   scopes,
	})
}

func decodeEnvelope(t *testing.T, result *mcp.CallToolResult) observabilityEnvelope {
	t.Helper()
	text := instrumentationText(t, result)
	var envelope observabilityEnvelope
	require.NoError(t, json.Unmarshal([]byte(text), &envelope))
	return envelope
}

func TestObservabilityToolRegistration(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	w.registerObservabilityTools()
	assert.Len(t, w.mcpServer.ListTools(), 9)

	w2, _, _, _ := observabilityTestWrapper()
	w2.ext.config.Observability.Signals.Trace = false
	w2.registerObservabilityTools()
	assert.Len(t, w2.mcpServer.ListTools(), 6, "disabling a signal removes its tool group")

	w3, _, _, _ := observabilityTestWrapper()
	w3.ext.metricReader = nil
	w3.registerObservabilityTools()
	assert.Len(t, w3.mcpServer.ListTools(), 5, "a missing reader must not register its tools")

	w4, _, _, _ := observabilityTestWrapper()
	w4.ext.config.Observability.Enabled = false
	w4.registerObservabilityTools()
	assert.Empty(t, w4.mcpServer.ListTools(), "disabled config registers nothing even if readers exist")
}

func TestObservabilityScopeGate(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	request := namedToolCall("metric_query", map[string]any{"metric": "m1"})

	err := w.authorizeToolCall(queryIdentity("tn-a"), request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), metricReadScope)

	require.NoError(t, w.authorizeToolCall(queryIdentity("tn-a", metricReadScope), request))
	require.NoError(t, w.authorizeToolCall(queryIdentity(""), request), "global keys are unrestricted")

	err = w.authorizeToolCall(queryIdentity("tn-a", traceReadScope), request)
	require.Error(t, err, "a scope from another signal must not grant access")

	require.Error(t, w.authorizeToolCall(context.Background(), request), "missing identity fails closed")

	err = w.authorizeToolCall(queryIdentity("tn-a", metricReadScope),
		namedToolCall("metric_query", map[string]any{"metric": "m1", "tenant_id": "tn-b"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant_id")

	w.ext.config.Observability.Signals.Metric = false
	require.Error(t, w.authorizeToolCall(queryIdentity("tn-a", metricReadScope), request),
		"disabled signal fails closed at call time")

	w.ext.config.Observability.Signals.Metric = true
	w.ext.metricReader = nil
	require.Error(t, w.authorizeToolCall(queryIdentity("tn-a", metricReadScope), request),
		"missing reader fails closed at call time")
}

func TestObservabilityQueryForcesTenantID(t *testing.T) {
	w, metric, trace, log := observabilityTestWrapper()
	ctx := queryIdentity("tn-a", metricReadScope, traceReadScope, logReadScope)

	result, err := w.handleMetricQuery(ctx, namedToolCall("metric_query", map[string]any{"metric": "m1"}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotNil(t, metric.lastQuery)
	assert.Equal(t, "tn-a", metric.lastQuery.TenantID)
	assert.Equal(t, "tn-a", decodeEnvelope(t, result).TenantID)

	_, err = w.handleTraceSearch(ctx, namedToolCall("trace_search", map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, trace.lastQuery)
	assert.Equal(t, "tn-a", trace.lastQuery.TenantID)

	_, err = w.handleLogSearch(ctx, namedToolCall("log_search", map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, log.lastQuery)
	assert.Equal(t, "tn-a", log.lastQuery.TenantID)

	global, err := w.handleMetricQuery(queryIdentity(""), namedToolCall("metric_query", map[string]any{"metric": "m1"}))
	require.NoError(t, err)
	require.False(t, global.IsError)
	assert.Empty(t, metric.lastQuery.TenantID, "global identity keeps global read scope")
}

func TestObservabilityTimeRangeAndLimits(t *testing.T) {
	w, _, _, log := observabilityTestWrapper()
	ctx := queryIdentity("tn-a", metricReadScope, traceReadScope, logReadScope)

	tooWide, err := w.handleMetricQueryRange(ctx, namedToolCall("metric_query_range", map[string]any{
		"metric": "m1",
		"start":  "2024-01-01T00:00:00Z",
		"end":    "2024-03-01T00:00:00Z",
	}))
	require.NoError(t, err)
	assert.True(t, tooWide.IsError)
	assert.Contains(t, instrumentationText(t, tooWide), "时间窗")

	badStart, err := w.handleLogSearch(ctx, namedToolCall("log_search", map[string]any{"start": "not-a-time"}))
	require.NoError(t, err)
	assert.True(t, badStart.IsError)

	logs := make([]observabilitystorageext.LogRecord, 0, 150)
	for i := 0; i < 150; i++ {
		logs = append(logs, observabilitystorageext.LogRecord{ID: "log", Body: "line"})
	}
	log.logs = logs
	searchResult, err := w.handleLogSearch(ctx, namedToolCall("log_search", map[string]any{}))
	require.NoError(t, err)
	envelope := decodeEnvelope(t, searchResult)
	assert.Equal(t, 100, envelope.Count)
	assert.True(t, envelope.Truncated)
}

func TestObservabilityAllToolsReturnEnvelopes(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	ctx := queryIdentity("tn-a", metricReadScope, traceReadScope, logReadScope)

	calls := []struct {
		name string
		args map[string]any
	}{
		{"metric_query", map[string]any{"metric": "m1"}},
		{"metric_query_range", map[string]any{"metric": "m1", "step": "1m"}},
		{"metric_list_names", map[string]any{}},
		{"metric_label_values", map[string]any{"label": "http.method"}},
		{"trace_search", map[string]any{}},
		{"trace_get", map[string]any{"trace_id": "trace-1"}},
		{"trace_services", map[string]any{}},
		{"log_search", map[string]any{}},
		{"log_context", map[string]any{"log_id": "log-1"}},
	}

	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			var result *mcp.CallToolResult
			var err error
			switch call.name {
			case "metric_query":
				result, err = w.handleMetricQuery(ctx, namedToolCall(call.name, call.args))
			case "metric_query_range":
				result, err = w.handleMetricQueryRange(ctx, namedToolCall(call.name, call.args))
			case "metric_list_names":
				result, err = w.handleMetricListNames(ctx, namedToolCall(call.name, call.args))
			case "metric_label_values":
				result, err = w.handleMetricLabelValues(ctx, namedToolCall(call.name, call.args))
			case "trace_search":
				result, err = w.handleTraceSearch(ctx, namedToolCall(call.name, call.args))
			case "trace_get":
				result, err = w.handleTraceGet(ctx, namedToolCall(call.name, call.args))
			case "trace_services":
				result, err = w.handleTraceServices(ctx, namedToolCall(call.name, call.args))
			case "log_search":
				result, err = w.handleLogSearch(ctx, namedToolCall(call.name, call.args))
			case "log_context":
				result, err = w.handleLogContext(ctx, namedToolCall(call.name, call.args))
			}
			require.NoError(t, err)
			require.False(t, result.IsError, instrumentationText(t, result))
			envelope := decodeEnvelope(t, result)
			assert.Equal(t, call.name, envelope.Tool)
			assert.Equal(t, "tn-a", envelope.TenantID)
			assert.NotNil(t, envelope.Items)
		})
	}
}

func TestMetricSeriesViewsSkipsEmptySeries(t *testing.T) {
	series := []observabilitystorageext.MetricSeries{
		{Metric: "empty"},
		{Metric: "real", Values: []observabilitystorageext.MetricTimeValue{{TimeUnixMilli: "1", Value: 2}}},
	}
	views, truncated := metricSeriesViews(series, 10, 10)
	assert.False(t, truncated)
	require.Len(t, views, 1)
	assert.Equal(t, "real", views[0].Metric)
}

func TestObservabilityConfigDefaults(t *testing.T) {
	cfg := createDefaultConfig()
	assert.False(t, cfg.Observability.Enabled)
	assert.False(t, cfg.Observability.Signals.Metric)
	assert.False(t, cfg.Observability.Signals.Trace)
	assert.False(t, cfg.Observability.Signals.Log)
	assert.Equal(t, 24*time.Hour, cfg.Observability.maxTimeRange())
	assert.Equal(t, 20*time.Second, cfg.Observability.queryTimeout())
	cfg.Auth.APIKeys = []string{"test-key"}
	require.NoError(t, cfg.Validate())

	cfg.Observability.Enabled = true
	require.Error(t, cfg.Validate(), "enabling without a signal must be rejected")

	cfg.Observability.Signals.Metric = true
	require.NoError(t, cfg.Validate())

	cfg.Observability.QueryTimeout = cfg.ToolTimeout + 1
	require.Error(t, cfg.Validate(), "query timeout must not exceed tool timeout")
}

func TestESScopedReadersUseTenantContext(t *testing.T) {
	// MCP relies on ES readers reading tenantctx for methods without a
	// TenantID argument. Guard that the shared context key is actually set.
	ctx := tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType:  tenantauth.KeyTypeTenant,
		TenantID: "tn-a",
	})
	assert.Equal(t, "tn-a", tenantctx.TenantIDFromContext(ctx))
}
