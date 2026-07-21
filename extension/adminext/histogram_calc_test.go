// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeHistogramQuantile_Basic(t *testing.T) {
	// 100 requests:
	//   le=0.005: 10 requests → cumulative=10 (10%)
	//   le=0.010: 40 requests → cumulative=50 (50%)
	//   le=0.025: 30 requests → cumulative=80 (80%)
	//   le=0.050: 15 requests → cumulative=95 (95%)
	//   le=0.100: 5  requests → cumulative=100 (100%)
	hb := HistogramBucket{
		TotalCount:   100,
		BucketCounts: []int64{10, 40, 30, 15, 5},
		Bounds:       []float64{0.005, 0.01, 0.025, 0.05, 0.1},
	}

	t.Run("p50", func(t *testing.T) {
		// p50 → rank = 50 → in bucket[1] (le=0.01)
		// lowerCumul=10, count=40, lower=0.005, upper=0.01
		// 0.005 + (50-10)/40 * (0.01-0.005) = 0.005 + 1.0 * 0.005 = 0.01
		q := ComputeHistogramQuantile(0.5, hb)
		assert.InDelta(t, 0.01, q, 1e-9)
	})

	t.Run("p90", func(t *testing.T) {
		// p90 → rank = 90 → in bucket[3] (le=0.05)
		// lowerCumul=80, count=15, lower=0.025, upper=0.05
		// 0.025 + (90-80)/15 * 0.025 = 0.025 + 0.6667*0.025 = 0.04167
		q := ComputeHistogramQuantile(0.9, hb)
		assert.InDelta(t, 0.041667, q, 1e-4)
	})

	t.Run("p95", func(t *testing.T) {
		// p95 → rank = 95 → in bucket[3] (le=0.05)
		// 0.025 + (95-80)/15 * 0.025 = 0.025 + 1.0 * 0.025 = 0.05
		q := ComputeHistogramQuantile(0.95, hb)
		assert.InDelta(t, 0.05, q, 1e-9)
	})

	t.Run("p99", func(t *testing.T) {
		// p99 → rank = 99 → in bucket[4] (le=0.1)
		// lowerCumul=95, count=5, lower=0.05, upper=0.1
		// 0.05 + (99-95)/5 * 0.05 = 0.05 + 0.8*0.05 = 0.09
		q := ComputeHistogramQuantile(0.99, hb)
		assert.InDelta(t, 0.09, q, 1e-9)
	})
}

func TestComputeHistogramQuantile_FirstBucket(t *testing.T) {
	// All entries in first bucket → p50 should interpolate within [0, 0.005].
	hb := HistogramBucket{
		TotalCount:   100,
		BucketCounts: []int64{100, 0, 0},
		Bounds:       []float64{0.005, 0.01, 0.025},
	}
	q := ComputeHistogramQuantile(0.5, hb)
	assert.True(t, q > 0 && q < 0.005, "p50 within first bucket [0, 0.005), got %f", q)
	assert.InDelta(t, 0.0025, q, 1e-4)
}

func TestComputeHistogramQuantile_Empty(t *testing.T) {
	t.Run("zero count", func(t *testing.T) {
		hb := HistogramBucket{TotalCount: 0, BucketCounts: []int64{0}, Bounds: []float64{1}}
		assert.Equal(t, float64(0), ComputeHistogramQuantile(0.9, hb))
	})

	t.Run("no bounds", func(t *testing.T) {
		hb := HistogramBucket{TotalCount: 10, BucketCounts: nil, Bounds: nil}
		assert.Equal(t, float64(0), ComputeHistogramQuantile(0.9, hb))
	})
}

func TestComputeHistogramQuantile_AboveLastBucket(t *testing.T) {
	// All entries above the last bucket (impossible normally, but defensive).
	hb := HistogramBucket{
		TotalCount:   100,
		TotalSum:     50.0,
		BucketCounts: []int64{0, 0, 0},
		Bounds:       []float64{0.01, 0.05, 0.1},
	}
	// rank=90, cumulative never reaches 90 → falls through to avg
	q := ComputeHistogramQuantile(0.9, hb)
	assert.InDelta(t, 0.5, q, 1e-9, "fallback to totalSum/totalCount avg")
}

func TestComputeHistogramQuantile_ZeroBucketCount(t *testing.T) {
	// Bucket with zero count between populated buckets.
	// p0.99 → rank=49.5, cumulative skips zero bucket:
	//   bucket[0]: cumul=49 < 49.5
	//   bucket[1]: cumul=49+0=49 < 49.5
	//   bucket[2]: cumul=49+1=50 ≥ 49.5 → found
	//   lowerCumul=49, count=1, lower=0.05, upper=0.1
	//   0.05 + (49.5-49)/1 * (0.1-0.05) = 0.05 + 0.025 = 0.075
	hb := HistogramBucket{
		TotalCount:   50,
		BucketCounts: []int64{49, 0, 1},
		Bounds:       []float64{0.01, 0.05, 0.1},
	}
	q := ComputeHistogramQuantile(0.99, hb)
	assert.InDelta(t, 0.075, q, 1e-9)
}

