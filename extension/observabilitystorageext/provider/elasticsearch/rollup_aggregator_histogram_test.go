// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSampleGroup_ToDocHistogram_Cumulative_Diff verifies the temporality fix:
// a cumulative histogram's rollup bucket_counts/value/count must be the
// last-minus-first diff of the window, NOT the element-wise sum. Summing
// cumulative samples (which grow monotonically) inflates the magnitude by the
// number of samples in the window.
func TestSampleGroup_ToDocHistogram_Cumulative_Diff(t *testing.T) {
	g := &sampleGroup{metricType: "histogram", bucketMs: 1700000000000}

	// Cumulative histogram: each sample's bucket_counts is the running total.
	// 3 samples in the window, counts grow from [10,20] -> [12,22] -> [15,25].
	g.add(MetricSample{
		BucketCounts: []int64{10, 20},
		Bounds:       []float64{1.0, 2.0},
		Value:        100,
		Count:        30,
		Temporality:  "cumulative",
	})
	g.add(MetricSample{
		BucketCounts: []int64{12, 22},
		Bounds:       []float64{1.0, 2.0},
		Value:        110,
		Count:        34,
		Temporality:  "cumulative",
	})
	g.add(MetricSample{
		BucketCounts: []int64{15, 25},
		Bounds:       []float64{1.0, 2.0},
		Value:        130,
		Count:        40,
		Temporality:  "cumulative",
	})

	doc := g.toDoc("latency", "app", "1")

	// Window delta = last - first = [15-10, 25-20] = [5, 5].
	assert.Equal(t, []uint64{5, 5}, doc.BucketCounts, "cumulative rollup must diff last-first")
	assert.Equal(t, []float64{1.0, 2.0}, doc.ExplicitBounds, "bounds preserved")
	assert.Equal(t, float64(30), doc.Value, "value = last-first sum")
	assert.Equal(t, int64(10), doc.Count, "count = last-first observation count")
}

// TestSampleGroup_ToDocHistogram_Delta_Accumulate verifies delta-temporality
// histograms accumulate element-wise (the correct behavior for per-interval
// increments).
func TestSampleGroup_ToDocHistogram_Delta_Accumulate(t *testing.T) {
	g := &sampleGroup{metricType: "histogram", bucketMs: 1700000000000}

	g.add(MetricSample{
		BucketCounts: []int64{3, 7},
		Bounds:       []float64{1.0, 2.0},
		Value:        42,
		Count:        10,
		Temporality:  "delta",
	})
	g.add(MetricSample{
		BucketCounts: []int64{4, 8},
		Bounds:       []float64{1.0, 2.0},
		Value:        58,
		Count:        12,
		Temporality:  "delta",
	})

	doc := g.toDoc("latency", "app", "1")

	assert.Equal(t, []uint64{7, 15}, doc.BucketCounts, "delta rollup must accumulate element-wise")
	assert.Equal(t, float64(100), doc.Value, "value = sum of delta sums")
	assert.Equal(t, int64(22), doc.Count, "count = sum of observation counts")
}

// TestSampleGroup_ToDocHistogram_Legacy_Cumulative verifies legacy docs (empty
// temporality) are treated as cumulative, not delta — the safe default because
// summing legacy cumulative samples would inflate magnitude.
func TestSampleGroup_ToDocHistogram_Legacy_Cumulative(t *testing.T) {
	g := &sampleGroup{metricType: "histogram", bucketMs: 1700000000000}

	g.add(MetricSample{BucketCounts: []int64{10, 20}, Value: 100, Count: 30}) // temporality ""
	g.add(MetricSample{BucketCounts: []int64{15, 25}, Value: 130, Count: 40})

	doc := g.toDoc("latency", "app", "1")

	assert.Equal(t, []uint64{5, 5}, doc.BucketCounts, "legacy empty temporality treated as cumulative diff")
}

// TestSampleGroup_ToDocHistogram_Cumulative_ResetClamp verifies a cumulative
// reset within the window (last < first) clamps negative diffs to 0 instead of
// overflowing uint64.
func TestSampleGroup_ToDocHistogram_Cumulative_ResetClamp(t *testing.T) {
	g := &sampleGroup{metricType: "histogram", bucketMs: 1700000000000}

	g.add(MetricSample{BucketCounts: []int64{15, 25}, Value: 130, Count: 40, Temporality: "cumulative"})
	g.add(MetricSample{BucketCounts: []int64{2, 3}, Value: 5, Count: 5, Temporality: "cumulative"}) // reset

	doc := g.toDoc("latency", "app", "1")

	// last-first = [2-15, 3-25] = [-13, -22] -> clamped to [0, 0].
	assert.Equal(t, []uint64{0, 0}, doc.BucketCounts, "negative diffs clamp to 0")
}
