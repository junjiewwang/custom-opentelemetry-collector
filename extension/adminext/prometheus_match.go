// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"regexp"
)

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
		if v, ok := expr.Labels[PromLabelName]; ok {
			matchers = append(matchers, nameMatcher{exact: v})
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
