// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import "time"

// StreamSelector is a parsed LogQL stream selector: {app="foo", env=~"prod|staging"}.
type StreamSelector struct {
	Matchers []LabelMatcher
}

// LineFilter is a parsed LogQL line filter: |= "error" or |~ "timeout|failed".
type LineFilter struct {
	Type    FilterType
	Pattern string
}

// FilterType indicates the type of line filter.
type FilterType int

const (
	FilterContains    FilterType = iota // |=  (contains substring)
	FilterNotContains                   // !=  (does not contain)
	FilterRegex                        // |~  (regex match)
	FilterNotRegex                     // !~  (regex not match)
)

// LabelMatcher is a parsed label matcher: name = "value" or name =~ "pattern".
type LabelMatcher struct {
	Name  string
	Type  MatchType
	Value string
}

// MatchType indicates the type of label match.
type MatchType int

const (
	MatchEqual    MatchType = iota // =
	MatchNotEqual                  // !=
	MatchRegex                     // =~
	MatchNotRegex                  // !~
)

// LogQLQuery is a fully parsed LogQL query.
// MVP scope: StreamSelector only. Future: LineFilters + Pipeline.
type LogQLQuery struct {
	StreamSelector StreamSelector
	LineFilters    []LineFilter

	// Execution parameters from the HTTP request.
	Start    time.Time
	End      time.Time
	Step     time.Duration
	Limit    int
	Direction string // "forward" or "backward"
}
