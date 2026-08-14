// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// TestSampleGroup_ToDocCounter_ValueIsLast verifies the counter rollup fix:
// a counter's rollup `value` must be the window's LAST sample (the monotonic
// instantaneous reading), NOT the sum. The read path (QueryRange) aggregates
// `value` directly; averaging a window-sum produced non-monotonic (seemingly
// decreasing) counter lines.
func TestSampleGroup_ToDocCounter_ValueIsLast(t *testing.T) {
	g := &sampleGroup{
		metricType: "counter",
		bucketMs:   1700000000000,
	}
	// Simulate a monotonic counter: 5 samples in a 5m window.
	for _, v := range []float64{100, 110, 120, 130, 140} {
		g.add(MetricSample{Value: v})
	}

	doc := g.toDoc("requests.total", "app", "1")

	// Value must be last (140), not sum (600).
	assert.Equal(t, float64(140), doc.Value, "counter rollup value must be last")
	assert.NotEqual(t, float64(600), doc.Value, "counter rollup value must NOT be sum")

	// first/last/sum are still preserved for rate/increase restoration.
	assert.Equal(t, float64(100), doc.First)
	assert.Equal(t, float64(140), doc.Last)
	assert.Equal(t, float64(600), doc.Sum)
	assert.Equal(t, "counter", doc.Type)
	assert.Equal(t, int64(5), doc.Count)
}

// TestSampleGroup_ToDocGauge_ValueIsAvg guards that the gauge path is unchanged:
// gauge value = avg, not last/sum.
func TestSampleGroup_ToDocGauge_ValueIsAvg(t *testing.T) {
	g := &sampleGroup{metricType: "gauge", bucketMs: 1700000000000}
	for _, v := range []float64{10, 20, 30} {
		g.add(MetricSample{Value: v})
	}
	doc := g.toDoc("cpu.usage", "app", "1")
	assert.Equal(t, float64(20), doc.Value, "gauge rollup value must be avg")
}

// ensure storedmodel is referenced to avoid unused import if assertions change.
var _ = storedmodel.StoredMetricDataPoint{}
