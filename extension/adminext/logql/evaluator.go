// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"strings"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

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

	return q
}

// escapeLokiPattern escapes special characters for simple_string query.
func escapeLokiPattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
