// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"regexp"
	"sort"
	"strings"
)

// seriesKey returns a stable, dedup-key string for a Prometheus series label set.
func seriesKey(m promMetric) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(',')
	}
	return b.String()
}

// planSeriesMatch decodes a single /api/v1/series match[] selector into the
// metric name(s) to query and the remaining (non-__name__) label matchers.
//
// The storage layer keys metrics by name (a term query on the name field), not
// by a "__name__" label, so __name__ must be routed to MetricName rather than
// passed as a label. This mirrors the /labels handler:
//   - bare name form "metric{...}"          → names=["metric"]
//   - __name__="x"                          → names=["x"]
//   - __name__=~"x" / ".*x.*"               → names=["x"]   (literal extraction)
//   - __name__=~"a|b|c"                     → names=["a","b","c"]
//   - __name__=~".*" / no __name__ matcher  → matchAllNames=true (all metrics)
//
// Real wildcard regexes (e.g. __name__=~"pool.*") reduce lossily to their
// literal portion ("pool"), consistent with /labels; this is a shared limitation
// from the storage layer not supporting metric-name regex.
//
// Other label matchers (exact, regex, and their negations) are returned
// verbatim for the storage layer. __name__ is never included in the label maps.
func planSeriesMatch(matchStr string) (names []string, labels, labelMatch map[string]string, matchAllNames bool, err error) {
	plan, perr := planSeriesMatchFull(matchStr)
	if perr != nil {
		return nil, nil, nil, false, perr
	}
	return plan.Names, plan.Labels, plan.LabelMatch, plan.MatchAllNames, nil
}

// seriesMatchPlan is the decoded form of a single match[] selector.
type seriesMatchPlan struct {
	Names         []string
	MatchAllNames bool
	Labels        map[string]string // =
	LabelMatch    map[string]string // =~
	LabelNot      map[string]string // !=
	LabelNotMatch map[string]string // !~
}

// planSeriesMatchFull is planSeriesMatch including the negated matchers.
// Kept separate so the existing 5-value call sites stay readable.
func planSeriesMatchFull(matchStr string) (*seriesMatchPlan, error) {
	expr, perr := parsePromQLSelector(matchStr)
	if perr != nil {
		return nil, perr
	}
	if expr == nil {
		return nil, errInvalidPromQL("empty selector")
	}

	// __name__ is routed to MetricName, never passed through as a label.
	stripName := func(in map[string]string) map[string]string {
		out := make(map[string]string, len(in))
		for k, v := range in {
			if k == PromLabelName {
				continue
			}
			out[k] = v
		}
		return out
	}

	plan := &seriesMatchPlan{
		Labels:        stripName(expr.Labels),
		LabelMatch:    stripName(expr.LabelMatch),
		LabelNot:      stripName(expr.LabelNot),
		LabelNotMatch: stripName(expr.LabelNotMatch),
	}

	// Bare metric name before the brace takes precedence.
	if expr.MetricName != "" {
		plan.Names = []string{expr.MetricName}
		return plan, nil
	}
	// Regex __name__=~"r": reduce to literal name(s) via the /labels helpers.
	// (The exact __name__="x" form needs no branch here: parsePromQLSelector
	// already routes it to MetricName, handled above.)
	if _, ok := expr.LabelMatch[PromLabelName]; ok {
		if single := extractMetricNameFromMatch([]string{matchStr}); single != "" {
			plan.Names = []string{single}
			return plan, nil
		}
		if many := extractMetricNamesFromMatch([]string{matchStr}); len(many) > 0 {
			plan.Names = many
			return plan, nil
		}
		// Regex carried no literal name (e.g. ".*") → match all names.
		plan.MatchAllNames = true
		return plan, nil
	}
	// No __name__ constraint at all → query across all metrics.
	plan.MatchAllNames = true
	return plan, nil
}

// anchorPromRegex wraps a Prometheus regex pattern so it matches the entire
// string, replicating Prometheus's fully-anchored =~ semantics. Go's regexp
// is unanchored by default; Prometheus anchors both ends.
func anchorPromRegex(pattern string) string {
	return "^(?:" + pattern + ")$"
}

// filterMetricNamesByMatch filters a list of metric names against the __name__
// constraints carried in Prometheus match[] selectors, used by the
// /api/v1/label/__name__/values endpoint.
//
// Semantics:
//   - match[] selectors are OR'd: a name is kept if it satisfies ANY selector.
//   - Within a selector, only the __name__ matcher is applied here:
//   - __name__="x"  → exact equality.
//   - __name__=~"r" → Prometheus regex (fully anchored).
//   - A selector with no __name__ matcher matches every name, so the union is
//     the full input set (returned as-is).
//   - Malformed selectors and selectors with an uncompilable regex are skipped
//     (they never contribute matches); this keeps a single bad selector from
//     breaking the whole request.
//
// Non-__name__ label constraints inside a selector (e.g. {job="x",__name__=~"p"})
// are intentionally NOT applied: the storage layer returns metric names
// independent of other labels. This is a documented limitation; the result is a
// superset of the strictly-correct set, which is safe for a name-listing endpoint.
//
// The function is pure and does not mutate its input. When matches is empty the
// input slice is returned unchanged.
func filterMetricNamesByMatch(names []string, matches []string) []string {
	if len(matches) == 0 || len(names) == 0 {
		return names
	}

	type nameMatcher struct {
		exact string // __name__="x"
		re    *regexp.Regexp
	}
	var matchers []nameMatcher
	for _, m := range matches {
		expr, err := parsePromQLSelector(m)
		if err != nil || expr == nil {
			continue // skip malformed selector
		}
		// parsePromQLSelector routes both the bare form ("metric{...}") and the
		// __name__="x" form to MetricName, so read the exact name from there.
		if expr.MetricName != "" {
			matchers = append(matchers, nameMatcher{exact: expr.MetricName})
			continue
		}
		if pat, ok := expr.LabelMatch[PromLabelName]; ok {
			re, err := regexp.Compile(anchorPromRegex(pat))
			if err != nil {
				continue // skip selector with bad regex
			}
			matchers = append(matchers, nameMatcher{re: re})
			continue
		}
		// No __name__ constraint → selector matches any name → union is all.
		return names
	}

	if len(matchers) == 0 {
		// Every selector was malformed/uncompilable and none matched-all.
		// Be conservative: return nothing rather than everything.
		return nil
	}

	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if _, dup := seen[n]; dup {
			continue
		}
		for _, mm := range matchers {
			if mm.re != nil {
				if mm.re.MatchString(n) {
					seen[n] = struct{}{}
					out = append(out, n)
					break
				}
			} else if mm.exact == n {
				seen[n] = struct{}{}
				out = append(out, n)
				break
			}
		}
	}
	return out
}
