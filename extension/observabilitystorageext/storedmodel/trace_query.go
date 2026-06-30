// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import "time"

// TraceQuery is the unified query type for searching traces.
// All providers (ES, PG, future) use this single definition.
type TraceQuery struct {
	AppID         string
	ServiceName   string
	OperationName string
	Tags          map[string]string
	MinDuration   time.Duration
	MaxDuration   time.Duration
	TimeRange     TimeRange
	Limit         int
	Offset        int
}

// TimeRange defines a time window for queries.
type TimeRange struct {
	Start time.Time
	End   time.Time
}
