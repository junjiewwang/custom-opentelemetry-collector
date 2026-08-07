// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// sumMetric builds a Sum metric with one data point.
func sumMetric(name string, monotonic bool) pmetric.Metric {
	m := pmetric.NewMetric()
	m.SetName(name)
	s := m.SetEmptySum()
	s.SetIsMonotonic(monotonic)
	dp := s.DataPoints().AppendEmpty()
	dp.SetIntValue(1)
	dp.SetTimestamp(pcommon.Timestamp(1_000_000_000))
	return m
}

// TestConvertMetric_NonMonotonicSumIsGauge guards the mapping used by the
// Prometheus /api/v1/metadata endpoint. An OTel UpDownCounter (non-monotonic
// Sum, e.g. jvm.memory.used) can decrease, so it is a Prometheus gauge; calling
// it a counter made Grafana wrap it in rate(), which is meaningless.
func TestConvertMetric_NonMonotonicSumIsGauge(t *testing.T) {
	pts := ConvertOTLPMetric(sumMetric("jvm.memory.used", false), pcommon.NewResource())
	require.NotEmpty(t, pts)
	assert.Equal(t, "gauge", pts[0].Type)
}

// TestConvertMetric_MonotonicSumIsCounter verifies a true counter still reports
// as one.
func TestConvertMetric_MonotonicSumIsCounter(t *testing.T) {
	pts := ConvertOTLPMetric(sumMetric("requests_total", true), pcommon.NewResource())
	require.NotEmpty(t, pts)
	assert.Equal(t, "counter", pts[0].Type)
}

// TestConvertMetric_GaugeStaysGauge covers the plain Gauge path.
func TestConvertMetric_GaugeStaysGauge(t *testing.T) {
	m := pmetric.NewMetric()
	m.SetName("cpu.utilization")
	dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetDoubleValue(0.5)
	dp.SetTimestamp(pcommon.Timestamp(1_000_000_000))

	pts := ConvertOTLPMetric(m, pcommon.NewResource())
	require.NotEmpty(t, pts)
	assert.Equal(t, "gauge", pts[0].Type)
}
