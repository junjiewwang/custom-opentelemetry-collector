package adminext

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stddev/stdvar were absent from AggFuncs, so `stddev(m)` never matched the
// aggregation wrapper and fell through to the bare-selector path: it returned
// all 6 raw series unchanged instead of 1 aggregated value. The plugin's
// function picker offers both, so they are implemented rather than rejected.
//
// Prometheus uses POPULATION variance (divide by N), not sample variance (N-1).
func TestReduceSamples_StddevStdvar(t *testing.T) {
	// mean 4; deviations -2,-1,0,1,2; squares 4,1,0,1,4 = 10; /5 = 2
	vals := []float64{2, 3, 4, 5, 6}

	assert.InDelta(t, 2.0, reduceSamples(AggStdvar, vals), 1e-9)
	assert.InDelta(t, math.Sqrt(2.0), reduceSamples(AggStddev, vals), 1e-9)

	// A single sample has zero spread.
	assert.InDelta(t, 0.0, reduceSamples(AggStddev, []float64{42}), 1e-9)
	assert.InDelta(t, 0.0, reduceSamples(AggStdvar, []float64{42}), 1e-9)

	// Identical samples have zero spread.
	assert.InDelta(t, 0.0, reduceSamples(AggStddev, []float64{7, 7, 7}), 1e-9)
}

func TestAggregateGroup_StddevStdvar(t *testing.T) {
	group := []promVectorSample{
		{Metric: promMetric{"a": "1"}, Value: []any{1000.0, "2"}},
		{Metric: promMetric{"a": "2"}, Value: []any{1000.0, "4"}},
		{Metric: promMetric{"a": "3"}, Value: []any{1000.0, "6"}},
	}
	// mean 4; squares 4,0,4 = 8; /3
	wantVar := 8.0 / 3.0

	got := aggregateGroup(AggStdvar, group)
	assert.Equal(t, formatPromValue(wantVar), got.Value[1])

	got = aggregateGroup(AggStddev, group)
	assert.Equal(t, formatPromValue(math.Sqrt(wantVar)), got.Value[1])
}

// Refactoring aggregateGroup to parse values once must not change the existing
// operators, including how count treats an unparseable sample.
func TestAggregateGroup_ExistingOperatorsUnchanged(t *testing.T) {
	group := []promVectorSample{
		{Metric: promMetric{}, Value: []any{1000.0, "10"}},
		{Metric: promMetric{}, Value: []any{1000.0, "20"}},
		{Metric: promMetric{}, Value: []any{1000.0, "30"}},
	}

	assert.Equal(t, formatPromValue(60), aggregateGroup(AggSum, group).Value[1])
	assert.Equal(t, formatPromValue(20), aggregateGroup(AggAvg, group).Value[1])
	assert.Equal(t, formatPromValue(30), aggregateGroup(AggMax, group).Value[1])
	assert.Equal(t, formatPromValue(10), aggregateGroup(AggMin, group).Value[1])
	assert.Equal(t, formatPromValue(3), aggregateGroup(AggCount, group).Value[1])
}

func TestAggregateGroup_CountIncludesUnparseableSamples(t *testing.T) {
	// count reports group cardinality; a NaN string is still a member.
	group := []promVectorSample{
		{Metric: promMetric{}, Value: []any{1000.0, "10"}},
		{Metric: promMetric{}, Value: []any{1000.0, "NaN"}},
	}
	assert.Equal(t, formatPromValue(2), aggregateGroup(AggCount, group).Value[1])
	// ...but it must not poison sum.
	assert.Equal(t, formatPromValue(10), aggregateGroup(AggSum, group).Value[1])
}

func TestParsePromQL_StddevIsAnAggregation(t *testing.T) {
	for _, fn := range []string{AggStddev, AggStdvar} {
		expr, err := parsePromQL(fn + `({"jvm.memory.used"})`)
		assert.NoError(t, err)
		assert.Equal(t, fn, expr.Aggregation, "%s must parse as an aggregation, not a metric name", fn)
	}

	expr, err := parsePromQL(`stddev by (service_name) (rate(m[5m]))`)
	assert.NoError(t, err)
	assert.Equal(t, AggStddev, expr.Aggregation)
	assert.Equal(t, []string{"service_name"}, expr.GroupBy)
}

// A rate window shorter than the scrape interval yields fewer than two samples,
// so the rate path produces no vectors. Aggregating that emitted a zero-value
// sample -- serialised as {"metric":null,"value":null} -- rather than an empty
// result. Grafana renders it as a broken series instead of "no data".
func TestApplyAggregation_EmptyInputYieldsNoSeries(t *testing.T) {
	for _, groupBy := range [][]string{nil, {"service_name"}} {
		assert.Empty(t, applyAggregation(AggSum, groupBy, nil))
		assert.Empty(t, applyAggregation(AggSum, groupBy, []promVectorSample{}))
	}
}
