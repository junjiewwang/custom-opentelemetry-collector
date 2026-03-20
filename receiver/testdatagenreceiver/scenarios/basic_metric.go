// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package scenarios

import (
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"

	tdg "go.opentelemetry.io/collector/custom/receiver/testdatagenreceiver"
)

const basicMetricName = "basic_metric"

func init() {
	tdg.Register(basicMetricName, func() tdg.Scenario {
		return &BasicMetricScenario{}
	})
}

// BasicMetricScenario 基础指标场景
// 生成多种类型的 Metric 数据（Gauge / Sum / Histogram）
type BasicMetricScenario struct {
	tdg.BaseScenario

	serviceName    string
	metricCount    int
	dataPointCount int
}

func (s *BasicMetricScenario) Name() string      { return basicMetricName }
func (s *BasicMetricScenario) Type() tdg.DataType { return tdg.DataTypeMetrics }

func (s *BasicMetricScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "metric-test-service")
	s.metricCount = tdg.ParseInt(cfg, "metric_count", 5)
	s.dataPointCount = tdg.ParseInt(cfg, "data_point_count", 3)
	return nil
}

func (s *BasicMetricScenario) GenerateMetrics() (pmetric.Metrics, error) {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()

	resAttrs := rm.Resource().Attributes()
	resAttrs.PutStr("service.name", s.serviceName)
	resAttrs.PutStr("telemetry.sdk.language", "go")
	resAttrs.PutStr("telemetry.sdk.name", "opentelemetry")

	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("testdatagen/basic_metric")
	sm.Scope().SetVersion("1.0.0")

	now := time.Now()
	r := rand.New(rand.NewSource(now.UnixNano()))

	metricDefs := []struct {
		name   string
		unit   string
		mtype  string // gauge / sum / histogram
	}{
		{"system.cpu.utilization", "1", "gauge"},
		{"system.memory.usage", "By", "gauge"},
		{"http.server.request.duration", "s", "histogram"},
		{"http.server.request.count", "1", "sum"},
		{"process.runtime.go.goroutines", "1", "gauge"},
		{"db.client.connections.usage", "1", "gauge"},
		{"http.client.request.duration", "s", "histogram"},
		{"runtime.jvm.memory.used", "By", "gauge"},
	}

	for i := 0; i < s.metricCount && i < len(metricDefs); i++ {
		def := metricDefs[i]
		m := sm.Metrics().AppendEmpty()
		m.SetName(def.name)
		m.SetUnit(def.unit)

		switch def.mtype {
		case "gauge":
			m.SetEmptyGauge()
			for dp := 0; dp < s.dataPointCount; dp++ {
				point := m.Gauge().DataPoints().AppendEmpty()
				point.SetTimestamp(pcommon.NewTimestampFromTime(now.Add(-time.Duration(s.dataPointCount-dp) * 10 * time.Second)))
				point.SetDoubleValue(r.Float64() * 100)
				point.Attributes().PutStr("host.name", fmt.Sprintf("host-%d", dp%3))
			}

		case "sum":
			m.SetEmptySum()
			m.Sum().SetIsMonotonic(true)
			m.Sum().SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			for dp := 0; dp < s.dataPointCount; dp++ {
				point := m.Sum().DataPoints().AppendEmpty()
				point.SetTimestamp(pcommon.NewTimestampFromTime(now.Add(-time.Duration(s.dataPointCount-dp) * 10 * time.Second)))
				point.SetIntValue(int64(1000 + r.Intn(5000)))
				point.Attributes().PutStr("http.method", tdg.RandomPick([]string{"GET", "POST", "PUT", "DELETE"}))
				point.Attributes().PutInt("http.status_code", int64(200+r.Intn(4)*100))
			}

		case "histogram":
			m.SetEmptyHistogram()
			m.Histogram().SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			for dp := 0; dp < s.dataPointCount; dp++ {
				point := m.Histogram().DataPoints().AppendEmpty()
				point.SetTimestamp(pcommon.NewTimestampFromTime(now.Add(-time.Duration(s.dataPointCount-dp) * 10 * time.Second)))
				point.SetCount(uint64(100 + r.Intn(900)))
				point.SetSum(r.Float64() * 50)
				point.SetMin(r.Float64() * 0.01)
				point.SetMax(r.Float64() * 5)
				point.ExplicitBounds().FromRaw([]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10})
				point.BucketCounts().FromRaw([]uint64{10, 20, 30, 50, 100, 150, 80, 40, 15, 5, 2, 1})
				point.Attributes().PutStr("http.method", "GET")
				point.Attributes().PutStr("http.route", fmt.Sprintf("/api/v1/resource_%d", dp))
			}
		}
	}

	return md, nil
}
