// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.uber.org/zap"
)

// concurrentQueryConfig holds the configuration for concurrent query splitting.
type concurrentQueryConfig struct {
	// MinTermsForSplit is the minimum number of terms in a regex alternation
	// before splitting into concurrent queries (default: 2).
	MinTermsForSplit int
	// MaxConcurrency limits the number of concurrent QueryFlat calls (default: 10).
	MaxConcurrency int
}

var defaultConcurrentConfig = concurrentQueryConfig{
	MinTermsForSplit: 2,
	MaxConcurrency:   10,
}

// splitCandidate represents a label key whose regex pattern can be split into
// multiple concurrent term queries.
type splitCandidate struct {
	Key    string   // label key in LabelMatch (e.g., "span_name")
	Values []string // individual term values from the pipe-separated pattern
}

// findSplitCandidate scans labelMatch for the first regex pattern that is a simple
// pipe-separated alternation of literal values (e.g., "A|B|C|D|E").
// Returns nil if no suitable candidate is found.
//
// Only considers patterns where all alternatives are literal (no regex metacharacters),
// matching the StrategyTerms behavior of TranslatePromQLRegex.
func findSplitCandidate(labelMatch map[string]string, minTerms int) *splitCandidate {
	for key, pattern := range labelMatch {
		values := splitPipeLiterals(pattern)
		if len(values) >= minTerms {
			return &splitCandidate{Key: key, Values: values}
		}
	}
	return nil
}

// splitPipeLiterals splits a PromQL regex pattern by unescaped "|" and returns
// the individual values only if ALL alternatives are literal (no regex metacharacters
// other than escaped dots). Returns nil if the pattern contains complex regex.
func splitPipeLiterals(pattern string) []string {
	parts := splitUnescapedPipeLocal(pattern)
	if len(parts) <= 1 {
		return nil
	}

	values := make([]string, 0, len(parts))
	for _, p := range parts {
		if !isLiteralOrEscapedDots(p) {
			return nil // contains complex regex, cannot split
		}
		values = append(values, unescapePromQLDots(p))
	}
	return values
}