func TestAggregateHistogramBuckets(t *testing.T) {
	buckets := []HistogramBucket{
		{
			Labels:       map[string]string{"client": "A", "server": "B"},
			TotalCount:   10,
			BucketCounts: []int64{5, 3, 2},
			Bounds:       []float64{0.005, 0.01, 0.025},
		},
		{
			Labels:       map[string]string{"client": "A", "server": "B"},
			TotalCount:   20,
			BucketCounts: []int64{8, 7, 5},
			Bounds:       []float64{0.005, 0.01, 0.025},
		},
		{
			Labels:       map[string]string{"client": "X", "server": "Y"},
			TotalCount:   5,
			BucketCounts: []int64{2, 2, 1},
			Bounds:       []float64{0.005, 0.01, 0.025},
		},
	}

	agg := AggregateHistogramBuckets(buckets)
	assert.Len(t, agg, 2)

	for _, a := range agg {
		switch a.Labels["client"] {
		case "A":
			assert.Equal(t, int64(30), a.TotalCount)
			assert.Equal(t, []int64{13, 10, 7}, a.BucketCounts)
		case "X":
			assert.Equal(t, int64(5), a.TotalCount)
			assert.Equal(t, []int64{2, 2, 1}, a.BucketCounts)
		}
	}
}

func TestSortedLabelKey(t *testing.T) {
	// Key stability: same labels → same key regardless of iteration order.
	k1 := sortedLabelKey(map[string]string{"b": "2", "a": "1"})
	k2 := sortedLabelKey(map[string]string{"a": "1", "b": "2"})
	assert.Equal(t, k1, k2)

	// Different values → different keys.
	k3 := sortedLabelKey(map[string]string{"a": "1"})
	assert.NotEqual(t, k1, k3)

	// Empty.
	assert.Equal(t, "", sortedLabelKey(nil))
}

func TestComputeHistogramQuantile_MultipleQuantiles(t *testing.T) {
	hb := HistogramBucket{
		TotalCount:   100,
		BucketCounts: []int64{10, 40, 30, 15, 5},
		Bounds:       []float64{0.005, 0.01, 0.025, 0.05, 0.1},
	}

	// p50 < p90 < p99
	p50 := ComputeHistogramQuantile(0.5, hb)
	p90 := ComputeHistogramQuantile(0.9, hb)
	p99 := ComputeHistogramQuantile(0.99, hb)
	assert.True(t, p50 < p90, "p50=%f should be < p90=%f", p50, p90)
	assert.True(t, p90 < p99, "p90=%f should be < p99=%f", p90, p99)
}

func TestComputeHistogramQuantile_EdgeThetaValues(t *testing.T) {
	hb := HistogramBucket{
		TotalCount:   100,
		BucketCounts: []int64{50, 50},
		Bounds:       []float64{0.01, 0.1},
	}

	q0 := ComputeHistogramQuantile(0.0, hb)
	assert.InDelta(t, 0.0, q0, 1e-9, "θ=0 → lower=0")

	// θ=0.5 → rank=50 → in bucket[0], lower=0, count=50, upper=0.01
	// 0 + (50-0)/50 * 0.01 = 0.01
	q50 := ComputeHistogramQuantile(0.5, hb)
	assert.InDelta(t, 0.01, q50, 1e-9)

	q100 := ComputeHistogramQuantile(1.0, hb)
	// rank=100 → in bucket[1], lower=0.01, count=50, upper=0.1
	// 0.01 + (100-50)/50 * 0.09 = 0.01 + 0.09 = 0.1
	assert.InDelta(t, 0.1, q100, 1e-9)
}

func TestComputeHistogramQuantile_NegativeBucketCount(t *testing.T) {
	// Defensive: negative bucket counts should not crash.
	hb := HistogramBucket{
		TotalCount:   0,
		BucketCounts: []int64{-1, 5},
		Bounds:       []float64{0.01, 0.1},
	}
	assert.False(t, math.IsNaN(ComputeHistogramQuantile(0.9, hb)))
}

func TestAggregateHistogramSamples(t *testing.T) {
	samples := []HistogramSample{
		{TimestampMs: 1000, Value: 0.01, BucketCounts: []int64{10, 5}, Bounds: []float64{0.01, 0.1}},
		{TimestampMs: 2000, Value: 0.02, BucketCounts: []int64{8, 7}, Bounds: []float64{0.01, 0.1}},
		{TimestampMs: 3000, Value: 0.03, BucketCounts: []int64{6, 9}, Bounds: []float64{0.01, 0.1}},
	}

	t.Run("full window", func(t *testing.T) {
		hb := AggregateHistogramSamples(samples, 500, 3500)
		assert.Equal(t, int64(45), hb.TotalCount)
		assert.InDelta(t, 0.06, hb.TotalSum, 1e-9)
		assert.Equal(t, []int64{24, 21}, hb.BucketCounts)
	})

	t.Run("partial window", func(t *testing.T) {
		hb := AggregateHistogramSamples(samples, 1500, 2500)
		assert.Equal(t, int64(15), hb.TotalCount) // 8+7 from sample at 2000
		assert.Equal(t, []int64{8, 7}, hb.BucketCounts)
	})

	t.Run("no matching samples", func(t *testing.T) {
		hb := AggregateHistogramSamples(samples, 100, 200)
		assert.Equal(t, int64(0), hb.TotalCount)
	})

	t.Run("no bucket data", func(t *testing.T) {
		plain := []HistogramSample{
			{TimestampMs: 1000, Value: 5.0},
		}
		hb := AggregateHistogramSamples(plain, 500, 1500)
		assert.Equal(t, int64(0), hb.TotalCount)
		assert.InDelta(t, 5.0, hb.TotalSum, 1e-9)
	})
}
