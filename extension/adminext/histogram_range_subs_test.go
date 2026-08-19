// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCumulativeHistogramBucketAt_Cumulative verifies that for cumulative
// temporality, the function returns the latest snapshot ≤ t directly (the
// snapshot already carries the full cumulative state).
func TestCumulativeHistogramBucketAt_Cumulative(t *testing.T) {
	samples := []HistogramSample{
		{TimestampMs: 1000, Value: 100, Count: 10, BucketCounts: []int64{3, 7}, Bounds: []float64{1, 2}, Temporality: "cumulative"},
		{TimestampMs: 2000, Value: 150, Count: 15, BucketCounts: []int64{4, 11}, Bounds: []float64{1, 2}, Temporality: "cumulative"},
		{TimestampMs: 3000, Value: 200, Count: 20, BucketCounts: []int64{5, 15}, Bounds: []float64{1, 2}, Temporality: "cumulative"},
	}

	// t=2500 → latest snapshot is the 2000 sample.
	hb := cumulativeHistogramBucketAt(samples, 2500)
	assert.Equal(t, float64(150), hb.TotalSum)
	assert.Equal(t, int64(15), hb.TotalCount)
	assert.Equal(t, []int64{4, 11}, hb.BucketCounts)

	// t=3000 → latest snapshot is the 3000 sample.
	hb = cumulativeHistogramBucketAt(samples, 3000)
	assert.Equal(t, float64(200), hb.TotalSum)
	assert.Equal(t, int64(20), hb.TotalCount)

	// t=500 (before any sample) → empty (no bounds).
	hb = cumulativeHistogramBucketAt(samples, 500)
	assert.Empty(t, hb.Bounds)
}

// TestCumulativeHistogramBucketAt_Delta verifies delta temporality accumulates
// per-interval increments up to t.
func TestCumulativeHistogramBucketAt_Delta(t *testing.T) {
	samples := []HistogramSample{
		{TimestampMs: 1000, Value: 10, Count: 3, BucketCounts: []int64{1, 2}, Bounds: []float64{1, 2}, Temporality: "delta"},
		{TimestampMs: 2000, Value: 20, Count: 5, BucketCounts: []int64{2, 3}, Bounds: []float64{1, 2}, Temporality: "delta"},
		{TimestampMs: 3000, Value: 30, Count: 7, BucketCounts: []int64{4, 1}, Bounds: []float64{1, 2}, Temporality: "delta"},
	}

	// t=2500 → accumulate first two increments.
	hb := cumulativeHistogramBucketAt(samples, 2500)
	assert.Equal(t, float64(30), hb.TotalSum) // 10+20
	assert.Equal(t, int64(8), hb.TotalCount)  // 3+5
	assert.Equal(t, []int64{3, 5}, hb.BucketCounts) // 1+2, 2+3

	// t=3000 → all three.
	hb = cumulativeHistogramBucketAt(samples, 3000)
	assert.Equal(t, float64(60), hb.TotalSum)
	assert.Equal(t, int64(15), hb.TotalCount)
	assert.Equal(t, []int64{7, 6}, hb.BucketCounts)
}

// TestCumulativeHistogramBucketAt_Legacy verifies legacy docs (empty temporality)
// are treated as cumulative (latest snapshot).
func TestCumulativeHistogramBucketAt_Legacy(t *testing.T) {
	samples := []HistogramSample{
		{TimestampMs: 1000, Value: 100, BucketCounts: []int64{3, 7}, Bounds: []float64{1, 2}}, // Temporality ""
		{TimestampMs: 2000, Value: 150, BucketCounts: []int64{4, 11}, Bounds: []float64{1, 2}},
	}

	hb := cumulativeHistogramBucketAt(samples, 2500)
	// Legacy without Count: TotalCount derives from Σ bucket_counts.
	assert.Equal(t, float64(150), hb.TotalSum)
	assert.Equal(t, int64(15), hb.TotalCount) // 4+11
	assert.Equal(t, []int64{4, 11}, hb.BucketCounts)
}

// TestLatestValueAt verifies the ≤t latest-value lookup for non-histogram fallback.
func TestLatestValueAt(t *testing.T) {
	samples := []HistogramSample{
		{TimestampMs: 1000, Value: 1.0},
		{TimestampMs: 2000, Value: 2.0},
		{TimestampMs: 3000, Value: 3.0},
	}
	assert.Equal(t, 2.0, latestValueAt(samples, 2500))
	assert.Equal(t, 3.0, latestValueAt(samples, 3000))
	assert.True(t, isNaN(latestValueAt(samples, 500)))
}

func isNaN(v float64) bool { return v != v }
