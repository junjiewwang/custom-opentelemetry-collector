// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// StoredMetricDataPoint is the unified storage type for metric data points.
// Each data point becomes a separate document. Field names align with OTLP JSON.
type StoredMetricDataPoint struct {
	TimeUnixMilli int64          `json:"timeUnixMilli"`
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	Value         float64        `json:"value"`
	Labels        map[string]any `json:"labels,omitempty"`
	Resource      map[string]any `json:"resource,omitempty"`
	ServiceName   string         `json:"serviceName"`
	AppID         string         `json:"appId,omitempty"`
	// Unit is the OTel metric unit (e.g. "By"=bytes, "1"=count, "ms", "s").
	// Backs the Prometheus /metadata "unit" field so Grafana Metrics Drilldown
	// can show "Unit: bytes" instead of a bare number.
	Unit string `json:"unit,omitempty"`

	// Histogram-specific fields (present only when Type="histogram").
	BucketCounts  []uint64  `json:"bucket_counts,omitempty"`
	ExplicitBounds []float64 `json:"explicit_bounds,omitempty"`
	// AggregationTemporality records the histogram's aggregation temporality
	// ("cumulative" or "delta"). Empty for non-histogram metrics and for
	// documents written before this field existed (treated as cumulative on read).
	// It is read at the metric level (pmetric.Histogram.AggregationTemporality),
	// NOT the data-point level, because temporality is a metric-level property.
	AggregationTemporality string `json:"aggregation_temporality,omitempty"`

	// Rollup-specific fields (present only in rollup tier documents).
	// Tier identifies the rollup resolution ("5m"), empty for raw docs.
	Tier string `json:"_tier,omitempty"`
	// Count is type-dependent: for histogram docs it is the observation count
	// (dp.Count()); for rollup gauge/counter docs it is the number of raw samples
	// folded into the bucket.
	Count int64 `json:"count,omitempty"`
	// Min/Max are the window min/max for gauge-type metrics.
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// Sum is the sum of raw values in the window (used for avg recomputation).
	Sum float64 `json:"sum,omitempty"`
	// First/Last are the window boundary values for counter-type metrics.
	// rate()/increase() on counters MUST use first/last, never avg.
	First float64 `json:"first,omitempty"`
	Last  float64 `json:"last,omitempty"`
}

// MetricMeta holds a metric's stored type and unit, returned by ListMetricTypes.
// Lives in storedmodel so both the observabilitystorageext interface and the
// elasticsearch provider can reference it without an import cycle.
type MetricMeta struct {
	Type string // "gauge", "counter", "histogram", "summary"
	Unit string // OTel unit (e.g. "By"=bytes, "1"=count, "ms", "s")
}

// ConvertOTLPMetric converts an OTLP metric to one or more StoredMetricDataPoint.
// Each data point (gauge value, sum data point, histogram point, summary point)
// becomes an independent document.
func ConvertOTLPMetric(metric pmetric.Metric, resource pcommon.Resource) []StoredMetricDataPoint {
	resourceAttrs := resource.Attributes()
	serviceName := getAttrStr(resourceAttrs, "service.name", "unknown")
	appID := getAppIDAttr(resourceAttrs)

	base := StoredMetricDataPoint{
		Name:        metric.Name(),
		ServiceName: serviceName,
		AppID:       appID,
		Unit:        metric.Unit(),
		Resource:    pcommonMapToFlat(resourceAttrs),
	}

	switch metric.Type() {
	case pmetric.MetricTypeGauge:
		return convertNumberPoints(metric.Gauge().DataPoints(), "gauge", base, "")
	case pmetric.MetricTypeSum:
		// Only a monotonic Sum is a counter. A non-monotonic Sum is an OTel
		// UpDownCounter (jvm.memory.used, jvm.thread.count, ...), which can
		// decrease and maps to a Prometheus gauge — the same mapping the OTel
		// Prometheus exporter uses. Reporting those as counters made Grafana
		// Metrics Drilldown wrap them in rate(), which is meaningless for a
		// value that goes up and down.
		if metric.Sum().IsMonotonic() {
			return convertNumberPoints(metric.Sum().DataPoints(), "counter", base,
				temporalityString(metric.Sum().AggregationTemporality()))
		}
		return convertNumberPoints(metric.Sum().DataPoints(), "gauge", base, "")
	case pmetric.MetricTypeHistogram:
		return convertHistogramPoints(metric.Histogram().DataPoints(), base, temporalityString(metric.Histogram().AggregationTemporality()))
	case pmetric.MetricTypeSummary:
		return convertSummaryPoints(metric.Summary().DataPoints(), base)
	default:
		return nil
	}
}

func convertNumberPoints(dps any, kind string, base StoredMetricDataPoint, temporality string) []StoredMetricDataPoint {
	var result []StoredMetricDataPoint
	switch pts := dps.(type) {
	case pmetric.NumberDataPointSlice:
		result = make([]StoredMetricDataPoint, pts.Len())
		for i := 0; i < pts.Len(); i++ {
			dp := pts.At(i)
			pt := base
			pt.TimeUnixMilli = int64(dp.Timestamp()) / 1e6
			pt.Type = kind
			pt.Labels = pcommonMapToFlatMetric(dp.Attributes())
			pt.AggregationTemporality = temporality
			switch dp.ValueType() {
			case pmetric.NumberDataPointValueTypeDouble:
				pt.Value = dp.DoubleValue()
			case pmetric.NumberDataPointValueTypeInt:
				pt.Value = float64(dp.IntValue())
			}
			result[i] = pt
		}
	case pmetric.HistogramDataPointSlice, pmetric.SummaryDataPointSlice:
		return nil
	}
	return result
}

func convertHistogramPoints(dps pmetric.HistogramDataPointSlice, base StoredMetricDataPoint, temporality string) []StoredMetricDataPoint {
	result := make([]StoredMetricDataPoint, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		pt := base
		pt.TimeUnixMilli = int64(dp.Timestamp()) / 1e6
		pt.Type = "histogram"
		pt.Labels = pcommonMapToFlatMetric(dp.Attributes())
		pt.AggregationTemporality = temporality
		pt.Count = int64(dp.Count())
		if dp.HasSum() {
			pt.Value = dp.Sum()
		}
		pt.BucketCounts = dp.BucketCounts().AsRaw()
		pt.ExplicitBounds = dp.ExplicitBounds().AsRaw()
		result[i] = pt
	}
	return result
}

// temporalityString maps pmetric.AggregationTemporality to the storage string
// ("cumulative"/"delta"). Returns "" for Unspecified (treated as cumulative on
// read for backward compatibility).
func temporalityString(at pmetric.AggregationTemporality) string {
	switch at {
	case pmetric.AggregationTemporalityCumulative:
		return "cumulative"
	case pmetric.AggregationTemporalityDelta:
		return "delta"
	default:
		return ""
	}
}

func convertSummaryPoints(dps pmetric.SummaryDataPointSlice, base StoredMetricDataPoint) []StoredMetricDataPoint {
	result := make([]StoredMetricDataPoint, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		pt := base
		pt.TimeUnixMilli = int64(dp.Timestamp()) / 1e6
		pt.Type = "summary"
		pt.Labels = pcommonMapToFlatMetric(dp.Attributes())
		pt.Value = dp.Sum()
		result[i] = pt
	}
	return result
}
