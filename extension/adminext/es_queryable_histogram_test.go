// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// TestExpandHistogramBuckets_DeltaAccumulatesAcrossSamples verifies that DELTA
// bucket_counts (the collector self-telemetry temporality) are accumulated
// ACROSS samples into a monotonic cumulative distribution, so rate()/heatmap
// consumers see a proper histogram instead of single-hit values.
func TestExpandHistogramBuckets_DeltaAccumulatesAcrossSamples(t *testing.T) {
	bounds := []float64{5, 10, 25}

	// Three samples (time-ordered) from a delta histogram: each carries the
	// per-interval bucket increments. Sample1 hits bucket[1] (le=10), sample2
	// hits bucket[2] (le=25), sample3 hits bucket[0] (le=5).
	series := []observabilitystorageext.MetricRawSeries{{
		Labels: map[string]string{"node_id": "n1"},
		Samples: []observabilitystorageext.MetricSample{
			{TimestampMs: 1000, BucketCounts: []int64{0, 1, 0, 0}, Bounds: bounds},
			{TimestampMs: 2000, BucketCounts: []int64{0, 0, 1, 0}, Bounds: bounds},
			{TimestampMs: 3000, BucketCounts: []int64{1, 0, 0, 0}, Bounds: bounds},
		},
	}}

	q := &esQuerier{}
	out := q.expandHistogramBuckets(series)

	// 4 bucket series: le=5, le=10, le=25, le=+Inf
	assert.Len(t, out, 4, "must expand to 4 le series")

	byLe := map[string][]float64{}
	for _, s := range out {
		le := s.Labels["le"]
		for _, sm := range s.Samples {
			byLe[le] = append(byLe[le], sm.Value)
		}
	}

	// Cumulative across samples must be monotonic and match the running sum of
	// bucket counts up to and including each bound.
	// delta per sample: s1={0,1,0,0}(≤10), s2={0,0,1,0}(≤25), s3={1,0,0,0}(≤5)
	// le=5:   cumulative ≤5    = [0, 0, 1]
	// le=10:  cumulative ≤10   = [1, 1, 2]
	// le=25:  cumulative ≤25   = [1, 2, 3]
	// le=+Inf: total           = [1, 2, 3]
	assert.Equal(t, []float64{0, 0, 1}, byLe["5"], "le=5 cumulative")
	assert.Equal(t, []float64{1, 1, 2}, byLe["10"], "le=10 cumulative")
	assert.Equal(t, []float64{1, 2, 3}, byLe["25"], "le=25 cumulative")
	assert.Equal(t, []float64{1, 2, 3}, byLe["+Inf"], "le=+Inf cumulative")
}

// TestExpandHistogramBuckets_NonHistogramPassthrough verifies non-histogram
// series (no BucketCounts) pass through unchanged.
func TestExpandHistogramBuckets_NonHistogramPassthrough(t *testing.T) {
	series := []observabilitystorageext.MetricRawSeries{{
		Labels:  map[string]string{"node_id": "n1"},
		Samples: []observabilitystorageext.MetricSample{{TimestampMs: 1000, Value: 42}},
	}}
	q := &esQuerier{}
	out := q.expandHistogramBuckets(series)
	assert.Len(t, out, 1)
	assert.Equal(t, float64(42), out[0].Samples[0].Value)
	assert.NotContains(t, out[0].Labels, "le")
}
