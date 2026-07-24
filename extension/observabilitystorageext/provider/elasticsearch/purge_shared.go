// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

// buildDeleteByQuery builds the ES query body for a time-range delete_by_query,
// scoped to appID when non-empty. tsField is the ES timestamp field; bound is
// the pre-serialized comparison value (an int64 for the numeric timestamp
// fields used by this package).
//
// Shared by Admin.Purge / Admin.PurgeByApp, Purger.deleteByQuery /
// deleteByQueryForApp, and Provider.Purge / Provider.PurgeByApp so the query
// construction — including app scoping — lives in one place. The per-signal
// timestamp field and bound are resolved via signalTimestampField /
// signalTimestampBound (see signal_spec.go).
func buildDeleteByQuery(tsField string, bound any, appID string) map[string]any {
	rangeClause := map[string]any{
		"range": map[string]any{
			tsField: map[string]any{"lt": bound},
		},
	}
	if appID == "" {
		return rangeClause
	}
	return map[string]any{
		"bool": map[string]any{
			"must": []map[string]any{
				rangeClause,
				{"term": map[string]any{FieldAppID: appID}},
			},
		},
	}
}
