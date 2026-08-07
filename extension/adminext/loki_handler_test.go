// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseLokiTime_Units(t *testing.T) {
	// Nanoseconds (>= 1e18) -> interpreted as nanoseconds.
	tm, ok := parseLokiTime("1784707266594000000")
	assert.True(t, ok)
	assert.Equal(t, time.Unix(0, 1784707266594000000), tm)

	// Seconds (< 1e12) -> interpreted as seconds (Prometheus `time` convention).
	tm, ok = parseLokiTime("1784707266")
	assert.True(t, ok)
	assert.Equal(t, time.Unix(1784707266, 0), tm)

	// Milliseconds (>= 1e12, < 1e18) -> interpreted as milliseconds (Grafana JS timestamps).
	tm, ok = parseLokiTime("1784707266594")
	assert.True(t, ok)
	assert.Equal(t, time.UnixMilli(1784707266594), tm)

	// Fractional seconds -> seconds with nanoseconds.
	tm, ok = parseLokiTime("1784707266.594")
	assert.True(t, ok)
	assert.Equal(t, time.Unix(1784707266, 594_000_000), tm)

	// RFC3339 -> parsed directly.
	tm, ok = parseLokiTime("2026-07-23T02:50:19.343Z")
	assert.True(t, ok)
	assert.Equal(t, time.Date(2026, 7, 23, 2, 50, 19, 343_000_000, time.UTC), tm)

	// Empty / garbage -> not ok.
	_, ok = parseLokiTime("")
	assert.False(t, ok)
	_, ok = parseLokiTime("not-a-time")
	assert.False(t, ok)
}

func TestParseLokiTime_InstantTimeNotMistakenForNanos(t *testing.T) {
	// Regression: a 10-digit second timestamp must NOT be read as nanoseconds
	// (that previously yielded 1970 and an empty instant metric window).
	tm, ok := parseLokiTime("1786095900")
	assert.True(t, ok)
	assert.Equal(t, int64(1786095900), tm.Unix())
	assert.Equal(t, int64(0), tm.UnixNano()%1_000_000_000)
}

func TestParseLokiTime_ScientificWithDot(t *testing.T) {
	// Regression (Bug A): "1.78e9" is 1.78e9 seconds (~2026). The dot-branch
	// must NOT swallow the exponent and return 1970 — it must fall through to
	// the ParseFloat scientific-notation path.
	tm, ok := parseLokiTime("1.78e9")
	assert.True(t, ok)
	assert.Equal(t, time.Unix(1780000000, 0), tm)
}
