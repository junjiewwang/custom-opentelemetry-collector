// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"testing"
	"time"
)

func TestSafeInterval_UserStep(t *testing.T) {
	tests := []struct {
		name         string
		duration     time.Duration
		step         time.Duration
		maxBuckets   int
		wantInterval string
		wantClamped  bool
	}{
		{
			name:         "step within limit, no clamp needed",
			duration:     100 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   0, // use DefaultMaxBuckets
			wantInterval: "1s",
			wantClamped:  false,
		},
		{
			name:         "step exactly at boundary, no clamp needed",
			duration:     10000 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   10000,
			wantInterval: "1s",
			wantClamped:  false,
		},
		{
			name:         "step too small for duration, should clamp",
			duration:     65536 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   0,
			wantInterval: "7s", // ceil(65536/10000) = ceil(6.5536) = 7
			wantClamped:  true,
		},
		{
			name:         "step=15s, large duration (30d), should clamp",
			duration:     30 * 24 * time.Hour, // 2592000 seconds
			step:         15 * time.Second,
			maxBuckets:   0,
			wantInterval: "260s", // ceil(2592000/10000) = ceil(259.2) = 260
			wantClamped:  true,
		},
		{
			name:         "step exactly safe, no clamp",
			duration:     1000 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   0,
			wantInterval: "1s",
			wantClamped:  false,
		},
		{
			name:         "custom maxBuckets=100, should clamp to ceil(dur/max)",
			duration:     1000 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   100,
			wantInterval: "10s", // ceil(1000/100) = ceil(10.0) = 10
			wantClamped:  true,
		},
		{
			name:         "custom maxBuckets=100 non-exact division",
			duration:     1001 * time.Second,
			step:         1 * time.Second,
			maxBuckets:   100,
			wantInterval: "11s", // ceil(1001/100) = ceil(10.01) = 11
			wantClamped:  true,
		},
		{
			name:         "step=30m, 7d range with default maxBuckets, no clamp",
			duration:     7 * 24 * time.Hour,
			step:         30 * time.Minute,
			maxBuckets:   0,
			wantInterval: "30m",
			wantClamped:  false,
		},
		{
			name:         "step=1m, 24h range, safe",
			duration:     24 * time.Hour,
			step:         1 * time.Minute,
			maxBuckets:   0,
			wantInterval: "1m",
			wantClamped:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval, clamped := SafeInterval(BucketParams{
				Duration:   tt.duration,
				Step:       tt.step,
				MaxBuckets: tt.maxBuckets,
			})

			if interval != tt.wantInterval {
				t.Errorf("interval = %q, want %q", interval, tt.wantInterval)
			}
			if clamped != tt.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, tt.wantClamped)
			}
		})
	}
}

func TestSafeInterval_AutoCalculate(t *testing.T) {
	tests := []struct {
		name         string
		duration     time.Duration
		maxBuckets   int
		wantInterval string
		wantClamped  bool
	}{
		{
			name:         "zero duration returns default",
			duration:     0,
			wantInterval: "1m",
			wantClamped:  false,
		},
		{
			name:         "≤1h → 15s",
			duration:     1 * time.Hour,
			wantInterval: "15s",
			wantClamped:  false,
		},
		{
			name:         "≤6h → 1m",
			duration:     6 * time.Hour,
			wantInterval: "1m",
			wantClamped:  false,
		},
		{
			name:         "≤24h → 5m",
			duration:     24 * time.Hour,
			wantInterval: "5m",
			wantClamped:  false,
		},
		{
			name:         "≤7d → 30m",
			duration:     7 * 24 * time.Hour,
			wantInterval: "30m",
			wantClamped:  false,
		},
		{
			name:         ">7d → 1h (within limit)",
			duration:     30 * 24 * time.Hour,
			wantInterval: "1h",
			wantClamped:  false,
		},
		{
			name:         "huge duration exceeds maxBuckets*1h, should scale up",
			duration:     500 * 24 * time.Hour, // 500 days = 12000 hours > 10000
			maxBuckets:   0,
			wantInterval: "2h", // ceil(12000/10000) = ceil(1.2) = 2
			wantClamped:  true,
		},
		{
			name:         "even larger with custom small maxBuckets",
			duration:     365 * 24 * time.Hour, // 365 days = 8760 hours
			maxBuckets:   100,
			wantInterval: "88h", // ceil(8760/100) = ceil(87.6) = 88
			wantClamped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval, clamped := SafeInterval(BucketParams{
				Duration:   tt.duration,
				Step:       0, // auto-calculate
				MaxBuckets: tt.maxBuckets,
			})

			if interval != tt.wantInterval {
				t.Errorf("interval = %q, want %q", interval, tt.wantInterval)
			}
			if clamped != tt.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, tt.wantClamped)
			}
		})
	}
}

