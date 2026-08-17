// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// esTruncatingFlatReader is a fake MetricReader for the PromQL engine's
// raw-doc read path. QueryFlat returns a truncated result whenever the requested
// span exceeds `truncateAbove`, simulating a high-cardinality metric where a wide
// slice exceeds adaptiveFlatMaxDocs (Hits.Total.Relation == "gte"). Each sample
// carries a fixed label set so grouping/dedup is deterministic.
type esTruncatingFlatReader struct {
	observabilitystorageext.MetricReader
	truncateAbove time.Duration
}

func (r *esTruncatingFlatReader) QueryFlat(ctx context.Context, query observabilitystorageext.MetricFlatQuery) (*observabilitystorageext.MetricFlatResult, error) {
	span := query.TimeRange.End.Sub(query.TimeRange.Start)
	truncated := span > r.truncateAbove

	// One sample per 15m of span, single label set → one series.
	step := 15 * time.Minute
	var samples []observabilitystorageext.MetricSample
	for t := query.TimeRange.Start; t.Before(query.TimeRange.End); t = t.Add(step) {
		samples = append(samples, observabilitystorageext.MetricSample{
			TimestampMs: t.UnixMilli(),
			Value:       float64(len(samples)),
			Labels:      map[string]string{"service_name": "svc"},
		})
	}

	total := int64(len(samples))
	if truncated {
		// Simulate ES reporting more docs matched than returned.
		total = int64(len(samples))*2 + 1
	}
	return &observabilitystorageext.MetricFlatResult{
		Samples:   samples,
		Total:     total,
		Truncated: truncated,
	}, nil
}

func newBisectQuerier(r observabilitystorageext.MetricReader) *esQuerier {
	q := &esQuerier{reader: r, logger: zap.NewNop()}
	q.qCache = &queryCache{}
	return q
}

// esDensityFlatReader implements both QueryFlat and QueryFlatDensity. The
// density probe reports a per-1m-bucket doc count so the "plan up front"
// path is exercised; QueryFlat then returns one sample per bucket for the
// requested slice.
type esDensityFlatReader struct {
	observabilitystorageext.MetricReader
	// docPerBucket is the doc count per 1m bucket (uniform density).
	docPerBucket int64
}

func (r *esDensityFlatReader) QueryFlatDensity(ctx context.Context, query observabilitystorageext.FlatDensityQuery) ([]observabilitystorageext.DensityBucket, error) {
	width := query.BucketWidthMs
	if width <= 0 {
		width = 60_000
	}
	var buckets []observabilitystorageext.DensityBucket
	for t := query.TimeRange.Start; t.Before(query.TimeRange.End); t = t.Add(time.Duration(width) * time.Millisecond) {
		buckets = append(buckets, observabilitystorageext.DensityBucket{StartMs: t.UnixMilli(), DocCount: r.docPerBucket})
	}
	return buckets, nil
}

func (r *esDensityFlatReader) QueryFlat(ctx context.Context, query observabilitystorageext.MetricFlatQuery) (*observabilitystorageext.MetricFlatResult, error) {
	// One sample per 1m bucket in the slice.
	var samples []observabilitystorageext.MetricSample
	for t := query.TimeRange.Start; t.Before(query.TimeRange.End); t = t.Add(time.Minute) {
		samples = append(samples, observabilitystorageext.MetricSample{
			TimestampMs: t.UnixMilli(),
			Value:       float64(len(samples)),
			Labels:      map[string]string{"service_name": "svc"},
		})
	}
	return &observabilitystorageext.MetricFlatResult{Samples: samples, Total: int64(len(samples)), Truncated: false}, nil
}

// TestBisectFlatSlice_DensityPlansUpFront verifies the "slice up front via
// density" path: with uniform density, the window is split into slices whose
// cumulative doc count stays under 10000, and each slice is fetched in parallel
// — no discarded truncated probes.
func TestBisectFlatSlice_DensityPlansUpFront(t *testing.T) {
	// 2000 docs/bucket × 60 buckets = 120000 docs over 1h → needs ~12 slices
	// of ~5 buckets each (5×2000=10000). This forces multiple planned slices.
	r := &esDensityFlatReader{docPerBucket: 2000}
	q := newBisectQuerier(r)
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(1 * time.Hour)

	res := q.bisectFlatSlice(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli())

	assert.False(t, res.truncated)
	// 60 one-minute buckets → 60 samples, all fetched (no drop).
	assert.Len(t, res.samples, 60)
}

// TestPlanFlatSlices_Bounds verifies the density→slice boundary math directly.
func TestPlanFlatSlices_Bounds(t *testing.T) {
	r := &esDensityFlatReader{docPerBucket: 3000}
	q := newBisectQuerier(r)
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(10 * time.Minute) // 10 buckets × 3000 = 30000 docs

	slices, ok := q.planFlatSlices(context.Background(), "m", nil, nil, "app", start.UnixMilli(), end.UnixMilli())
	require.True(t, ok)
	// 3000/bucket, cap 10000 → 3 buckets per slice (9000), so 10 buckets → 4 slices.
	assert.Len(t, slices, 4, "10 buckets × 3000 docs / 10000 cap → 4 slices")

	// First slice spans [start, start+3min), second [start+3min, start+6min), etc.
	assert.Equal(t, start.UnixMilli(), slices[0].start)
	assert.Equal(t, start.Add(3*time.Minute).UnixMilli(), slices[0].end)
	assert.Equal(t, end.UnixMilli(), slices[len(slices)-1].end)
}

