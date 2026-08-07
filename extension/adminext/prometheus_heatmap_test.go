package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A brace-less remainder is treated as a metric name. For an expression the
// parser does not understand, that remainder is raw query text, which becomes a
// term query for an impossible metric — HTTP 200 with an empty vector, which
// Grafana renders as "No data". The user cannot tell an unsupported query from
// a genuinely empty one, which is how several of these bugs stayed hidden.
func TestParsePromQL_RejectsUnsupportedExpressions(t *testing.T) {
	unsupported := []string{
		// Binary operators: the aggregation wrapper eats the left operand, so the
		// leftover is the trailing operand alone. Silently returned the LEFT
		// side's value as if it were the ratio -- wrong data, not missing data.
		`sum(rate(m[5m])) / sum(rate(m[5m]))`,
		`sum(rate(m[5m])) * 2`,
		`a - b`,
		`a + b`,
		// Aggregation modifier the parser does not implement.
		`sum without (span_kind) (rate(m[5m]))`,
		// Free-form text.
		`this is not promql at all`,
		// Unknown function.
		`bogus_func(m)`,
	}

	for _, q := range unsupported {
		t.Run(q, func(t *testing.T) {
			_, err := parsePromQL(q)
			assert.Error(t, err, "must reject rather than return an empty result")
		})
	}
}

// The rejection must not overreach: everything the backend genuinely supports
// has to keep parsing, including OTel's dotted metric names.
func TestParsePromQL_AcceptsSupportedExpressions(t *testing.T) {
	supported := []string{
		`traces_spanmetrics_calls_total`,
		`jvm.memory.used`,
		`{"jvm.memory.used"}`,
		`{__name__="jvm.memory.used"}`,
		`sum(rate(traces_spanmetrics_calls_total[5m]))`,
		`sum by (service_name) (rate(m[5m]))`,
		`avg({"jvm.memory.used"})`,
		`histogram_quantile(0.99, sum by (le) (rate(m[5m])))`,
		`topk(5, sum by (service_name) (rate(m[5m])))`,
		`m{service_name!~"test.*", span_kind!="Client"}`,
		// Compound Go-style durations, which $__rate_interval interpolates to.
		`sum(rate(m[1m0s]))`,
		`sum(rate(m[4m30s]))`,
	}

	for _, q := range supported {
		t.Run(q, func(t *testing.T) {
			_, err := parsePromQL(q)
			assert.NoError(t, err)
		})
	}
}

// *_over_time is NOT implemented: parseFuncWrapper only knows rate/increase/
// irate, so `max_over_time(m[5m])` fell through to the bare-selector path and
// returned the raw latest value. max, min and count all produced byte-for-byte
// identical output -- count_over_time reported a memory-byte figure instead of
// a sample count. Rejecting is the honest behaviour until it is implemented;
// this test pins that so the functions cannot be silently reintroduced as
// no-ops.
func TestParsePromQL_RejectsUnimplementedOverTime(t *testing.T) {
	for _, q := range []string{
		`max_over_time(m[5m])`,
		`min_over_time(m[5m])`,
		`count_over_time(m[5m])`,
		`avg_over_time(m[5m])`,
		`sum_over_time(m[5m])`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := parsePromQL(q)
			assert.Error(t, err, "unimplemented function must not masquerade as a bare selector")
		})
	}
}

func TestGroupsByLe(t *testing.T) {
	assert.True(t, groupsByLe([]string{"le"}))
	assert.True(t, groupsByLe([]string{"le", "service_name"}))
	assert.True(t, groupsByLe([]string{"service_name", "le"}))
	assert.False(t, groupsByLe([]string{"service_name"}))
	assert.False(t, groupsByLe(nil))
}

func TestFormatPromFloat(t *testing.T) {
	// le bounds must render the way Prometheus writes them: shortest form that
	// round-trips, not Go's default %v padding.
	assert.Equal(t, "0.005", formatPromFloat(0.005))
	assert.Equal(t, "1", formatPromFloat(1))
	assert.Equal(t, "2.5", formatPromFloat(2.5))
	assert.Equal(t, "10000", formatPromFloat(10000))
}

// The heatmap query `sum by (le) (rate(m[5m]))` must yield one series per
// bucket. ES stores no "le" label -- bucket data lives in bucket_counts /
// explicit_bounds arrays -- so grouping by it collapsed every bucket into a
// single series and the heatmap rendered one flat row.
func TestAddBucketPoint_BuildsOneSeriesPerLe(t *testing.T) {
	series := make(map[bucketSeriesKey]*promMatrixSample)
	var order []bucketSeriesKey

	groupLabels := map[string]string{"service_name": "checkout"}
	addBucketPoint(series, &order, bucketSeriesKey{"g", "0.5"}, groupLabels, "0.5", 1000, 2)
	addBucketPoint(series, &order, bucketSeriesKey{"g", "1"}, groupLabels, "1", 1000, 5)
	addBucketPoint(series, &order, bucketSeriesKey{"g", "+Inf"}, groupLabels, "+Inf", 1000, 7)
	// A second timestamp appends to the existing series rather than making new ones.
	addBucketPoint(series, &order, bucketSeriesKey{"g", "0.5"}, groupLabels, "0.5", 2000, 3)

	assert.Len(t, series, 3, "one series per distinct le")
	assert.Equal(t, []bucketSeriesKey{{"g", "0.5"}, {"g", "1"}, {"g", "+Inf"}}, order,
		"insertion order preserved for stable output")

	first := series[bucketSeriesKey{"g", "0.5"}]
	assert.Len(t, first.Values, 2, "second timestamp appended to the same series")
	assert.Equal(t, "0.5", first.Metric[PromLabelLe])
	assert.Equal(t, "checkout", first.Metric["service_name"])
}
