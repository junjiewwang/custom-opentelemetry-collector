// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ── routeIndexPattern routing coherence ────────────────────────────────

func newRouteReader(rollupEnabled bool, readyAfter time.Duration) *MetricReader {
	return &MetricReader{
		config: &Config{
			Metrics:          IndexConfig{IndexPrefix: "otel-metrics"},
			RollupEnabled:    rollupEnabled,
			RollupReadyAfter: readyAfter,
		},
	}
}

func TestRouteIndexPattern_ShortWindowAlwaysRaw(t *testing.T) {
	r := newRouteReader(true, 24*time.Hour)
	now := time.Now()
	// 1h window ending now — always raw (≤2h threshold).
	pat := r.routeIndexPattern("app", now.Add(-time.Hour), now)
	assert.Equal(t, "otel-metrics-", pat[:len("otel-metrics-")], "must route to raw tier")
	assert.NotContains(t, pat, "rollup", "≤2h window must not use rollup")
}

func TestRouteIndexPattern_RecentLongWindowFallsBackToRaw(t *testing.T) {
	r := newRouteReader(true, 24*time.Hour)
	now := time.Now()
	// 6h window ending now (today) — rollup tier has no today data, so raw.
	pat := r.routeIndexPattern("app", now.Add(-6*time.Hour), now)
	assert.NotContains(t, pat, "rollup", "window ending within ReadyAfter must fall back to raw")
	assert.Contains(t, pat, "otel-metrics-", "must route to raw tier")
}

func TestRouteIndexPattern_StabilizedLongWindowUsesRollup(t *testing.T) {
	r := newRouteReader(true, 24*time.Hour)
	now := time.Now()
	// 6h window entirely 25h in the past — stabilized, rollup data exists.
	end := now.Add(-25 * time.Hour)
	pat := r.routeIndexPattern("app", end.Add(-6*time.Hour), end)
	assert.Contains(t, pat, "rollup-5m", "stabilized long window must use rollup tier")
}

func TestRouteIndexPattern_RollupDisabledAlwaysRaw(t *testing.T) {
	r := newRouteReader(false, 24*time.Hour)
	now := time.Now()
	end := now.Add(-25 * time.Hour)
	pat := r.routeIndexPattern("app", end.Add(-6*time.Hour), end)
	assert.NotContains(t, pat, "rollup", "RollupEnabled=false must always use raw")
}

func TestRouteIndexPattern_ZeroReadyAfterDefaultsTo24h(t *testing.T) {
	r := newRouteReader(true, 0)
	now := time.Now()
	// 6h window ending now — with default 24h ReadyAfter, must fall back to raw.
	pat := r.routeIndexPattern("app", now.Add(-6*time.Hour), now)
	assert.NotContains(t, pat, "rollup", "default ReadyAfter=24h must route recent window to raw")
}

// ── rollupWorkItem helpers ─────────────────────────────────────────────

func TestRollupWorkItem_SliceBounds(t *testing.T) {
	// date is the DAY (midnight UTC), hour is the offset within it.
	date := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	item := &rollupWorkItem{appID: "app", date: date, hour: 3}

	assert.Equal(t, "2026.08.13:03", item.sliceKey())
	// Start = 03:00, end = 04:00 (next hour boundary).
	assert.Equal(t, time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC).UnixMilli(), item.sliceStartMs())
	assert.Equal(t, time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC).UnixMilli(), item.sliceEndMs())
}

// ── watermark contiguity ───────────────────────────────────────────────

func TestWatermarkContiguous(t *testing.T) {
	h0 := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).UnixMilli() // 00:00
	h1 := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC).UnixMilli() // 01:00
	h2 := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC).UnixMilli() // 02:00
	h3 := time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC).UnixMilli() // 03:00

	// First hour ever (current==0) is always contiguous.
	assert.True(t, watermarkContiguous(0, h0))

	// Watermark = END of hour 0 (== start of hour 1). Hour 1 starts at h1:
	// current == hourStartMs, contiguous.
	assert.True(t, watermarkContiguous(h1, h1))

	// Gap: watermark = end of hour 0 (h1), but the hour starts at h2 (hour 1 skipped).
	assert.False(t, watermarkContiguous(h1, h2))

	// Watermark ahead of the hour (would go backwards): watermark = h3, hour = h1.
	assert.False(t, watermarkContiguous(h3, h1))
}

