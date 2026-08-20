// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── quantile_over_time ───────────────────────────────────────────────────

// TestQuantileToPercent verifies the TraceQL fraction → ES percentage
// conversion. Passing the raw fraction to ES requested the 0.9th percentile
// (≈ the minimum) instead of p90, so durations came back ~500x too small.
func TestQuantileToPercent(t *testing.T) {
	assert.InDelta(t, 50.0, quantileToPercent(0.5), 1e-9)
	assert.InDelta(t, 90.0, quantileToPercent(0.9), 1e-9)
	assert.InDelta(t, 99.0, quantileToPercent(0.99), 1e-9)
	// 1.0 is the p100 fraction, not a percentage.
	assert.InDelta(t, 100.0, quantileToPercent(1), 1e-9)
	// Values already on the 0-100 scale pass through unchanged.
	assert.InDelta(t, 90.0, quantileToPercent(90), 1e-9)
}

// TestBuildMetricsSubAggregation_QuantileScale verifies the percents sent to ES
// are on the 0-100 scale.
func TestBuildMetricsSubAggregation_QuantileScale(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	agg := r.buildMetricsSubAggregation(TraceMetricsQuery{
		Function:    "quantile_over_time",
		Field:       "duration",
		Percentiles: []float64{0.5, 0.9, 0.99},
	})

	pct, ok := agg["percentiles"].(map[string]any)
	require.True(t, ok, "expected percentiles aggregation")
	assert.Equal(t, []float64{50, 90, 99}, pct["percents"])
}

// TestBuildMetricsSubAggregation_QuantileDefaults verifies the default quantiles
// are fractions, so they survive the same conversion as explicit ones.
func TestBuildMetricsSubAggregation_QuantileDefaults(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	agg := r.buildMetricsSubAggregation(TraceMetricsQuery{
		Function: "quantile_over_time",
		Field:    "duration",
	})
	pct := agg["percentiles"].(map[string]any)
	assert.Equal(t, []float64{50, 95, 99}, pct["percents"])
}

