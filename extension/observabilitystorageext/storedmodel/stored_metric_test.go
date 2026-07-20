// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestConvertHistogramPoints_BucketFields(t *testing.T) {
	dps := pmetric.NewHistogramDataPointSlice()
	dps.EnsureCapacity(1)
	dp := dps.AppendEmpty()
	dp.SetTimestamp(1234567890000000000)
	dp.SetSum(42.0)
	dp.SetCount(3)

	// Bucket bounds and counts.
	bounds := dp.ExplicitBounds()
	bounds.EnsureCapacity(2)
	bounds.Append(0.005, 0.1)

	counts := dp.BucketCounts()
	counts.EnsureCapacity(2)
	counts.Append(1, 2)

	base := StoredMetricDataPoint{
		Name: "test_histogram",
	}

	result := convertHistogramPoints(dps, base)
	require.Len(t, result, 1)

	pt := result[0]
	assert.Equal(t, "histogram", pt.Type)
	assert.Equal(t, "test_histogram", pt.Name)
	assert.Equal(t, 42.0, pt.Value)
	assert.Equal(t, []uint64{1, 2}, pt.BucketCounts)
	assert.Equal(t, []float64{0.005, 0.1}, pt.ExplicitBounds)
}

func TestConvertHistogramPoints_EmptyBuckets(t *testing.T) {
	dps := pmetric.NewHistogramDataPointSlice()
	dps.EnsureCapacity(1)
	dp := dps.AppendEmpty()
	dp.SetTimestamp(1234567890000000000)
	dp.SetSum(10.0)

	base := StoredMetricDataPoint{
		Name: "test",
	}

	result := convertHistogramPoints(dps, base)
	require.Len(t, result, 1)

	pt := result[0]
	assert.Equal(t, 10.0, pt.Value)
	assert.Empty(t, pt.BucketCounts)
	assert.Empty(t, pt.ExplicitBounds)
}