func TestSafeInterval_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		params       BucketParams
		wantInterval string
		wantClamped  bool
	}{
		{
			name: "negative duration treated as zero",
			params: BucketParams{
				Duration: -1 * time.Hour,
				Step:     5 * time.Second,
			},
			wantInterval: "1m",
			wantClamped:  false,
		},
		{
			name: "step=30m, exactly 30m→safe for 7d range",
			params: BucketParams{
				Duration:   7 * 24 * time.Hour,
				Step:       30 * time.Minute,
				MaxBuckets: 0,
			},
			wantInterval: "30m",
			wantClamped:  false,
		},
		{
			name: "sub-second step rounds to 1s (not clamped, just string conversion)",
			params: BucketParams{
				Duration: 1 * time.Hour,
				Step:     500 * time.Millisecond,
			},
			wantInterval: "1s",
			wantClamped:  false, // step >= minSafeSec, only string representation differs
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval, clamped := SafeInterval(tt.params)
			if interval != tt.wantInterval {
				t.Errorf("interval = %q, want %q", interval, tt.wantInterval)
			}
			if clamped != tt.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, tt.wantClamped)
			}
		})
	}
}

// TestSafeInterval_FlatCap_LongRangeNotClamped verifies Phase 1.1: a non-grouped
// (flat) range query uses DefaultMaxBucketsFlat (65535), so a 7d @ 10s step
// (60480 buckets) is returned at full resolution instead of being clamped to
// ~10000 points. Under the old DefaultMaxBuckets (10000) this would clamp.
func TestSafeInterval_FlatCap_LongRangeNotClamped(t *testing.T) {
	tests := []struct {
		name         string
		duration     time.Duration
		step         time.Duration
		maxBuckets   int
		wantInterval string
		wantClamped  bool
	}{
		{
			name:         "7d @ 10s under flat cap (60480 < 65535) — no clamp",
			duration:     7 * 24 * time.Hour,
			step:         10 * time.Second,
			maxBuckets:   DefaultMaxBucketsFlat,
			wantInterval: "10s",
			wantClamped:  false,
		},
		{
			name:         "7d @ 10s under OLD cap (60480 > 10000) — would clamp",
			duration:     7 * 24 * time.Hour,
			step:         10 * time.Second,
			maxBuckets:   DefaultMaxBuckets,
			wantInterval: "61s", // ceil(604800/10000) = 61
			wantClamped:  true,
		},
		{
			name:         "30d @ 1m under flat cap (43200 < 65535) — no clamp",
			duration:     30 * 24 * time.Hour,
			step:         1 * time.Minute,
			maxBuckets:   DefaultMaxBucketsFlat,
			wantInterval: "1m",
			wantClamped:  false,
		},
		{
			name:         "1y @ 1h under flat cap (8760 < 65535) — no clamp",
			duration:     365 * 24 * time.Hour,
			step:         1 * time.Hour,
			maxBuckets:   DefaultMaxBucketsFlat,
			wantInterval: "1h",
			wantClamped:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval, clamped := SafeInterval(BucketParams{
				Duration:   tt.duration,
				Step:       tt.step,
				MaxBuckets: tt.maxBuckets,
			})
			if interval != tt.wantInterval {
				t.Errorf("interval = %q, want %q", interval, tt.wantInterval)
			}
			if clamped != tt.wantClamped {
				t.Errorf("clamped = %v, want %v", clamped, tt.wantClamped)
			}
		})
	}
}

