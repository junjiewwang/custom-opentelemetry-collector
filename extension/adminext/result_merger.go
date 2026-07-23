// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"sort"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// mergeLogResults merges results from multiple OR-branched log queries.
// De-duplicates by (timestamp, body), sorts by timestamp according to direction,
// and truncates to limit.
func mergeLogResults(branchResults []*observabilitystorageext.LogSearchResult, limit int, direction string) *observabilitystorageext.LogSearchResult {
	if len(branchResults) == 0 {
		return &observabilitystorageext.LogSearchResult{}
	}
	if len(branchResults) == 1 {
		return branchResults[0]
	}

	// 1. Collect all entries, dedup by (timestamp + body).
	seen := make(map[string]bool)
	var all []observabilitystorageext.LogRecord
	for _, r := range branchResults {
		if r == nil {
			continue
		}
		for _, rec := range r.Logs {
			key := rec.TimeUnixNano + "\x00" + rec.Body
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, rec)
		}
	}

	// 2. Sort by timestamp.
	sort.Slice(all, func(i, j int) bool {
		// Compare as strings (nanosecond timestamps — lexical order equals numeric order).
		if all[i].TimeUnixNano != all[j].TimeUnixNano {
			if direction == "forward" {
				return all[i].TimeUnixNano < all[j].TimeUnixNano
			}
			return all[i].TimeUnixNano > all[j].TimeUnixNano
		}
		// For same-timestamp entries, stable order (body comparison).
		return all[i].Body < all[j].Body
	})

	// 3. Truncate.
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	return &observabilitystorageext.LogSearchResult{
		Logs:  all,
		Total: int64(len(all)),
	}
}

// mergeMetricResults merges metric series from multiple OR-branched metric queries.
// Series with the same label set are merged by summing values at matching timestamps.
func mergeMetricResults(branchResults []*observabilitystorageext.LogMetricResult) *observabilitystorageext.LogMetricResult {
	if len(branchResults) == 0 {
		return &observabilitystorageext.LogMetricResult{}
	}
	if len(branchResults) == 1 {
		return branchResults[0]
	}

	// Group series by label signature (stable string key).
	type seriesKey struct {
		labels string
	}
	merged := make(map[string]*observabilitystorageext.LogMetricSeries)
	var order []string

	for _, r := range branchResults {
		if r == nil {
			continue
		}
		for _, s := range r.Series {
			key := labelsToKey(s.Labels)
			if existing, ok := merged[key]; ok {
				// Merge values: sum at matching timestamps.
				existing.Values = mergeMetricValues(existing.Values, s.Values)
			} else {
				cp := observabilitystorageext.LogMetricSeries{
					Labels: s.Labels,
					Values: make([]observabilitystorageext.LogMetricValue, len(s.Values)),
				}
				copy(cp.Values, s.Values)
				merged[key] = &cp
				order = append(order, key)
			}
		}
	}

	series := make([]observabilitystorageext.LogMetricSeries, 0, len(order))
	for _, k := range order {
		series = append(series, *merged[k])
	}

	return &observabilitystorageext.LogMetricResult{Series: series}
}

// labelsToKey creates a stable string key from a label map for series identification.
func labelsToKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, labels[k]...)
	}
	return string(b)
}

// mergeMetricValues merges two sorted metric value lists by summing at matching timestamps.
func mergeMetricValues(a, b []observabilitystorageext.LogMetricValue) []observabilitystorageext.LogMetricValue {
	if len(b) == 0 {
		return a
	}
	// Add new timestamps from b, sum existing ones.
	for _, bv := range b {
		found := false
		for i := range a {
			if a[i].TimestampNano == bv.TimestampNano {
				a[i].Value += bv.Value // sum values at same timestamp
				found = true
				break
			}
		}
		if !found {
			a = append(a, bv)
		}
	}
	// Re-sort by timestamp.
	sort.Slice(a, func(i, j int) bool {
		return a[i].TimestampNano < a[j].TimestampNano
	})
	return a
}
