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

	// Histogram-specific fields (present only when Type="histogram").
	BucketCounts  []uint64  `json:"bucket_counts,omitempty"`
	ExplicitBounds []float64 `json:"explicit_bounds,omitempty"`
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
		Resource:    pcommonMapToFlat(resourceAttrs),
	}

	switch metric.Type() {
	case pmetric.MetricTypeGauge:
		return convertNumberPoints(metric.Gauge().DataPoints(), "gauge", base)
	case pmetric.MetricTypeSum:
		return convertNumberPoints(metric.Sum().DataPoints(), "counter", base)
	case pmetric.MetricTypeHistogram:
		return convertHistogramPoints(metric.Histogram().DataPoints(), base)
	case pmetric.MetricTypeSummary:
		return convertSummaryPoints(metric.Summary().DataPoints(), base)
	default:
		return nil
	}
}

func convertNumberPoints(dps any, kind string, base StoredMetricDataPoint) []StoredMetricDataPoint {
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

func convertHistogramPoints(dps pmetric.HistogramDataPointSlice, base StoredMetricDataPoint) []StoredMetricDataPoint {
	result := make([]StoredMetricDataPoint, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		pt := base
		pt.TimeUnixMilli = int64(dp.Timestamp()) / 1e6
		pt.Type = "histogram"
		pt.Labels = pcommonMapToFlatMetric(dp.Attributes())
		if dp.HasSum() {
			pt.Value = dp.Sum()
		}
		pt.BucketCounts = dp.BucketCounts().AsRaw()
		pt.ExplicitBounds = dp.ExplicitBounds().AsRaw()
		result[i] = pt
	}
	return result
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