// splitUnescapedPipeLocal splits by unescaped "|" characters.
// This is a local copy to avoid cross-package dependency on the ES provider.
func splitUnescapedPipeLocal(pattern string) []string {
	var parts []string
	var current strings.Builder
	i := 0
	for i < len(pattern) {
		if pattern[i] == '\\' && i+1 < len(pattern) {
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

// isLiteralOrEscapedDots returns true if the string contains only literal characters
// and escaped dots (\.), with no unescaped regex metacharacters.
func isLiteralOrEscapedDots(s string) bool {
	metaChars := `*+?[]{}()^$`
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			// Escaped character — skip both. Only \. is expected in PromQL.
			i += 2
			continue
		}
		if strings.ContainsRune(metaChars, rune(s[i])) {
			return false
		}
		// Unescaped "." is a regex wildcard — not a literal.
		if s[i] == '.' {
			return false
		}
		i++
	}
	return true
}

// unescapePromQLDots converts PromQL's \. (escaped literal dot) back to ".".
func unescapePromQLDots(s string) string {
	return strings.ReplaceAll(s, `\.`, ".")
}

// concurrentFlatResult holds the merged result from concurrent QueryFlat calls.
type concurrentFlatResult struct {
	Result *observabilitystorageext.MetricFlatResult
	Err    error
}

// concurrentQueryFlat splits a QueryFlat call into multiple concurrent sub-queries
// when a splittable multi-value regex is detected. Each sub-query fetches data for
// one term value, and results are merged.
//
// If no splittable pattern is found, falls back to a single QueryFlat call.
// This preserves correctness: the merged result is identical to a single query with
// the original terms filter.
func (e *Extension) concurrentQueryFlat(
	ctx context.Context,
	flatQuery observabilitystorageext.MetricFlatQuery,
	logger *zap.Logger,
) (*observabilitystorageext.MetricFlatResult, error) {
	cfg := defaultConcurrentConfig

	// Find a label that can be split into concurrent queries.
	candidate := findSplitCandidate(flatQuery.LabelMatch, cfg.MinTermsForSplit)
	if candidate == nil {
		// No splittable pattern — fall back to single query.
		return e.storageMetricReader.QueryFlat(ctx, flatQuery)
	}

	numTerms := len(candidate.Values)
	if numTerms > cfg.MaxConcurrency {
		// Too many terms — fall back to single query to avoid goroutine explosion.
		return e.storageMetricReader.QueryFlat(ctx, flatQuery)
	}

	logger.Debug("concurrent QueryFlat: splitting regex into parallel queries",
		zap.String("label", candidate.Key),
		zap.Int("terms", numTerms),
	)

	// Launch concurrent sub-queries, one per term value.
	type subResult struct {
		samples []observabilitystorageext.MetricSample
		total   int64
		err     error
	}

	results := make([]subResult, numTerms)
	var wg sync.WaitGroup
	wg.Add(numTerms)

	for i, value := range candidate.Values {
		go func(idx int, termValue string) {
			defer wg.Done()

			// Build sub-query: replace the pipe-separated regex pattern in LabelMatch
			// with a single literal value. We keep the value in LabelMatch (rather than
			// moving it to Labels) to preserve the same code path: TranslatePromQLRegex
			// processes the single literal as a term query, without going through
			// translateLabelValue normalization that Labels would trigger.
			subLabelMatch := cloneLabelMatchWithSingleTerm(flatQuery.LabelMatch, candidate.Key, termValue)

			subQuery := observabilitystorageext.MetricFlatQuery{
				MetricName:  flatQuery.MetricName,
				Labels:      flatQuery.Labels, // unchanged — same exact-match constraints
				LabelMatch:  subLabelMatch,
				ServiceName: flatQuery.ServiceName,
				TimeRange:   flatQuery.TimeRange,
				MaxDocs:     flatQuery.MaxDocs,
			}

			result, err := e.storageMetricReader.QueryFlat(ctx, subQuery)
			if err != nil {
				results[idx] = subResult{err: err}
				return
			}
			if result != nil {
				results[idx] = subResult{samples: result.Samples, total: result.Total}
			}
		}(i, value)
	}

	wg.Wait()

	// Merge results.
	var allSamples []observabilitystorageext.MetricSample
	var totalDocs int64
	for _, r := range results {
		if r.err != nil {
			// If any sub-query fails, return the first error.
			return nil, r.err
		}
		allSamples = append(allSamples, r.samples...)
		totalDocs += r.total
	}

	if len(allSamples) == 0 {
		return &observabilitystorageext.MetricFlatResult{
			Samples: nil,
			Total:   0,
		}, nil
	}

	return &observabilitystorageext.MetricFlatResult{
		Samples: allSamples,
		Total:   totalDocs,
	}, nil
}

// cloneLabelMatchWithSingleTerm returns a copy of labelMatch with the given key
// set to the single termValue (as a literal regex pattern, not an exact label).
// This preserves the TranslatePromQLRegex code path so the value is NOT
// normalized by translateLabelValue.
func cloneLabelMatchWithSingleTerm(labelMatch map[string]string, key, termValue string) map[string]string {
	result := make(map[string]string, len(labelMatch))
	for k, v := range labelMatch {
		if k == key {
			result[k] = termValue // replace pipe-separated pattern with single literal
		} else {
			result[k] = v
		}
	}
	return result
}

// cloneLabelsWithTerm returns a copy of labels with the given key set to termValue.
// Deprecated: prefer cloneLabelMatchWithSingleTerm to avoid translateLabelValue normalization.
func cloneLabelsWithTerm(labels map[string]string, key, termValue string) map[string]string {
	result := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		result[k] = v
	}
	result[key] = termValue
	return result
}

// cloneLabelMatchWithout returns a copy of labelMatch with the specified key removed.
// Returns nil if the resulting map would be empty.
// Deprecated: prefer cloneLabelMatchWithSingleTerm.
func cloneLabelMatchWithout(labelMatch map[string]string, key string) map[string]string {
	if len(labelMatch) <= 1 {
		return nil
	}
	result := make(map[string]string, len(labelMatch)-1)
	for k, v := range labelMatch {
		if k != key {
			result[k] = v
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
