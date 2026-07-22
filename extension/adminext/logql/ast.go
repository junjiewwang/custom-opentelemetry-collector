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

// PipelineStage is a single stage in the LogQL pipeline.
// Examples: | json, | logfmt, | json | level = "error"
type PipelineStage struct {
	Type PipelineType
	// Parser: "json", "logfmt", "unpack"
	Parser string
	// LabelFilter: pipeline-level filter (e.g., | json | level = "error")
	LabelFilter *LabelMatcher
	// LineFormat: template for line output (e.g., | line_format "{{.level}}: {{.msg}}")
	LineFormat string
	// LabelFormat: rename/modify labels
	LabelFormat *LabelMatcher
}

// PipelineType indicates the type of pipeline stage.
type PipelineType int

const (
	PipelineParser       PipelineType = iota // | json, | logfmt, | unpack
	PipelineLabelFilter                      // | json | level = "error"
	PipelineLineFormat                       // | line_format "..."
	PipelineLabelFormat                      // | label_format key=value
)

// LogQLQuery is a fully parsed LogQL query.
type LogQLQuery struct {
	StreamSelector StreamSelector
	LineFilters    []LineFilter
	Pipeline       []PipelineStage

	// Execution parameters from the HTTP request.
	Start    time.Time
	End      time.Time
	Step     time.Duration
	Limit    int
	Direction string // "forward" or "backward"
}