// TestSafeInterval_GroupedCap_AvoidsTooManyBuckets verifies Phase 1.1: a grouped
// query caps the time axis at ESHardMaxBuckets/seriesLimit so the per-shard
// bucket count (seriesLimit × time_buckets) never exceeds ES's 65535 limit,
// turning a too_many_buckets ERROR into a (visible) clamp.
func TestSafeInterval_GroupedCap_AvoidsTooManyBuckets(t *testing.T) {
	seriesLimit := 100
	cap := ESHardMaxBuckets / seriesLimit // 655
	// 7d @ 10s = 60480 time buckets > 655 → must clamp (not error).
	interval, clamped := SafeInterval(BucketParams{
		Duration:   7 * 24 * time.Hour,
		Step:        10 * time.Second,
		MaxBuckets: cap,
	})
	if !clamped {
		t.Fatalf("expected clamped=true for 7d@10s under grouped cap %d, got interval=%q", cap, interval)
	}
	// Resulting time buckets must fit under seriesLimit×cap ≤ ESHardMaxBuckets.
	parsed, err := time.ParseDuration(interval)
	if err != nil {
		t.Fatalf("unparseable interval %q: %v", interval, err)
	}
	timeBuckets := EstimateBucketCount(7*24*time.Hour, parsed)
	if total := seriesLimit * timeBuckets; total > ESHardMaxBuckets {
		t.Errorf("grouped total buckets %d×%d = %d exceeds ES limit %d",
			seriesLimit, timeBuckets, total, ESHardMaxBuckets)
	}
}

func TestEstimateBucketCount(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		interval time.Duration
		want     int
	}{
		{
			name:     "zero interval",
			duration: 1 * time.Hour,
			interval: 0,
			want:     0,
		},
		{
			name:     "exactly divides",
			duration: 100 * time.Second,
			interval: 10 * time.Second,
			want:     11, // 100/10 + 1 = 11
		},
		{
			name:     "not exactly dividing",
			duration: 100 * time.Second,
			interval: 30 * time.Second,
			want:     4, // 100/30 + 1 = 4
		},
		{
			name:     "duration less than interval",
			duration: 5 * time.Second,
			interval: 10 * time.Second,
			want:     1, // 5/10 + 1 = 1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateBucketCount(tt.duration, tt.interval)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSafeInterval_AllBucketsStayWithinLimit verifies that for realistic
// scenarios (default maxBuckets=10000), the returned interval never
// produces more buckets than the configured limit.
func TestSafeInterval_AllBucketsStayWithinLimit(t *testing.T) {
	// Property test with DefaultMaxBuckets — the auto-calculate tiers
	// are designed for this value.
	const limit = DefaultMaxBuckets

	durations := []time.Duration{
		1 * time.Minute,
		1 * time.Hour,
		6 * time.Hour,
		24 * time.Hour,
		7 * 24 * time.Hour,
		30 * 24 * time.Hour,
	}
	steps := []time.Duration{0, 1 * time.Second, 15 * time.Second, 1 * time.Minute, 5 * time.Minute}

	for _, dur := range durations {
		for _, step := range steps {
			interval, _ := SafeInterval(BucketParams{
				Duration:   dur,
				Step:       step,
				MaxBuckets: limit,
			})

			parsed, err := time.ParseDuration(interval)
			if err != nil {
				t.Errorf("could not parse interval %q: %v", interval, err)
				continue
			}

			count := EstimateBucketCount(dur, parsed)
			if count > limit {
				t.Errorf("duration=%s step=%s interval=%q → %d buckets (> %d)",
					dur, step, interval, count, limit)
			}
		}
	}
}
