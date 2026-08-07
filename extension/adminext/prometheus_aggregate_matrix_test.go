// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matrixSample builds a promMatrixSample from (ts, value) pairs.
func matrixSample(metric promMetric, pts ...[2]float64) promMatrixSample {
	values := make([][]any, 0, len(pts))
	for _, p := range pts {
		values = append(values, []any{p[0], formatPromValue(p[1])})
	}
	return promMatrixSample{Metric: metric, Values: values}
}

// valuesAt reads a series' value at a timestamp.
func valuesAt(t *testing.T, s promMatrixSample, ts float64) float64 {
	t.Helper()
	for _, tv := range s.Values {
		if tv[0].(float64) == ts {
			f, err := strconv.ParseFloat(tv[1].(string), 64)
			require.NoError(t, err)
			return f
		}
	}
	t.Fatalf("no sample at ts=%v", ts)
	return 0
}

// TestAggregateMatrix_NoGroupByCollapsesToOne guards the regression where range
// aggregation was a pass-through: `sum(rate(m[5m]))` returned every underlying
// series instead of one, while the instant path correctly returned a single
// value. Grafana rendered hundreds of lines where one was expected.
func TestAggregateMatrix_NoGroupByCollapsesToOne(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"__name__": "m", "svc": "a"}, [2]float64{100, 1}, [2]float64{200, 2}),
		matrixSample(promMetric{"__name__": "m", "svc": "b"}, [2]float64{100, 3}, [2]float64{200, 4}),
		matrixSample(promMetric{"__name__": "m", "svc": "c"}, [2]float64{100, 5}, [2]float64{200, 6}),
	}

	got := aggregateMatrix(AggSum, nil, in)
	require.Len(t, got, 1, "sum() without by() must produce exactly one series")
	assert.Empty(t, got[0].Metric, "aggregated series carries no labels")
	assert.InDelta(t, 9.0, valuesAt(t, got[0], 100), 1e-9)
	assert.InDelta(t, 12.0, valuesAt(t, got[0], 200), 1e-9)
}

// TestAggregateMatrix_GroupByCollapsesPerLabel verifies by() collapses to one
// series per distinct label value, not one per input series.
func TestAggregateMatrix_GroupByCollapsesPerLabel(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"__name__": "m", "svc": "a", "op": "x"}, [2]float64{100, 1}),
		matrixSample(promMetric{"__name__": "m", "svc": "a", "op": "y"}, [2]float64{100, 2}),
		matrixSample(promMetric{"__name__": "m", "svc": "b", "op": "x"}, [2]float64{100, 4}),
	}

	got := aggregateMatrix(AggSum, []string{"svc"}, in)
	require.Len(t, got, 2, "one series per distinct svc")

	bySvc := map[string]promMatrixSample{}
	for _, s := range got {
		bySvc[s.Metric["svc"]] = s
	}
	assert.InDelta(t, 3.0, valuesAt(t, bySvc["a"], 100), 1e-9)
	assert.InDelta(t, 4.0, valuesAt(t, bySvc["b"], 100), 1e-9)
	// Only the grouping label survives.
	assert.NotContains(t, bySvc["a"].Metric, "op")
	assert.NotContains(t, bySvc["a"].Metric, "__name__")
}

// TestAggregateMatrix_Operators covers each supported aggregation operator.
func TestAggregateMatrix_Operators(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"svc": "a"}, [2]float64{100, 2}),
		matrixSample(promMetric{"svc": "b"}, [2]float64{100, 8}),
		matrixSample(promMetric{"svc": "c"}, [2]float64{100, 5}),
	}

	for _, tc := range []struct {
		fn   string
		want float64
	}{
		{AggSum, 15},
		{AggAvg, 5},
		{AggMax, 8},
		{AggMin, 2},
		{AggCount, 3},
	} {
		got := aggregateMatrix(tc.fn, nil, in)
		require.Len(t, got, 1, tc.fn)
		assert.InDeltaf(t, tc.want, valuesAt(t, got[0], 100), 1e-9, "operator %s", tc.fn)
	}
}

// TestAggregateMatrix_MisalignedTimestamps verifies a timestamp missing from one
// series simply does not contribute to that point, as in Prometheus, rather than
// dropping the point or poisoning it.
func TestAggregateMatrix_MisalignedTimestamps(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"svc": "a"}, [2]float64{100, 1}, [2]float64{200, 2}),
		matrixSample(promMetric{"svc": "b"}, [2]float64{200, 4}), // no sample at 100
	}

	got := aggregateMatrix(AggSum, nil, in)
	require.Len(t, got, 1)
	assert.InDelta(t, 1.0, valuesAt(t, got[0], 100), 1e-9, "only series a contributes")
	assert.InDelta(t, 6.0, valuesAt(t, got[0], 200), 1e-9)
}

// TestAggregateMatrix_TimestampsAscending verifies output stays chronological
// even when input series start at different times.
func TestAggregateMatrix_TimestampsAscending(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"svc": "a"}, [2]float64{300, 1}),
		matrixSample(promMetric{"svc": "b"}, [2]float64{100, 1}, [2]float64{200, 1}),
	}

	got := aggregateMatrix(AggSum, nil, in)
	require.Len(t, got, 1)
	ts := make([]float64, 0, len(got[0].Values))
	for _, tv := range got[0].Values {
		ts = append(ts, tv[0].(float64))
	}
	assert.IsIncreasing(t, ts)
}

// TestAggregateMatrix_SkipsNonFiniteValues verifies NaN/Inf (emitted as strings
// by formatPromValue) do not poison an aggregate.
func TestAggregateMatrix_SkipsNonFiniteValues(t *testing.T) {
	in := []promMatrixSample{
		matrixSample(promMetric{"svc": "a"}, [2]float64{100, 3}),
		{Metric: promMetric{"svc": "b"}, Values: [][]any{{float64(100), "NaN"}}},
	}

	got := aggregateMatrix(AggSum, nil, in)
	require.Len(t, got, 1)
	assert.InDelta(t, 3.0, valuesAt(t, got[0], 100), 1e-9)
}

// TestAggregateMatrix_EmptyInput verifies the empty case is a no-op rather than
// producing a phantom series.
func TestAggregateMatrix_EmptyInput(t *testing.T) {
	assert.Empty(t, aggregateMatrix(AggSum, nil, nil))
	assert.Empty(t, aggregateMatrix(AggSum, []string{"svc"}, []promMatrixSample{}))
}

// TestPromMetricType maps stored OTel types onto the Prometheus metadata
// vocabulary, defaulting unknown values to gauge.
func TestPromMetricType(t *testing.T) {
	assert.Equal(t, "counter", promMetricType("counter"))
	assert.Equal(t, "gauge", promMetricType("gauge"))
	assert.Equal(t, "histogram", promMetricType("histogram"))
	assert.Equal(t, "summary", promMetricType("summary"))
	assert.Equal(t, "gauge", promMetricType(""), "missing type falls back to gauge")
	assert.Equal(t, "gauge", promMetricType("something-else"))
}
