// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"strings"
	"testing"
	"time"
)

func TestIndexPattern(t *testing.T) {
	assert := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	assert(IndexPattern("otel-traces", ""), "otel-traces-*")
	assert(IndexPattern("otel-traces", "app1"), "otel-traces-app1-*")
}

func TestIndexPatternForRange(t *testing.T) {
	// A 30-minute window on 2026-08-10 10:00–10:30 UTC must only touch that day
	// (plus the 1-day defensive pad on each side → 08.09, 08.10, 08.11).
	start := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 10, 10, 30, 0, 0, time.UTC)

	t.Run("scoped appID", func(t *testing.T) {
		got := IndexPatternForRange("otel-traces", "app1", start, end)
		// Should mention exactly three days, app-scoped.
		for _, d := range []string{"2026.08.09", "2026.08.10", "2026.08.11"} {
			want := "otel-traces-app1-" + d
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		if strings.Contains(got, "2026.08.08") || strings.Contains(got, "2026.08.12") {
			t.Errorf("over-narrowed or extra day in %q", got)
		}
	})

	t.Run("global appID emits per-day wildcards", func(t *testing.T) {
		got := IndexPatternForRange("otel-traces", "", start, end)
		// Each day becomes otel-traces-*-<date> (covers all appIDs for that day).
		for _, d := range []string{"2026.08.09", "2026.08.10", "2026.08.11"} {
			want := "otel-traces-*-" + d
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("multi-day window across midnight", func(t *testing.T) {
		s := time.Date(2026, 8, 9, 23, 30, 0, 0, time.UTC)
		e := time.Date(2026, 8, 10, 0, 30, 0, 0, time.UTC)
		got := IndexPatternForRange("otel-metrics", "app1", s, e)
		// Pad makes it 08.08..08.11.
		for _, d := range []string{"2026.08.08", "2026.08.09", "2026.08.10", "2026.08.11"} {
			if !strings.Contains(got, "otel-metrics-app1-"+d) {
				t.Errorf("missing %q in %q", d, got)
			}
		}
	})

	t.Run("zero time falls back to full wildcard", func(t *testing.T) {
		got := IndexPatternForRange("otel-traces", "app1", time.Time{}, end)
		if got != "otel-traces-app1-*" {
			t.Errorf("fallback got %q, want otel-traces-app1-*", got)
		}
		got2 := IndexPatternForRange("otel-traces", "", start, time.Time{})
		if got2 != "otel-traces-*" {
			t.Errorf("fallback got %q, want otel-traces-*", got2)
		}
	})

	t.Run("non-UTC input normalized to UTC", func(t *testing.T) {
		// 2026-08-10 01:00 in +08:00 = 2026-08-09 17:00 UTC → pad covers 08.08..08.10.
		loc, _ := time.LoadLocation("Asia/Shanghai")
		s := time.Date(2026, 8, 10, 1, 0, 0, 0, loc)
		e := time.Date(2026, 8, 10, 1, 30, 0, 0, loc)
		got := IndexPatternForRange("otel-traces", "app1", s, e)
		if !strings.Contains(got, "otel-traces-app1-2026.08.09") {
			t.Errorf("expected UTC-normalized day 08.09 in %q", got)
		}
	})
}
