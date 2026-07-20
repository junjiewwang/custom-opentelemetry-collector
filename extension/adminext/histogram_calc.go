// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import "sort"

// ═══════════════════════════════════════════════════
// Histogram Quantile Calculator
// ═══════════════════════════════════════════════════
//
// Computes Prometheus histogram_quantile(θ, bucket_counts) in-process
// from ES histogram documents. Each document stores a complete snapshot
// of bucket_counts + explicit_bounds, so we aggregate across documents
// and compute the quantile via linear interpolation.
//
// Isolated as a pure function with no external dependencies for
// testability and clarity.

// HistogramBucket holds the aggregated bucket data for one label group.
type HistogramBucket struct {
	Labels       map[string]string
	TotalCount   int64
	TotalSum     float64
	BucketCounts []int64
	Bounds       []float64
}

// HistogramQuantileResult holds the computed quantile value.
type HistogramQuantileResult struct {
	Labels map[string]string
	Value  float64 // NaN if not computable
}

// AggregateHistogramBuckets sums bucket_counts across multiple histogram
// data points (all belonging to the same label group).
// Each data point represents one flush cycle's worth of histograms.
func AggregateHistogramBuckets(buckets []HistogramBucket) []HistogramBucket {
	// Group by label key (sorted label pairs).
	type key struct{ s string }
	groups := make(map[key]*HistogramBucket)

	for _, b := range buckets {
		k := key{s: sortedLabelKey(b.Labels)}
		existing, ok := groups[k]
		if !ok {
			cp := HistogramBucket{
				Labels:       copyMap(b.Labels),
				TotalCount:   b.TotalCount,
				TotalSum:     b.TotalSum,
				BucketCounts: make([]int64, len(b.Bounds)),
				Bounds:       b.Bounds,
			}
			copy(cp.BucketCounts, b.BucketCounts)
			groups[k] = &cp
		} else {
			existing.TotalCount += b.TotalCount
			existing.TotalSum += b.TotalSum
			// Align bucket counts to the longer bounds.
			if len(b.Bounds) > len(existing.Bounds) {
				expanded := make([]int64, len(b.Bounds))
				copy(expanded, existing.BucketCounts)
				existing.BucketCounts = expanded
				existing.Bounds = b.Bounds
			}
			for i := range b.BucketCounts {
				if i < len(existing.BucketCounts) {
					existing.BucketCounts[i] += b.BucketCounts[i]
				}
			}
		}
	}

	result := make([]HistogramBucket, 0, len(groups))
	for _, g := range groups {
		result = append(result, *g)
	}
	return result
}

// ComputeHistogramQuantile calculates the quantile (0 ≤ θ ≤ 1) from
// aggregated bucket counts.
//
// Uses the same algorithm as Prometheus histogram_quantile:
//
//	rank = θ * totalCount
//	find bucket i where cumulative[i] ≥ rank
//	interpolate: lower + (rank - lowerCumul) / bucketCount * (upper - lower)
func ComputeHistogramQuantile(θ float64, hb HistogramBucket) float64 {
	if hb.TotalCount == 0 || len(hb.Bounds) == 0 || len(hb.BucketCounts) == 0 {
		return 0
	}

	rank := θ * float64(hb.TotalCount)

	var cumulative int64
	for i := 0; i < len(hb.Bounds); i++ {
		cumulative += hb.BucketCounts[i]
		if float64(cumulative) >= rank {
			// Found the bucket.
			lowerCumul := float64(cumulative - hb.BucketCounts[i])
			bucketCount := float64(hb.BucketCounts[i])

			var lower float64
			if i > 0 {
				lower = hb.Bounds[i-1]
			}

			if bucketCount == 0 {
				return lower
			}

			return lower + (rank-lowerCumul)/bucketCount*(hb.Bounds[i]-lower)
		}
	}

	// Above last bucket: use average of remaining values.
	if hb.TotalSum > 0 && hb.TotalCount > 0 {
		return hb.TotalSum / float64(hb.TotalCount)
	}
	return hb.Bounds[len(hb.Bounds)-1]
}

// sortedLabelKey returns a deterministic string key for label maps,
// sorted alphabetically by key name.
func sortedLabelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	for _, k := range keys {
		buf = append(buf, k...)
		buf = append(buf, '=')
		buf = append(buf, labels[k]...)
		buf = append(buf, '\x00')
	}
	return string(buf)
}

func copyMap(m map[string]string) map[string]string {
	r := make(map[string]string, len(m))
	for k, v := range m {
		r[k] = v
	}
	return r
}
