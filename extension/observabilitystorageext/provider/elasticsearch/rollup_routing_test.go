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
