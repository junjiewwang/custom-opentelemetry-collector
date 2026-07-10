// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"math"
	"time"
)

const (
	// DefaultMaxBuckets is the safe upper bound for ES date_histogram buckets.
	// This is deliberately set below ES's default max_buckets (65535) to leave
	// headroom for nested aggregations that multiply the bucket count.
	DefaultMaxBuckets = 10000
)

// BucketParams holds the inputs needed to calculate a safe histogram interval.
type BucketParams struct {
	// Duration is the total time range to cover.
	Duration time.Duration
	// Step is the user-requested step interval (0 = auto-calculate).
	Step time.Duration
	// MaxBuckets is the upper bound on bucket count (0 = use DefaultMaxBuckets).
	MaxBuckets int
}

// SafeInterval returns a histogram interval string that guarantees bucket
// count <= maxBuckets. If the user's step would produce too many buckets,
// it clamps upward to the minimum safe interval.
//
// Returns:
//   - interval: ES fixed_interval string (e.g. "60s")
//   - clamped: true if the returned interval differs from the user's step
func SafeInterval(p BucketParams) (interval string, clamped bool) {
	maxBuckets := p.MaxBuckets
	if maxBuckets <= 0 {
		maxBuckets = DefaultMaxBuckets
	}

	if p.Duration <= 0 {
		return "1m", false
	}

	durationSec := p.Duration.Seconds()

	// If user specified a step, validate it against the safe minimum.
	if p.Step > 0 {
		stepSec := p.Step.Seconds()
		minSafeSec := durationSec / float64(maxBuckets)

		if stepSec >= minSafeSec {
			return durationToIntervalString(p.Step), false
		}
		// Clamp to ceil of minimum safe interval.
		// Always output in seconds to avoid precision loss from minute/hour
		// truncation (e.g. ceil(260s) → "4m" = 240s would be too small).
		clampedSec := int(math.Ceil(minSafeSec))
		if clampedSec < 1 {
			clampedSec = 1
		}
		return fmt.Sprintf("%ds", clampedSec), true
	}

	// Auto-calculate: use predefined tiers that are inherently safe
	// for DefaultMaxBuckets. These are tuned for ~250-400 buckets max
	// per tier, which is well within 10000.
	switch {
	case durationSec <= 3600: // ≤1h → 15s (max 240 buckets)
		return "15s", false
	case durationSec <= 21600: // ≤6h → 1m (max 360 buckets)
		return "1m", false
	case durationSec <= 86400: // ≤24h → 5m (max 288 buckets)
		return "5m", false
	case durationSec <= 604800: // ≤7d → 30m (max 336 buckets)
		return "30m", false
	default: // >7d → 1h
		hours := durationSec / 3600
		if hours > float64(maxBuckets) {
			// Even 1h interval exceeds limit, need to scale up.
			scaled := int(math.Ceil(hours / float64(maxBuckets)))
			return fmt.Sprintf("%dh", scaled), true
		}
		return "1h", false
	}
}

// EstimateBucketCount returns the approximate number of buckets a given
// duration and interval would produce.
func EstimateBucketCount(duration time.Duration, interval time.Duration) int {
	if interval <= 0 {
		return 0
	}
	return int(duration/interval) + 1
}

// durationToIntervalString converts a time.Duration to an ES fixed_interval
// string, using the most readable unit (s, m, h).
func durationToIntervalString(d time.Duration) string {
	sec := int(d.Seconds())
	if sec == 0 {
		return "1s"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh", sec/3600)
}
