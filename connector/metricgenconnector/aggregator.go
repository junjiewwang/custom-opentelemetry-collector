// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import "sync/atomic"

// counter is a thread-safe monotonic counter.
type counter struct {
	value atomic.Int64
}

func (c *counter) Add(delta int64) {
	c.value.Add(delta)
}

func (c *counter) Load() int64 {
	return c.value.Load()
}

func (c *counter) Swap() int64 {
	return c.value.Swap(0)
}

// histogram is a lock-free bucket histogram with sum and count.
// It is NOT safe for concurrent writes from multiple goroutines;
// each series is owned by a single aggregator shard.
type histogram struct {
	buckets []atomic.Int64
	bounds  []float64
	sum     atomic.Int64
	count   atomic.Int64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{
		bounds:  bounds,
		buckets: make([]atomic.Int64, len(bounds)),
	}
}

// Record adds a value to the histogram.
func (h *histogram) Record(val float64) {
	h.sum.Add(int64(val * 1e6)) // microsecond precision
	h.count.Add(1)

	// Linear scan for the correct bucket.
	for i, b := range h.bounds {
		if val <= b {
			h.buckets[i].Add(1)
			return
		}
	}
}

// Snapshot returns a copy of the current histogram state and resets all counters.
func (h *histogram) Snapshot() (buckets []uint64, bounds []float64, sum int64, count uint64) {
	buckets = make([]uint64, len(h.buckets))
	for i := range h.buckets {
		buckets[i] = uint64(h.buckets[i].Swap(0))
	}
	bounds = h.bounds
	sum = h.sum.Swap(0)
	count = uint64(h.count.Swap(0))
	return
}