// ── routeTierDecision ──────────────────────────────────────────────────

func TestRouteTierDecision(t *testing.T) {
	r := newRouteReader(true, 24*time.Hour)
	now := time.Now()
	split := now.Add(-24 * time.Hour)

	t.Run("fully recent → raw", func(t *testing.T) {
		d := r.routeTierDecision(now.Add(-6*time.Hour), now)
		assert.Equal(t, "raw", d.tier)
	})

	t.Run("fully stabilized → rollup", func(t *testing.T) {
		d := r.routeTierDecision(now.Add(-48*time.Hour), now.Add(-25*time.Hour))
		assert.Equal(t, "rollup", d.tier)
	})

	t.Run("crosses boundary → mixed", func(t *testing.T) {
		d := r.routeTierDecision(now.Add(-7*24*time.Hour), now)
		assert.Equal(t, "mixed", d.tier)
		assert.WithinDuration(t, split, d.splitPoint, time.Second, "splitPoint should be now-24h")
	})

	t.Run("rollup disabled → raw always", func(t *testing.T) {
		disabled := newRouteReader(false, 24*time.Hour)
		d := disabled.routeTierDecision(now.Add(-48*time.Hour), now.Add(-25*time.Hour))
		assert.Equal(t, "raw", d.tier)
	})
}

// ── mergeRangeResults ──────────────────────────────────────────────────

func TestMergeRangeResults(t *testing.T) {
	ts := func(v float64) time.Time { return time.UnixMilli(int64(v) * 1000) }
	dp := func(v float64) MetricDataPoint { return MetricDataPoint{Time: ts(v), Value: v} }

	// a = older rollup portion (labels {a:1}, {b:2}), b = newer raw portion.
	a := &MetricRangeResult{Data: []MetricSeries{
		{Labels: map[string]string{"k": "1"}, Values: []MetricDataPoint{dp(100), dp(110)}},
		{Labels: map[string]string{"k": "2"}, Values: []MetricDataPoint{dp(200)}},
	}}
	b := &MetricRangeResult{Data: []MetricSeries{
		{Labels: map[string]string{"k": "1"}, Values: []MetricDataPoint{dp(120), dp(130)}},
		{Labels: map[string]string{"k": "3"}, Values: []MetricDataPoint{dp(300)}},
	}}

	merged := mergeRangeResults(a, b)

	assert.Len(t, merged.Data, 3, "3 distinct label sets across both")

	// Find each series by label.
	byKey := map[string]*MetricSeries{}
	for i := range merged.Data {
		byKey[merged.Data[i].Labels["k"]] = &merged.Data[i]
	}

	// k=1 appears in both → values concatenated (a then b, time-ascending).
	assert.Equal(t, 4, len(byKey["1"].Values), "k=1 merged values")
	assert.Equal(t, float64(100), byKey["1"].Values[0].Value)
	assert.Equal(t, float64(130), byKey["1"].Values[3].Value)

	// k=2 only in a, k=3 only in b.
	assert.Equal(t, 1, len(byKey["2"].Values))
	assert.Equal(t, 1, len(byKey["3"].Values))
}

func TestMergeRangeResults_NilInputs(t *testing.T) {
	merged := mergeRangeResults(nil, &MetricRangeResult{Data: []MetricSeries{
		{Labels: map[string]string{"k": "1"}, Values: []MetricDataPoint{{Value: 1}}},
	}})
	assert.Len(t, merged.Data, 1)
	assert.Equal(t, float64(1), merged.Data[0].Values[0].Value)
}
