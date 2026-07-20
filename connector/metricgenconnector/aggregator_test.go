// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCounter_Add(t *testing.T) {
	var c counter
	c.Add(5)
	assert.Equal(t, int64(5), c.Load())
	c.Add(3)
	assert.Equal(t, int64(8), c.Load())
}

func TestCounter_Swap(t *testing.T) {
	var c counter
	c.Add(10)
	old := c.Swap()
	assert.Equal(t, int64(10), old)
	assert.Equal(t, int64(0), c.Load())
}

func TestHistogram_Record(t *testing.T) {
	bounds := []float64{10, 50, 100}
	h := newHistogram(bounds)

	h.Record(5)   // bucket 0
	h.Record(30)  // bucket 1
	h.Record(60)  // bucket 2
	h.Record(200) // overflow (not in any bucket)

	buckets, b, sumMicros, count := h.Snapshot()

	assert.Equal(t, bounds, b)
	assert.Equal(t, uint64(4), count)
	assert.Equal(t, uint64(1), buckets[0]) // 5 → ≤10
	assert.Equal(t, uint64(1), buckets[1]) // 30 → ≤50
	assert.Equal(t, uint64(1), buckets[2]) // 60 → ≤100
	// Overflow (200) is not counted in any bucket.
	assert.True(t, sumMicros > 0)
}

func TestHistogram_SnapshotResets(t *testing.T) {
	h := newHistogram([]float64{10})
	h.Record(5)

	_, _, _, count := h.Snapshot()
	assert.Equal(t, uint64(1), count)

	_, _, _, count2 := h.Snapshot()
	assert.Equal(t, uint64(0), count2, "snapshot should reset counters")
}

func TestNewDimensionSet_HashConsistency(t *testing.T) {
	attrs := map[string]string{
		"service.name": "test-svc",
		"span.name":    "GET /api",
	}

	ds1 := newDimensionSet(attrs)
	ds2 := newDimensionSet(attrs)

	assert.Equal(t, ds1.hash, ds2.hash, "same attrs must produce same hash")
	assert.Equal(t, ds1.keys, ds2.keys)
	assert.Equal(t, ds1.values, ds2.values)
}

func TestNewDimensionSet_UnsortedInput(t *testing.T) {
	// Order of keys in map shouldn't matter.
	ds1 := newDimensionSet(map[string]string{"a": "1", "b": "2"})
	ds2 := newDimensionSet(map[string]string{"b": "2", "a": "1"})

	assert.Equal(t, ds1.hash, ds2.hash)
	assert.Equal(t, ds1.keys, ds2.keys)
	assert.Equal(t, ds1.values, ds2.values)
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := CreateDefaultConfig().(*Config)

	assert.Equal(t, 15, int(cfg.MetricsFlushInterval.Seconds()))
	assert.Equal(t, 2000, cfg.CardinalityLimit)
	assert.True(t, cfg.RED.Enabled)
	assert.NotEmpty(t, cfg.RED.Dimensions)
	assert.NotEmpty(t, cfg.RED.Histogram.Buckets)
	assert.True(t, cfg.ServiceGraph.Enabled)
}

func TestConfig_Validate(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	assert.NoError(t, err)
}
