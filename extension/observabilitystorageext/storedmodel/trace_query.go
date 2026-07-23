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
	TagsOr        [][]map[string]string // OR groups: outer groups ANDed, inner maps ORed, map entries ANDed
	MinDuration   time.Duration
	MaxDuration   time.Duration
	TimeRange     TimeRange
	Limit         int
	Offset        int

	// ── Intrinsic filters (from TraceQL) ──
	SpanKind string // "client", "server", "internal", "producer", "consumer"
	Status   string // "ok", "error", "unset"
	IsRoot   bool   // true = filter for root spans only (parentSpanId = "")

	// ── Event filters (from TraceQL event:* scope) ──
	EventTags   []map[string]string     // AND conditions on span events (requires nested query)
	TagsNotOr   [][]map[string]string
	TagsRegexOr [][]map[string]string
	EventTagsOr [][][]map[string]string // OR groups of event conditions

	// ── Negation / Existence / Regex filters (Sprint 2) ──
	// TagsNot: != value conditions → ES must_not + term.
	TagsNot map[string]string
	// TagsExists: != nil conditions → ES exists query.
	TagsNotExists []string
	TagsExists []string
	// TagsRegex: =~ regex conditions → ES regexp query.
	TagsRegex map[string]string

	// ── Root span intrinsic filters (Sprint 3) ──
	// RootName filters by root span's name (e.g., trace:rootName = "GET /api").
	RootName string
	// RootService filters by root span's serviceName (e.g., trace:rootService = "gateway").
	RootService string
}

// TimeRange defines a time window for queries.
type TimeRange struct {
	Start time.Time
	End   time.Time
}
