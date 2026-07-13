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
	TagsOr        []map[string]string // OR groups: each map is ANDed internally, groups are ORed together
	MinDuration   time.Duration
	MaxDuration   time.Duration
	TimeRange     TimeRange
	Limit         int
	Offset        int

	// ── Intrinsic filters (from TraceQL) ──
	SpanKind string // "client", "server", "internal", "producer", "consumer"
	Status   string // "ok", "error", "unset"
	IsRoot   bool   // true = filter for root spans only (parentSpanId = "")
}

// TimeRange defines a time window for queries.
type TimeRange struct {
	Start time.Time
	End   time.Time
}
