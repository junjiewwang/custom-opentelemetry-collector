// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap/zaptest"
)

func TestConvertOTLPMetric_Gauge(t *testing.T) {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "test-svc")

	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("cpu.usage")
	dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)))
	dp.SetDoubleValue(42.5)
	dp.Attributes().PutStr("host", "srv01")

	points := storedmodel.ConvertOTLPMetric(m, rm.Resource())
	assert.Len(t, points, 1)
	assert.Equal(t, "cpu.usage", points[0].Name)
	assert.Equal(t, "gauge", points[0].Type)
	assert.Equal(t, 42.5, points[0].Value)
	assert.Equal(t, "test-svc", points[0].ServiceName)
	assert.Equal(t, "srv01", points[0].Labels["host"])
}

func TestConvertOTLPMetric_Sum(t *testing.T) {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("request.count")
	dp := m.SetEmptySum().DataPoints().AppendEmpty()
	dp.SetIntValue(100)

	points := storedmodel.ConvertOTLPMetric(m, rm.Resource())
	assert.Len(t, points, 1)
	assert.Equal(t, "counter", points[0].Type)
	assert.Equal(t, float64(100), points[0].Value)
}

func TestMetricWriter_GetIndexName(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()
	cfg := newTestConfig([]string{server.URL})
	client, _ := NewClient(cfg, zaptest.NewLogger(t))
	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	name := writer.getIndexName("app1", time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, "otel-metrics-app1-2026.05.29", name)
}