// TestParseMetricsResponse_QuantileFansOut verifies each requested quantile gets
// its own series labeled "p", matching Tempo. Previously all quantiles were
// averaged into a single meaningless series.
func TestParseMetricsResponse_QuantileFansOut(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": float64(1_000_000_000),
					"metric": map[string]any{
						// ES returns nanoseconds for the duration field.
						"values": map[string]any{
							"50.0": float64(1_000_000),   // 1ms
							"90.0": float64(47_000_000),  // 47ms
							"99.0": float64(471_000_000), // 471ms
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:    "quantile_over_time",
		Field:       "duration",
		Step:        time.Second,
		Percentiles: []float64{0.5, 0.9, 0.99},
		TimeRange:   TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 3, "expected one series per quantile")

	// Labels use the TraceQL fraction, and values are converted to seconds.
	byP := map[string]float64{}
	for _, s := range out.Series {
		require.Len(t, s.Values, 1)
		byP[s.Labels["p"]] = s.Values[0].Value
	}
	assert.InDelta(t, 0.001, byP["0.5"], 1e-9)
	assert.InDelta(t, 0.047, byP["0.9"], 1e-9)
	assert.InDelta(t, 0.471, byP["0.99"], 1e-9)
}

// TestParseMetricsResponse_QuantileNaN verifies ES's NaN for empty buckets
// becomes 0 rather than propagating into the series.
func TestParseMetricsResponse_QuantileNaN(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	// json.Marshal cannot emit NaN, so hand-write the aggregation payload.
	resp := &SearchResponse{}
	resp.Aggregations = map[string]json.RawMessage{
		"buckets": json.RawMessage(`{"buckets":[{"key":1000000000,"metric":{"values":{"90.0":"NaN"}}}]}`),
	}

	query := TraceMetricsQuery{
		Function:    "quantile_over_time",
		Field:       "duration",
		Step:        time.Second,
		Percentiles: []float64{0.9},
		TimeRange:   TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	// A "NaN" string fails float parsing and is skipped, leaving no series.
	for _, s := range out.Series {
		for _, v := range s.Values {
			assert.False(t, math.IsNaN(v.Value), "NaN must not reach the response")
		}
	}
}

// ── histogram_over_time ──────────────────────────────────────────────────

// TestQuantileFor_NoFloatDrift verifies the "p" label reproduces the quantile
// the caller asked for. Round-tripping ES's echoed percent ("99.9") through
// /100 reintroduces binary float error (0.9990000000000001), which would leak
// into the label; matching against the requested quantiles avoids that.
func TestQuantileFor_NoFloatDrift(t *testing.T) {
	requested := []float64{0.5, 0.9, 0.99, 0.999}

	assert.Equal(t, "0.999", formatSeriesKey(quantileFor(99.9, requested)))
	assert.Equal(t, "0.99", formatSeriesKey(quantileFor(99, requested)))
	assert.Equal(t, "0.9", formatSeriesKey(quantileFor(90, requested)))
	assert.Equal(t, "0.5", formatSeriesKey(quantileFor(50, requested)))

	// A percent with no matching request falls back to the divided form.
	assert.InDelta(t, 0.75, quantileFor(75, requested), 1e-9)
}

// TestParseMetricsResponse_QuantilePreservesRequestedLabel is the end-to-end
// guard for the same drift, through the parser.
func TestParseMetricsResponse_QuantilePreservesRequestedLabel(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				map[string]any{
					"key":    float64(1_000_000_000),
					"metric": map[string]any{"values": map[string]any{"99.9": float64(5_000_000)}},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:    "quantile_over_time",
		Field:       "duration",
		Step:        time.Second,
		Percentiles: []float64{0.999},
		TimeRange:   TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	assert.Equal(t, "0.999", out.Series[0].Labels["p"])
}

// TestExtractMetricValues_SortIsNumeric guards series ordering. Labels are
// formatted with %g, so small bucket bounds render in scientific notation
// ("3.2768e-05") where lexical ordering diverges from numeric ordering.
func TestExtractMetricValues_SortIsNumeric(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	raw := mustJSON(t, map[string]any{
		"buckets": map[string]any{
			"32768":      map[string]any{"doc_count": float64(1)}, // 3.2768e-05 s
			"1073741824": map[string]any{"doc_count": float64(2)}, // 1.073741824 s
			"1048576":    map[string]any{"doc_count": float64(3)}, // 0.001048576 s
		},
	})

	got, err := r.extractMetricValues(raw, 0, TraceMetricsQuery{
		Function: "histogram_over_time",
		Field:    "duration",
	})
	require.NoError(t, err)
	require.Len(t, got, 3)

	// Ascending by numeric value, not by string.
	assert.Equal(t, []string{"3.2768e-05", "0.001048576", "1.073741824"},
		[]string{got[0].key, got[1].key, got[2].key})

	// Lexical ordering would have put "0.001048576" first — confirm the keys
	// really are the kind of strings that expose the difference.
	assert.Less(t, got[1].key, got[0].key, "string order differs from numeric order here")
}

// TestParseMetricsResponse_HistogramOrderAcrossTimeBuckets guards series
// ordering when a duration bucket first appears in a later time bucket.
// Ordering by first-seen put such a bucket at the end of the response, so the
// emitted series were not ascending by bucket bound.
func TestParseMetricsResponse_HistogramOrderAcrossTimeBuckets(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				// First time bucket only sees a large duration...
				map[string]any{
					"key": float64(1_000_000_000),
					"metric": map[string]any{
						"buckets": map[string]any{
							"1073741824": map[string]any{"doc_count": float64(2)}, // ~1s
						},
					},
				},
				// ...the smallest bucket shows up only later.
				map[string]any{
					"key": float64(2_000_000_000),
					"metric": map[string]any{
						"buckets": map[string]any{
							"32768":   map[string]any{"doc_count": float64(7)}, // 3.2768e-05 s
							"1048576": map[string]any{"doc_count": float64(4)}, // ~0.001 s
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:  "histogram_over_time",
		Field:     "duration",
		Step:      time.Second,
		TimeRange: TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 2_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 3)

	got := make([]float64, len(out.Series))
	for i, s := range out.Series {
		v, err := strconv.ParseFloat(s.Labels["__bucket"], 64)
		require.NoError(t, err)
		got[i] = v
	}
	assert.IsIncreasing(t, got, "series must be ascending by bucket bound")
	assert.Equal(t, "3.2768e-05", out.Series[0].Labels["__bucket"])
}

// TestLog2BucketRanges verifies the generated ranges reproduce Tempo's
// Log2Bucketize: a value rounds up to the next power of two.
func TestLog2BucketRanges(t *testing.T) {
	ranges := log2BucketRanges()
	require.NotEmpty(t, ranges)

	// Each range is (2^(n-1), 2^n], expressed as [from, to) with a +1 shift.
	first := ranges[0]
	assert.Equal(t, "2", first["key"])
	assert.InDelta(t, 2.0, first["from"], 1e-9)
	assert.InDelta(t, 3.0, first["to"], 1e-9)

	second := ranges[1]
	assert.Equal(t, "4", second["key"])
	assert.InDelta(t, 3.0, second["from"], 1e-9)
	assert.InDelta(t, 5.0, second["to"], 1e-9)

	// Bounds are strictly increasing powers of two.
	for i, rg := range ranges {
		want := uint64(1) << (i + 1)
		assert.Equal(t, strconv.FormatUint(want, 10), rg["key"])
	}
}

// TestBuildMetricsSubAggregation_HistogramIsBucketed verifies histogram_over_time
// uses a keyed range aggregation rather than a flat value_count. A flat count
// produced a single unlabeled series, which left Grafana's yBuckets empty and
// silently disabled the RED duration heatmap.
func TestBuildMetricsSubAggregation_HistogramIsBucketed(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	agg := r.buildMetricsSubAggregation(TraceMetricsQuery{
		Function: "histogram_over_time",
		Field:    "duration",
	})

	_, isValueCount := agg["value_count"]
	assert.False(t, isValueCount, "histogram must not collapse to a single count")

	rangeAgg, ok := agg["range"].(map[string]any)
	require.True(t, ok, "expected range aggregation")
	assert.Equal(t, true, rangeAgg["keyed"])
	assert.Equal(t, FieldDurationNano, rangeAgg["field"])
	assert.NotEmpty(t, rangeAgg["ranges"])
}

// TestBuildMetricsSubAggregation_RateHasNoSubAgg guards the rate/count path:
// the histogram bucket's native doc_count is the metric, so buildMetricsSubAggregation
// must return nil (no value_count on _id — fielddata blowup; none on _doc either —
// ES 7.10 returns 0 for it, which silently zeroed every rate series).
func TestBuildMetricsSubAggregation_RateHasNoSubAgg(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	for _, fn := range []string{"rate", "count_over_time"} {
		agg := r.buildMetricsSubAggregation(TraceMetricsQuery{Function: fn})
		assert.Nil(t, agg, "%s must not issue a sub-aggregation (doc_count is the metric)", fn)
	}
}

// TestParseMetricsResponse_HistogramFansOut verifies one series per duration
// bucket, labeled __bucket in seconds, matching Tempo's heatmap contract.
func TestParseMetricsResponse_HistogramFansOut(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": float64(1_000_000_000),
					"metric": map[string]any{
						"buckets": map[string]any{
							"1048576":    map[string]any{"doc_count": float64(5)},  // ~1ms
							"1073741824": map[string]any{"doc_count": float64(2)},  // ~1s
							"2147483648": map[string]any{"doc_count": float64(0)},  // empty → dropped
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:  "histogram_over_time",
		Field:     "duration",
		Step:      time.Second,
		TimeRange: TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 2, "empty buckets are dropped")

	// __bucket is the power-of-two upper bound in seconds; the value is a count,
	// so it must not be unit-converted.
	counts := map[string]float64{}
	for _, s := range out.Series {
		require.Len(t, s.Values, 1)
		counts[s.Labels["__bucket"]] = s.Values[0].Value
	}
	assert.InDelta(t, 5.0, counts["0.001048576"], 1e-9)
	assert.InDelta(t, 2.0, counts["1.073741824"], 1e-9)
}

// TestParseMetricsResponse_HistogramGroupedKeepsBothLabels verifies a bucketed
// function combined with by() carries the group label and __bucket together.
func TestParseMetricsResponse_HistogramGroupedKeepsBothLabels(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"by_service": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": "gateway",
					"buckets": map[string]any{
						"buckets": []any{
							map[string]any{
								"key": float64(1_000_000_000),
								"metric": map[string]any{
									"buckets": map[string]any{
										"1048576": map[string]any{"doc_count": float64(3)},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:  "histogram_over_time",
		Field:     "duration",
		Step:      time.Second,
		ByLabels:  []string{"service"},
		TimeRange: TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	assert.Equal(t, "gateway", out.Series[0].Labels["service"])
	assert.Equal(t, "0.001048576", out.Series[0].Labels["__bucket"])
}

// TestParseMetricsResponse_RateStaysSingleSeries guards against the fan-out
// changes leaking into scalar functions. Rate reads the bucket's native
// doc_count (no "metric" sub-aggregation) and divides by the step.
func TestParseMetricsResponse_RateStaysSingleSeries(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				map[string]any{"key": float64(1_000_000_000), "doc_count": float64(10)},
			},
		},
	})

	query := TraceMetricsQuery{
		Function:  "rate",
		Step:      10 * time.Second,
		TimeRange: TimeRange{Start: time.Unix(0, 1_000_000_000), End: time.Unix(0, 1_000_000_000)},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	assert.Empty(t, out.Series[0].Labels, "scalar functions carry no split label")
	assert.InDelta(t, 1.0, out.Series[0].Values[0].Value, 1e-9)
}
