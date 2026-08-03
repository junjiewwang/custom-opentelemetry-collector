// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterMetricNamesByMatch_NoMatch(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := filterMetricNamesByMatch(in, nil)
	assert.Equal(t, in, got, "no match[] → return input unchanged")
	// Same backing array (no copy) when there is nothing to filter.
	assert.Same(t, &in[0], &got[0], "no match[] → return same slice (no copy)")
}

func TestFilterMetricNamesByMatch_Exact(t *testing.T) {
	in := []string{"alpha", "beta", "gamma"}
	got := filterMetricNamesByMatch(in, []string{`{__name__="beta"}`})
	assert.Equal(t, []string{"beta"}, got)
}

func TestFilterMetricNamesByMatch_RegexAnchored(t *testing.T) {
	in := []string{"pool", "bufferpool_wait_total", "pool_size", "gamma"}
	// Prometheus =~ is fully anchored: "pool" matches only exactly "pool".
	got := filterMetricNamesByMatch(in, []string{`{__name__=~"pool"}`})
	assert.Equal(t, []string{"pool"}, got)
	// ".*pool.*" matches names containing "pool".
	got = filterMetricNamesByMatch(in, []string{`{__name__=~".*pool.*"}`})
	assert.Equal(t, []string{"pool", "bufferpool_wait_total", "pool_size"}, got)
	// Prefix regex.
	got = filterMetricNamesByMatch(in, []string{`{__name__=~"pool.*"}`})
	assert.Equal(t, []string{"pool", "pool_size"}, got)
}

func TestFilterMetricNamesByMatch_OrAcrossSelectors(t *testing.T) {
	in := []string{"alpha", "beta", "gamma", "delta"}
	got := filterMetricNamesByMatch(in, []string{
		`{__name__="alpha"}`,
		`{__name__=~"gam.*"}`,
	})
	assert.Equal(t, []string{"alpha", "gamma"}, got)
}

func TestFilterMetricNamesByMatch_SelectorWithoutNameMatchesAll(t *testing.T) {
	in := []string{"a", "b", "c"}
	// A selector with no __name__ constraint matches any name → union is all.
	got := filterMetricNamesByMatch(in, []string{`{job="x"}`})
	assert.Equal(t, in, got)
	// Even mixed: an all-matching selector OR'd with a restrictive one = all.
	got = filterMetricNamesByMatch(in, []string{`{__name__="a"}`, `{job="x"}`})
	assert.Equal(t, in, got)
}

func TestFilterMetricNamesByMatch_MalformedSelectorSkipped(t *testing.T) {
	in := []string{"a", "b"}
	// Malformed selector is skipped; the valid one still filters.
	got := filterMetricNamesByMatch(in, []string{`{not closed`, `{__name__="a"}`})
	assert.Equal(t, []string{"a"}, got)
}

func TestFilterMetricNamesByMatch_BadRegexSkipped(t *testing.T) {
	in := []string{"a", "b"}
	// Uncompilable regex (unclosed group) → selector skipped. All selectors
	// skipped → conservative empty result.
	got := filterMetricNamesByMatch(in, []string{`{__name__=~"[unclosed"}`})
	assert.Empty(t, got)
}

func TestFilterMetricNamesByMatch_DedupAndOrder(t *testing.T) {
	in := []string{"alpha", "beta", "alpha", "gamma"}
	got := filterMetricNamesByMatch(in, []string{
		`{__name__=~"alpha|beta"}`,
	})
	assert.Equal(t, []string{"alpha", "beta"}, got, "dedup, preserve first-seen order")
}

func TestFilterMetricNamesByMatch_EmptyNames(t *testing.T) {
	got := filterMetricNamesByMatch(nil, []string{`{__name__="a"}`})
	assert.Empty(t, got)
}

func TestAnchorPromRegex(t *testing.T) {
	assert.Equal(t, `^(?:pool.*)$`, anchorPromRegex("pool.*"))
	assert.Equal(t, `^(?:(?i)foo)$`, anchorPromRegex("(?i)foo"))
}
