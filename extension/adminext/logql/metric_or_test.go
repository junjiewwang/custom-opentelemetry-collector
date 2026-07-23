// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Metric query with OR-decomposed inner LogQL.
func TestParseMetric_OR_Inner(t *testing.T) {
	input := `sum by (level) (count_over_time({app="foo"} | json | level="error" OR level="warn" [5m]))`
	expr, err := ParseMetric(input)
	require.NoError(t, err)
	assert.Equal(t, "sum", expr.Aggregation)
	assert.Equal(t, "count_over_time", expr.Function)
	assert.Equal(t, []string{"level"}, expr.By)
	assert.Equal(t, 5*time.Minute, expr.RangeDuration)
	assert.Nil(t, expr.Inner, "OR detected: Inner should be nil")
	require.Len(t, expr.InnerBranches, 2)
	assert.Equal(t, "foo", expr.InnerBranches[0].StreamSelector.Matchers[0].Value)
	assert.Equal(t, "foo", expr.InnerBranches[1].StreamSelector.Matchers[0].Value)
}

// Backward-compatible: no OR, Inner populated.
func TestParseMetric_NoOR_InnerUnchanged(t *testing.T) {
	input := `sum by (level) (count_over_time({app="foo"} | json | level="error" [5m]))`
	expr, err := ParseMetric(input)
	require.NoError(t, err)
	assert.Equal(t, "sum", expr.Aggregation)
	assert.NotNil(t, expr.Inner, "no OR: Inner should be set")
	assert.Nil(t, expr.InnerBranches)
}
