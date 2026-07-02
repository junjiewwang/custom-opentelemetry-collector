// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import "time"

// DailyStorageRequest defines parameters for per-day storage usage queries.
type DailyStorageRequest struct {
	StartDate time.Time // start date (date part only)
	EndDate   time.Time // end date
	AppID     string    // optional: filter by appID
}

// DailyStoragePoint holds aggregated storage usage for a single calendar day.
type DailyStoragePoint struct {
	Date     string         `json:"date"`
	BySignal map[string]int64 `json:"bySignal"` // per-signal total (keys: "trace","metric","log")
	ByApp    map[string]int64 `json:"byApp"`    // per-app total
}

// DailyStorageResponse holds the result of a daily storage usage query.
type DailyStorageResponse struct {
	Points []DailyStoragePoint `json:"points"`
}
