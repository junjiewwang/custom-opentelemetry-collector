// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"regexp"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// containsTemplateRe matches Loki's Go template pattern:
//   {{ contains "VALUE" __line__ }}
// Used by Grafana trace-to-logs to search log lines for a trace ID.
var containsTemplateRe = regexp.MustCompile(`\{\{\s*contains\s+"([^"]+)"\s+__line__\s*\}\}`)

// ExtractContainsValue extracts the search value from a label_format template
// like `{{ contains "TRACE_ID" __line__ }}`. Returns "" if the pattern doesn't match.
func ExtractContainsValue(template string) string {
	m := containsTemplateRe.FindStringSubmatch(template)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// Evaluator converts a parsed LogQL query into a storage-layer LogQuery.
type Evaluator struct{}

// Evaluate converts a LogQL query to a storage LogQuery.
func (e *Evaluator) Evaluate(lq *LogQLQuery) *observabilitystorageext.LogQuery {
	q := &observabilitystorageext.LogQuery{
		TimeRange: observabilitystorageext.TimeRange{
			Start: lq.Start,
			End:   lq.End,
		},
		Limit:     lq.Limit,
		Direction: lq.Direction,
	}

	// Stream selector → Labels + LabelMatch
	for _, m := range lq.StreamSelector.Matchers {
		switch m.Type {
		case MatchEqual:
			if q.Labels == nil {
				q.Labels = make(map[string]string)
			}
			q.Labels[m.Name] = m.Value
		case MatchNotEqual:
			if q.LabelNot == nil {
				q.LabelNot = make(map[string]string)
			}
			q.LabelNot[m.Name] = m.Value
		case MatchRegex:
			if q.LabelMatch == nil {
				q.LabelMatch = make(map[string]string)
			}
			q.LabelMatch[m.Name] = m.Value
		case MatchNotRegex:
			// Not regex: push to LabelMatch with a "!" prefix convention.
			if q.LabelNotMatch == nil {
				q.LabelNotMatch = make(map[string]string)
			}
			q.LabelNotMatch[m.Name] = m.Value
		}
	}

	// Line filters → query body search
	// Contains filters (|=, !=) go into the full-text Query field (match query on body).
	// Regex filters (|~, !~) go into separate RegexFilters / NotRegexFilters because
	// ES match queries apply the text analyzer to the query string, which tokenizes
	// PCRE patterns (e.g. "(?i)order" → tokens ["i", "order"]) and breaks matching.
	// Regex filters use ES regexp query instead.
	var lineQueries []string
	for _, f := range lq.LineFilters {
		// Empty pattern means "match everything" in Loki semantics
		if f.Pattern == "" {
			continue
		}
		switch f.Type {
		case FilterContains:
			pattern := escapeLokiPattern(f.Pattern)
			lineQueries = append(lineQueries, `"`+pattern+`"`)
		case FilterNotContains:
			pattern := escapeLokiPattern(f.Pattern)
			lineQueries = append(lineQueries, `-"`+pattern+`"`)
		case FilterRegex:
			q.RegexFilters = append(q.RegexFilters, f.Pattern)
		case FilterNotRegex:
			q.NotRegexFilters = append(q.NotRegexFilters, f.Pattern)
		}
	}
	if len(lineQueries) > 0 {
		q.Query = strings.Join(lineQueries, " ")
	}

	// Pipeline stages: translate label_format + contains template to line filter.
	// Grafana trace-to-logs generates: | label_format X=`{{ contains "ID" __line__ }}` | X="true"
	// We translate this to |= "ID" (ES match_phrase), which is semantically equivalent
	// and far more efficient than Go-side template evaluation. The subsequent
	// PipelineLabelFilter for the same label is skipped (the line filter already
	// ensures the condition).
	//
	// IMPORTANT: only translate when the SAME branch also has a PipelineLabelFilter
	// for that label name. In Grafana's OR-branched queries, the label_format stage
	// is in the shared prefix of both branches, but only the branch with X="true"
	// actually needs the body search. The other branch (e.g. trace_id="ID") must NOT
	// get the body match_phrase — it would incorrectly filter out logs that match by
	// trace_id label but don't contain the ID in their body text.
	labelFilterNames := map[string]bool{}
	for _, stage := range lq.Pipeline {
		if stage.Type == PipelineLabelFilter && stage.LabelFilter != nil {
			labelFilterNames[stage.LabelFilter.Name] = true
		}
	}
	var containsLabelNames map[string]bool
	for _, stage := range lq.Pipeline {
		if stage.Type == PipelineLabelFormat && stage.LabelFormat != nil {
			if value := ExtractContainsValue(stage.LabelFormat.Value); value != "" {
				// Only translate if this branch also filters on the label_format's label.
				// Otherwise the label_format is a shared-prefix artifact from OR splitting
				// and should not add a body constraint to this branch.
				if !labelFilterNames[stage.LabelFormat.Name] {
					continue
				}
				pattern := escapeLokiPattern(value)
				if q.Query == "" {
					q.Query = `"` + pattern + `"`
				} else {
					q.Query += ` "` + pattern + `"`
				}
				if containsLabelNames == nil {
					containsLabelNames = map[string]bool{}
				}
				containsLabelNames[stage.LabelFormat.Name] = true
			}
		}
	}

	return q
}

// escapeLokiPattern escapes special characters for simple_string query.
func escapeLokiPattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
