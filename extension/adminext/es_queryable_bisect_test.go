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
