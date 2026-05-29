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
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap/zaptest"
)

func TestMetricWriter_GaugeToDoc(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	// Create gauge metric
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "cpu-monitor")
	rm.Resource().Attributes().PutStr("app_id", "app-001")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("system.cpu.usage")
	metric.SetEmptyGauge()

	dp := metric.Gauge().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetDoubleValue(0.75)
	dp.Attributes().PutStr("cpu", "cpu0")
	dp.Attributes().PutStr("state", "user")

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.gaugeToDoc(metric, resource, "cpu-monitor")

	require.Len(t, docs, 1)
	doc := docs[0]

	assert.Equal(t, "system.cpu.usage", doc["metric_name"])
	assert.Equal(t, "gauge", doc["metric_type"])
	assert.Equal(t, "cpu-monitor", doc["service_name"])
	assert.Equal(t, 0.75, doc["value"])
	assert.Equal(t, "app-001", doc["app_id"])

	labels := doc["labels"].(map[string]any)
	assert.Equal(t, "cpu0", labels["cpu"])
	assert.Equal(t, "user", labels["state"])
}

func TestMetricWriter_GaugeToDoc_IntValue(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "gauge-int")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("process.open_file_descriptors")
	metric.SetEmptyGauge()

	dp := metric.Gauge().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetIntValue(256)

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.gaugeToDoc(metric, resource, "gauge-int")

	require.Len(t, docs, 1)
	// Int values are stored as float64
	assert.Equal(t, float64(256), docs[0]["value"])
}

func TestMetricWriter_SumToDoc(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "request-counter")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("http.server.request.total")
	metric.SetEmptySum()
	metric.Sum().SetIsMonotonic(true)

	dp := metric.Sum().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetIntValue(12345)
	dp.Attributes().PutStr("method", "GET")
	dp.Attributes().PutInt("status", 200)

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.sumToDoc(metric, resource, "request-counter")

	require.Len(t, docs, 1)
	doc := docs[0]

	assert.Equal(t, "http.server.request.total", doc["metric_name"])
	assert.Equal(t, "counter", doc["metric_type"])
	assert.Equal(t, "request-counter", doc["service_name"])
	assert.Equal(t, float64(12345), doc["value"])

	labels := doc["labels"].(map[string]any)
	assert.Equal(t, "GET", labels["method"])
	assert.Equal(t, int64(200), labels["status"])
}

func TestMetricWriter_HistogramToDoc(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "latency-svc")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("http.server.duration")
	metric.SetEmptyHistogram()

	dp := metric.Histogram().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetSum(1500.5)
	dp.SetCount(100)
	dp.ExplicitBounds().Append(10.0, 25.0, 50.0, 100.0, 250.0)
	dp.BucketCounts().Append(5, 15, 30, 25, 15, 10) // one more than bounds

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.histogramToDoc(metric, resource, "latency-svc")

	require.Len(t, docs, 1)
	doc := docs[0]

	assert.Equal(t, "http.server.duration", doc["metric_name"])
	assert.Equal(t, "histogram", doc["metric_type"])
	assert.Equal(t, "latency-svc", doc["service_name"])
	assert.Equal(t, 1500.5, doc["value"])

	histogram := doc["histogram"].(map[string]any)
	counts := histogram["counts"].([]uint64)
	assert.Equal(t, []uint64{5, 15, 30, 25, 15, 10}, counts)
	values := histogram["values"].([]float64)
	assert.Equal(t, []float64{10.0, 25.0, 50.0, 100.0, 250.0}, values)
}

func TestMetricWriter_SummaryToDoc(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "summary-svc")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("rpc.server.duration")
	metric.SetEmptySummary()

	dp := metric.Summary().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetSum(3000.0)
	dp.SetCount(50)

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.summaryToDoc(metric, resource, "summary-svc")

	require.Len(t, docs, 1)
	doc := docs[0]

	assert.Equal(t, "rpc.server.duration", doc["metric_name"])
	assert.Equal(t, "summary", doc["metric_type"])
	assert.Equal(t, 3000.0, doc["value"])
}

func TestMetricWriter_GetIndexName(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	ts := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	indexName := writer.getIndexName(ts)
	assert.Equal(t, "otel-metrics-2026.12.31", indexName)
}

func TestMetricWriter_WriteMetrics_EndToEnd(t *testing.T) {
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

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	// Create metric data
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "test-svc")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("test.gauge")
	metric.SetEmptyGauge()

	dp := metric.Gauge().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetDoubleValue(42.0)

	err = writer.WriteMetrics(context.Background(), md)
	require.NoError(t, err)

	// Verify bulk body
	require.NotEmpty(t, receivedBody)
	lines := strings.Split(strings.TrimSpace(string(receivedBody)), "\n")
	require.Len(t, lines, 2)

	var action map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &action))
	indexAction := action["index"].(map[string]any)
	assert.Equal(t, "otel-metrics-2026.05.29", indexAction["_index"])

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &doc))
	assert.Equal(t, "test.gauge", doc["metric_name"])
	assert.Equal(t, 42.0, doc["value"])
}

func TestMetricWriter_MultipleDataPoints(t *testing.T) {
	var bulkCallCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_bulk" {
			bulkCallCount++
		}
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 5

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	// Create metric with multiple data points
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "multi-dp")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("cpu.usage")
	metric.SetEmptyGauge()

	for i := 0; i < 7; i++ {
		dp := metric.Gauge().DataPoints().AppendEmpty()
		dp.SetTimestamp(pcommon.NewTimestampFromTime(
			time.Date(2026, 5, 29, 10, 0, i, 0, time.UTC),
		))
		dp.SetDoubleValue(float64(i) * 0.1)
	}

	err = writer.WriteMetrics(context.Background(), md)
	require.NoError(t, err)

	// 7 data points with batch_size 5: 1 bulk call triggered
	assert.Equal(t, 1, bulkCallCount)

	// Flush remaining 2
	require.NoError(t, writer.Flush(context.Background()))
	assert.Equal(t, 2, bulkCallCount)
}

func TestMetricWriter_AppIDExtracted(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	// Resource with app_id
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "svc")
	rm.Resource().Attributes().PutStr("app_id", "my-app-123")

	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("test.metric")
	metric.SetEmptyGauge()
	dp := metric.Gauge().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	dp.SetDoubleValue(1.0)

	resource := extractResourceAttributes(rm.Resource())
	docs := writer.gaugeToDoc(metric, resource, "svc")

	require.Len(t, docs, 1)
	assert.Equal(t, "my-app-123", docs[0]["app_id"])
}
