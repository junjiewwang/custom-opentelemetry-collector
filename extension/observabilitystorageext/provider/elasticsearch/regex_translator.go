// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"regexp"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
)

// RegexTranslateStrategy indicates how a regex pattern should be handled at the ES level.
type RegexTranslateStrategy int

const (
	// StrategyTerm means the regex resolves to a single exact value → use ES "term" query.
	StrategyTerm RegexTranslateStrategy = iota
	// StrategyTerms means the regex is a "|" alternation of exact values → use ES "terms" query.
	StrategyTerms
	// StrategyPrefix means the regex is a simple prefix pattern → use ES "prefix" query.
	StrategyPrefix
	// StrategyUnsupported means the regex is too complex for ES flattened field → must be post-filtered.
	StrategyUnsupported
)

// RegexTranslation holds the result of translating a PromQL regex into an ES-compatible strategy.
type RegexTranslation struct {
	Strategy RegexTranslateStrategy
	// Values contains the literal value(s) to match.
	// For StrategyTerm: single element; for StrategyTerms: multiple elements.
	Values []string
	// Prefix is set only for StrategyPrefix.
	Prefix string
	// OriginalRegex is the raw PromQL regex (for StrategyUnsupported, used for post-filtering).
	OriginalRegex string
}

// TranslatePromQLRegex converts a PromQL =~ regex pattern into an ES-compatible query strategy.
//
// PromQL regex patterns typically follow these forms:
//   - "exact_value" → single term
//   - "value1|value2|value3" → terms (OR)
//   - "prefix.*" → prefix query
//   - Complex patterns with *, +, [], () → unsupported for flattened fields
//
// The function handles PromQL's `\.` escape (literal dot) by unescaping it to `.`.
func TranslatePromQLRegex(pattern string) RegexTranslation {
	if pattern == "" {
		return RegexTranslation{Strategy: StrategyUnsupported, OriginalRegex: pattern}
	}

	// Split by unescaped "|" (alternation) to detect multi-value patterns.
	alternatives := splitUnescapedPipe(pattern)

	// Check if all alternatives are "exact" (no regex metacharacters except \.).
	var literals []string
	for _, alt := range alternatives {
		if isLiteralWithEscapedDots(alt) {
			literals = append(literals, unescapePromQLRegex(alt))
		} else if isPrefixPattern(alt) {
			// If there's only one alternative and it's a prefix pattern, use prefix strategy.
			if len(alternatives) == 1 {
				prefix := extractPrefix(alt)
				return RegexTranslation{
					Strategy:      StrategyPrefix,
					Prefix:        prefix,
					OriginalRegex: pattern,
				}
			}
			// Mixed alternation with prefix is unsupported.
			return RegexTranslation{Strategy: StrategyUnsupported, OriginalRegex: pattern}
		} else {
			// Complex regex → unsupported.
			return RegexTranslation{Strategy: StrategyUnsupported, OriginalRegex: pattern}
		}
	}

	if len(literals) == 0 {
		return RegexTranslation{Strategy: StrategyUnsupported, OriginalRegex: pattern}
	}

	if len(literals) == 1 {
		return RegexTranslation{
			Strategy:      StrategyTerm,
			Values:        literals,
			OriginalRegex: pattern,
		}
	}

	return RegexTranslation{
		Strategy:      StrategyTerms,
		Values:        literals,
		OriginalRegex: pattern,
	}
}

// BuildESClauseFromRegex creates an ES query clause from a RegexTranslation.
// Returns nil if the translation strategy is unsupported (caller should handle post-filtering).
func BuildESClauseFromRegex(field string, translation RegexTranslation) map[string]any {
	switch translation.Strategy {
	case StrategyTerm:
		return query.TermQ(field, translation.Values[0])
	case StrategyTerms:
		return query.TermsQ(field, translation.Values)
	case StrategyPrefix:
		return map[string]any{
			"prefix": map[string]any{field: translation.Prefix},
		}
	default:
		return nil
	}
}

// PostFilterByRegex checks if a label value matches the original PromQL regex pattern.
// Used for StrategyUnsupported cases where ES cannot handle the regex natively.
// The pattern is anchored (^...$) as per PromQL semantics.
func PostFilterByRegex(value, promqlPattern string) bool {
	// PromQL regex is implicitly anchored: ^pattern$
	anchoredPattern := "^(?:" + promqlPattern + ")$"
	matched, err := regexp.MatchString(anchoredPattern, value)
	if err != nil {
		return false
	}
	return matched
}

// ── Internal helpers ─────────────────────────────────

// splitUnescapedPipe splits a pattern by unescaped "|" characters.
// A pipe is escaped if preceded by an odd number of backslashes.
func splitUnescapedPipe(pattern string) []string {
	var parts []string
	var current strings.Builder
	i := 0
	for i < len(pattern) {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			// Escaped character — consume both.
			current.WriteByte(pattern[i])
			current.WriteByte(pattern[i+1])
			i += 2
		} else if pattern[i] == '|' {
			parts = append(parts, current.String())
			current.Reset()
			i++
		} else {
			current.WriteByte(pattern[i])
			i++
		}
	}
	parts = append(parts, current.String())
	return parts
}

// isLiteralWithEscapedDots checks whether a pattern is a literal string
// that only uses `\.` for escaping dots (no other regex metacharacters).
// This covers the most common Grafana/PromQL pattern: "opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export"
func isLiteralWithEscapedDots(pattern string) bool {
	i := 0
	for i < len(pattern) {
		ch := pattern[i]
		if ch == '\\' {
			// Only allow \. (escaped dot) — other escapes indicate complex regex.
			if i+1 < len(pattern) && pattern[i+1] == '.' {
				i += 2
				continue
			}
			// Other escape sequences (e.g., \d, \w, \\) → not a pure literal.
			return false
		}
		// Regex metacharacters that indicate non-literal patterns.
		if isRegexMeta(ch) {
			return false
		}
		i++
	}
	return true
}

// isPrefixPattern checks if a pattern is of the form "literal.*" (prefix match).
func isPrefixPattern(pattern string) bool {
	if !strings.HasSuffix(pattern, ".*") {
		return false
	}
	// Check that everything before .* is a literal.
	prefix := pattern[:len(pattern)-2]
	return isLiteralWithEscapedDots(prefix)
}

// extractPrefix extracts the literal prefix from a "literal.*" pattern.
func extractPrefix(pattern string) string {
	prefix := pattern[:len(pattern)-2]
	return unescapePromQLRegex(prefix)
}

// unescapePromQLRegex removes PromQL regex escaping (specifically \. → .).
func unescapePromQLRegex(pattern string) string {
	var result strings.Builder
	result.Grow(len(pattern))
	i := 0
	for i < len(pattern) {
		if pattern[i] == '\\' && i+1 < len(pattern) && pattern[i+1] == '.' {
			result.WriteByte('.')
			i += 2
		} else {
			result.WriteByte(pattern[i])
			i++
		}
	}
	return result.String()
}

// isRegexMeta returns true if the character is a regex metacharacter
// that would make the pattern non-literal.
func isRegexMeta(ch byte) bool {
	switch ch {
	case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '^', '$':
		return true
	}
	return false
}
