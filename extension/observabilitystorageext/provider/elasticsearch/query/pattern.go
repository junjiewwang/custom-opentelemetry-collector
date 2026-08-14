// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"strings"
	"time"
)

// IndexPattern returns the ES index pattern for the given prefix and appID.
// When appID is non-empty, returns an app-scoped pattern; otherwise a global wildcard.
//
//	prefix="otel-traces", appID=""     → "otel-traces-*"
//	prefix="otel-traces", appID="app1" → "otel-traces-app1-*"
func IndexPattern(prefix, appID string) string {
	if appID != "" {
		return prefix + "-" + appID + "-*"
	}
	return prefix + "-*"
}

// IndexPatternForRange returns a comma-separated list of index patterns
// restricted to the daily partitions that overlap [start,end].
//
// Indices are named "{prefix}-{appID}-{YYYY.MM.DD}" (see metric_writer.go
// getIndexName). A query whose time window is 30 minutes into "today" would
// otherwise match "{prefix}-{appID}-*" and scan EVERY historical day (7+ days
// × every shard), overflowing the ES heap. This narrows it to just the days
// the window touches.
//
// When appID is empty, each day is emitted as a per-day wildcard
// ("{prefix}-*-{date}") so all appIDs for that day are covered. start/end are
// interpreted in UTC to match the index date suffix (writer uses UTC).
//
// If start or end is zero (unbounded time range), falls back to the full
// wildcard IndexPattern so callers don't accidentally over-narrow.
func IndexPatternForRange(prefix, appID string, start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return IndexPattern(prefix, appID)
	}
	// Normalize to UTC, snap to day boundaries. A window ending at 23:59 on day
	// D and starting 00:01 on day D still only touches day D; a window crossing
	// midnight touches both. Add a 1-day pad on each side defensively in case
	// the writer's UTC bucketing is slightly off from the stored timestamp.
	//
	// The end pad must NOT extend past "today": an appID-scoped pattern emits an
	// exact index name per day ({prefix}-{appID}-{date}), and a future date has
	// no index yet — ES returns index_not_found (404) for an explicitly-named
	// missing index, failing the whole query. The wildcard form ({prefix}-*-{date})
	// is immune (matches nothing → empty), so this only bit appID-scoped queries.
	sDay := start.UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	eDay := end.UTC().Add(24 * time.Hour).Truncate(24 * time.Hour)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if eDay.After(today) {
		eDay = today
	}

	var patterns []string
	for d := sDay; !d.After(eDay); d = d.Add(24 * time.Hour) {
		dateStr := d.Format("2006.01.02")
		if appID != "" {
			patterns = append(patterns, fmt.Sprintf("%s-%s-%s", prefix, appID, dateStr))
		} else {
			patterns = append(patterns, fmt.Sprintf("%s-*-%s", prefix, dateStr))
		}
	}
	if len(patterns) == 0 {
		return IndexPattern(prefix, appID)
	}
	return strings.Join(patterns, ",")
}