// TestPlanFlatSlices_NoDensitySupport verifies fallback when the reader does
// not implement FlatDensityProber.
func TestPlanFlatSlices_NoDensitySupport(t *testing.T) {
	q := newBisectQuerier(&esTruncatingFlatReader{truncateAbove: time.Hour})
	_, ok := q.planFlatSlices(context.Background(), "m", nil, nil, "app", 0, 3600_000)
	assert.False(t, ok, "non-density reader must report not-ok to trigger fallback")
}

func TestBisectFlatSlice_NotTruncatedReturnsAsIs(t *testing.T) {
	q := newBisectQuerier(&esTruncatingFlatReader{truncateAbove: 2 * time.Hour})
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(1 * time.Hour) // below truncateAbove

	res := q.bisectFlatSlice(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli())

	assert.False(t, res.truncated)
	assert.Len(t, res.samples, 4) // 1h @ 15m granularity
	assert.Equal(t, int64(4), res.total)
}

func TestBisectFlatSlice_TruncatedBisectsAndMerges(t *testing.T) {
	q := newBisectQuerier(&esTruncatingFlatReader{truncateAbove: time.Hour})
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(4 * time.Hour) // 4h > 1h truncateAbove

	res := q.bisectFlatSlice(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli())

	// After bisection, every leaf ≤1h → not truncated.
	assert.False(t, res.truncated)
	// 4h @ 15m = 16 samples regardless of bisection rounds — no series dropped.
	assert.Len(t, res.samples, 16)
}

func TestBisectFlatSlice_TruncatedAtFloorKeepsFlag(t *testing.T) {
	q := newBisectQuerier(&esTruncatingFlatReader{truncateAbove: 0}) // always truncated
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(30 * time.Second) // below 1m floor

	res := q.bisectFlatSlice(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli())

	assert.True(t, res.truncated, "floor-clamped truncation must stay flagged")
}

// TestSelectConcreteSliced_EndToEnd verifies the full sliced path merges
// bisection results into deduplicated, time-sorted series with no dropped
// samples. A 4h window with truncateAbove=1h forces each 2h slice to bisect.
func TestSelectConcreteSliced_EndToEnd(t *testing.T) {
	q := newBisectQuerier(&esTruncatingFlatReader{truncateAbove: time.Hour})
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(4 * time.Hour)

	series, err := q.selectConcreteSliced(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli(), "cache-key", 2*time.Hour)

	require.NoError(t, err)
	require.Len(t, series, 1, "single label set → one series")

	s := series[0]
	assert.Len(t, s.Samples, 16, "4h @ 15m = 16 samples, none dropped")

	// Verify monotonic ascending timestamps (merge + dedup must not reorder).
	for i := 1; i < len(s.Samples); i++ {
		assert.True(t, s.Samples[i].TimestampMs > s.Samples[i-1].TimestampMs,
			"samples must be strictly ascending (no dup, no reorder)")
	}
}

// TestSelectConcreteSliced_DedupAcrossBisection verifies that when adjacent
// bisection leaves emit the same boundary timestamp, the merge dedups it.
func TestSelectConcreteSliced_DedupAcrossBisection(t *testing.T) {
	r := &esTruncatingFlatReader{truncateAbove: time.Hour}
	q := newBisectQuerier(r)
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(2 * time.Hour)

	series, err := q.selectConcreteSliced(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli(), "cache-key", 2*time.Hour)

	require.NoError(t, err)
	require.Len(t, series, 1)
	// 2h @ 15m = 8 distinct timestamps.
	assert.Len(t, series[0].Samples, 8)
}

// TestSelectConcrete_Sub2hTruncatedBisects verifies the unified ≤2h path:
// a window ≤2h that is truncated by MaxDocs must go through bisectFlatSlice
// (divide-on-truncation), not the old fixed-15m fallback. Here truncateAbove=1h
// and a 90m window forces bisection to 45m→…→1h leaves.
func TestSelectConcrete_Sub2hTruncatedBisects(t *testing.T) {
	r := &esTruncatingFlatReader{truncateAbove: time.Hour}
	q := newBisectQuerier(r)
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(90 * time.Minute) // ≤2h but >1h truncateAbove

	series, err := q.selectConcrete(context.Background(), "traces_spanmetrics_calls_total", nil, nil, "app", start.UnixMilli(), end.UnixMilli())

	require.NoError(t, err)
	require.Len(t, series, 1)
	// 90m @ 15m granularity = 6 samples, none dropped by truncation.
	assert.Len(t, series[0].Samples, 6)

	// Samples must be strictly ascending (merge reordered nothing).
	for i := 1; i < len(series[0].Samples); i++ {
		assert.True(t, series[0].Samples[i].TimestampMs > series[0].Samples[i-1].TimestampMs)
	}
}

