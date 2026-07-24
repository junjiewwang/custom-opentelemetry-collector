// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── log_reader: parseNestedAgg / parseHistogramLayer ─────────────────────

func newTestLogReader(s Searcher) *LogReader {
	return &LogReader{
		searcher: s,
		config:   &Config{Logs: IndexConfig{IndexPrefix: "otel-logs"}},
		logger:   zap.NewNop(),
	}
}

// TestParseMetricAggResult_NoGroupBy verifies the top-level (no group-by) path:
// the response carries {"over_time": {...}} and the parser must extract buckets,
// skipping leading empty buckets.
func TestParseMetricAggResult_NoGroupBy(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	resp := mustAggs(t, map[string]any{
		"over_time": map[string]any{
			"buckets": []any{
				map[string]any{"key": 1.7e18, "doc_count": float64(0)}, // leading empty → skipped
				map[string]any{"key": 1.70000005e18, "doc_count": float64(3)},
				map[string]any{"key": 1.7000001e18, "doc_count": float64(0)}, // trailing empty → kept
			},
		},
	})

	series := r.parseMetricAggResult(resp, nil)
	require.Len(t, series, 1)
	require.Len(t, series[0].Values, 2) // leading empty dropped, trailing kept
	assert.Equal(t, float64(3), series[0].Values[0].Value)
	assert.Equal(t, int64(1700000050000000000), series[0].Values[0].TimestampNano)
	// Empty trailing bucket still emitted with value 0.
	assert.Equal(t, float64(0), series[0].Values[1].Value)
}

// TestParseMetricAggResult_GroupedBy verifies nested terms → histogram parsing,
// including string bucket keys (keyword fields).
func TestParseMetricAggResult_GroupedBy(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	resp := mustAggs(t, map[string]any{
		"by_service": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": "svc-a",
					"over_time": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1000), "doc_count": float64(5)},
						},
					},
				},
				map[string]any{
					"key": "svc-b",
					"over_time": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1000), "doc_count": float64(2)},
						},
					},
				},
			},
		},
	})

	series := r.parseMetricAggResult(resp, []string{"service"})
	require.Len(t, series, 2)
	assert.Equal(t, map[string]string{"service": "svc-a"}, series[0].Labels)
	assert.Equal(t, float64(5), series[0].Values[0].Value)
	assert.Equal(t, map[string]string{"service": "svc-b"}, series[1].Labels)
}

// TestParseMetricAggResult_NumericBucketKey verifies that numeric (long) term
// bucket keys are converted to strings without quotes — a regression guard for
// the json.RawMessage key handling.
func TestParseMetricAggResult_NumericBucketKey(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	resp := mustAggs(t, map[string]any{
		"by_pid": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": float64(1234), // numeric long key
					"over_time": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(500), "doc_count": float64(1)},
						},
					},
				},
			},
		},
	})

	series := r.parseMetricAggResult(resp, []string{"pid"})
	require.Len(t, series, 1)
	// Numeric key must be rendered as a plain number string, not quoted JSON.
	assert.Equal(t, map[string]string{"pid": "1234"}, series[0].Labels)
}

// TestSearchLogMetric_EndToEnd wires the fake searcher into SearchLogMetric to
// confirm the aggregation is built and parsed through the public method, and
// that the correct index pattern is emitted for the given AppID.
func TestSearchLogMetric_EndToEnd(t *testing.T) {
	fake := &fakeSearcher{
		Responses: []any{
			map[string]any{
				"aggregations": map[string]any{
					"over_time": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1e18), "doc_count": float64(7)},
						},
					},
				},
			},
		},
	}
	r := newTestLogReader(fake)

	res, err := r.SearchLogMetric(t.Context(), LogMetricQuery{
		LogQuery:       LogQuery{AppID: "app-1"},
		GroupByLabels:  nil,
		IntervalNanos:  60_000_000_000,
	})
	require.NoError(t, err)
	require.Len(t, res.Series, 1)
	assert.Equal(t, float64(7), res.Series[0].Values[0].Value)
	assert.Equal(t, "otel-logs-app-1-*", fake.LastIndexPattern)
}

// ── trace_metrics: parseMetricsResponse / parseGroupedSeries ─────────────

func newTestTraceReader(s Searcher) *TraceReader {
	return &TraceReader{
		searcher: s,
		config:   &Config{Traces: IndexConfig{IndexPrefix: "otel-traces"}},
		logger:   zap.NewNop(),
	}
}

// TestParseMetricsResponse_SingleSeries verifies rate() extraction:
// value_count {"value": N} divided by step seconds.
func TestParseMetricsResponse_SingleSeries(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"buckets": map[string]any{
			"buckets": []any{
				map[string]any{"key": float64(1_000_000_000), "metric": map[string]any{"value": float64(10)}},
				map[string]any{"key": float64(16_000_000_000), "metric": map[string]any{"value": float64(20)}},
			},
		},
	})

	query := TraceMetricsQuery{
		Function: "rate",
		Step:     15 * time.Second,
		TimeRange: TimeRange{
			Start: time.Unix(0, 1_000_000_000),
			End:   time.Unix(0, 16_000_000_000),
		},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	require.Len(t, out.Series[0].Values, 2)
	// rate = value / step_seconds, rounded to 6 decimal places: 10/15 and 20/15.
	assert.InDelta(t, 10.0/15.0, out.Series[0].Values[0].Value, 1e-6)
	assert.InDelta(t, 20.0/15.0, out.Series[0].Values[1].Value, 1e-6)
	// Bucket key is nanoseconds → ms.
	assert.Equal(t, int64(1000), out.Series[0].Values[0].TimestampMs)
}

// TestParseMetricsResponse_GroupedSeries verifies walking the terms tree with
// numeric (float64) bucket keys rendered via fmt %v.
func TestParseMetricsResponse_GroupedSeries(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"by_service": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": "gateway",
					"buckets": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1_000_000_000), "metric": map[string]any{"value": float64(4)}},
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function: "rate",
		Step:     1 * time.Second,
		ByLabels: []string{"service"},
		TimeRange: TimeRange{
			Start: time.Unix(0, 1_000_000_000),
			End:   time.Unix(0, 1_000_000_000),
		},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	assert.Equal(t, map[string]string{"service": "gateway"}, out.Series[0].Labels)
	require.Len(t, out.Series[0].Values, 1)
	assert.InDelta(t, 4.0, out.Series[0].Values[0].Value, 1e-9)
}

// TestParseMetricsResponse_GroupedSeries_NumericKey verifies numeric group-by
// bucket keys (e.g. resource.process.pid) are stringified correctly.
func TestParseMetricsResponse_GroupedSeries_NumericKey(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"by_pid": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": float64(42),
					"buckets": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1_000_000_000), "metric": map[string]any{"value": float64(1)}},
						},
					},
				},
			},
		},
	})

	query := TraceMetricsQuery{
		Function: "rate",
		Step:     1 * time.Second,
		ByLabels: []string{"pid"},
		TimeRange: TimeRange{
			Start: time.Unix(0, 1_000_000_000),
			End:   time.Unix(0, 1_000_000_000),
		},
	}
	out, err := r.parseMetricsResponse(resp, query, "buckets")
	require.NoError(t, err)
	require.Len(t, out.Series, 1)
	assert.Equal(t, map[string]string{"pid": "42"}, out.Series[0].Labels)
}

// TestParseMetricsResponse_MissingBucketAgg verifies the error path when the
// expected bucket aggregation name is absent.
func TestParseMetricsResponse_MissingBucketAgg(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{"other": map[string]any{}})
	query := TraceMetricsQuery{Function: "rate", Step: time.Second,
		TimeRange: TimeRange{Start: time.Unix(0, 0), End: time.Unix(0, 0)}}
	_, err := r.parseMetricsResponse(resp, query, "buckets")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "buckets")
}

// ── metric_reader: parseSimpleResult / parseGroupedResult / NilValue ─────

func newTestMetricReader(s Searcher) *MetricReader {
	return &MetricReader{
		searcher: s,
		config:   &Config{Metrics: IndexConfig{IndexPrefix: "otel-metrics"}},
		logger:   zap.NewNop(),
	}
}

// TestParseSimpleResult_NilValueSentinel verifies that buckets whose agg_value
// is absent (empty bucket) are emitted with the NilValue sentinel so fill
// strategies can detect them.
func TestParseSimpleResult_NilValueSentinel(t *testing.T) {
	r := newTestMetricReader(&fakeSearcher{})
	aggFunc, err := GetAggregation("avg")
	require.NoError(t, err)

	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"time_series": map[string]any{
			"buckets": []any{
				map[string]any{"key": float64(1000), "agg_value": map[string]any{"value": float64(5)}},
				map[string]any{"key": float64(2000)}, // no agg_value → NilValue
			},
		},
	})

	out, err := r.parseSimpleResult(resp, aggFunc)
	require.NoError(t, err)
	require.Len(t, out.Data, 1)
	require.Len(t, out.Data[0].Values, 2)
	assert.Equal(t, float64(5), out.Data[0].Values[0].Value)
	assert.True(t, math.IsNaN(out.Data[0].Values[1].Value), "empty bucket must be NilValue")
}

// TestParseGroupedResult verifies composite + date_histogram parsing, label
// extraction from the composite key, and NilValue for empty sub-buckets.
func TestParseGroupedResult(t *testing.T) {
	r := newTestMetricReader(&fakeSearcher{})
	aggFunc, err := GetAggregation("sum")
	require.NoError(t, err)

	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"by_group": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": map[string]any{"host": "h1"},
					"time_series": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1000), "agg_value": map[string]any{"value": float64(9)}},
						},
					},
				},
				map[string]any{
					"key": map[string]any{"host": "h2"},
					"time_series": map[string]any{
						"buckets": []any{
							map[string]any{"key": float64(1000)}, // empty → NilValue
						},
					},
				},
			},
		},
	})

	out, err := r.parseGroupedResult(resp, aggFunc)
	require.NoError(t, err)
	require.Len(t, out.Data, 2)
	assert.Equal(t, map[string]string{"host": "h1"}, out.Data[0].Labels)
	assert.Equal(t, float64(9), out.Data[0].Values[0].Value)
	assert.Equal(t, map[string]string{"host": "h2"}, out.Data[1].Labels)
	assert.True(t, math.IsNaN(out.Data[1].Values[0].Value))
}

// TestParseGroupedResult_MissingAgg verifies a missing by_group agg yields an
// empty (not nil-deref panic) result.
func TestParseGroupedResult_MissingAgg(t *testing.T) {
	r := newTestMetricReader(&fakeSearcher{})
	aggFunc, err := GetAggregation("avg")
	require.NoError(t, err)
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{"other": map[string]any{}})
	out, err := r.parseGroupedResult(resp, aggFunc)
	require.NoError(t, err)
	assert.Empty(t, out.Data)
}

// ── trace_reader: parseTraceSummaryResult ─────────────────────────────────

// TestParseTraceSummaryResult verifies offset/limit slicing, root-span
// detection, and duration computation (start + duration, since endTime is not
// in the _source projection).
func TestParseTraceSummaryResult(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})

	span1 := mustJSON(t, map[string]any{
		"traceId": "t1", "spanId": "s1", "name": "GET /", "serviceName": "gateway",
		"startTimeUnixNano": int64(1_000_000_000), "durationNano": int64(500_000_000),
	})
	span2 := mustJSON(t, map[string]any{
		"traceId": "t1", "spanId": "s2", "parentSpanId": "s1", "name": "db", "serviceName": "db",
		"startTimeUnixNano": int64(1_100_000_000), "durationNano": int64(200_000_000),
	})
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, map[string]any{
		"traces": map[string]any{
			"buckets": []any{
				map[string]any{
					"key": "t1",
					"root_span": map[string]any{
						"hits": map[string]any{
							"hits": []any{
								map[string]any{"_id": "s1", "_source": span1},
								map[string]any{"_id": "s2", "_source": span2},
							},
						},
					},
				},
			},
		},
		"total_traces": map[string]any{"value": int64(1)},
	})

	out, err := r.parseTraceSummaryResult(resp, 0, 20)
	require.NoError(t, err)
	require.Len(t, out.Summaries, 1)
	ts := out.Summaries[0]
	assert.Equal(t, "t1", ts.TraceID)
	assert.Equal(t, int64(2), ts.SpanCount)
	// Root span = the one without parentSpanId.
	assert.Equal(t, "gateway", ts.RootServiceName)
	assert.Equal(t, "GET /", ts.RootSpanName)
	// Duration = (maxEnd - minStart)/ms. maxEnd = max(start+dur):
	// span1 end = 1.0e9 + 0.5e9 = 1.5e9; span2 end = 1.1e9 + 0.2e9 = 1.3e9.
	// minStart = 1.0e9 → 500ms.
	assert.Equal(t, int64(500), ts.DurationMs)
	assert.Equal(t, int64(1), out.Total)
}

// TestParseTraceSummaryResult_OffsetBeyondResults verifies offset slicing
// returns no summaries (but preserves total) without panicking.
func TestParseTraceSummaryResult_OffsetBeyondResults(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	aggs := map[string]any{
		"traces": map[string]any{
			"buckets": []any{
				map[string]any{
					"key":       "t1",
					"root_span": map[string]any{"hits": map[string]any{"hits": []any{}}},
				},
			},
		},
		"total_traces": map[string]any{"value": int64(5)},
	}
	resp := &SearchResponse{}
	resp.Aggregations = mustAggs(t, aggs)
	out, err := r.parseTraceSummaryResult(resp, 10, 20)
	require.NoError(t, err)
	assert.Empty(t, out.Summaries)
	assert.Equal(t, int64(5), out.Total)
}

// ── log_reader: hitsToLogRecords + compatLogRecord ───────────────────────

// TestHitsToLogRecords_NewFormat verifies the canonical field mapping and ID
// propagation from the hit.
func TestHitsToLogRecords_NewFormat(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	src := mustJSON(t, map[string]any{
		"timeUnixNano":   int64(1_700_000_000_000_000_000),
		"severityText":   "ERROR",
		"severityNumber": int32(17),
		"body":           "boom",
		"serviceName":    "svc",
		"appId":          "app-1",
		"traceId":        "tid",
		"spanId":         "sid",
	})
	hit := SearchHit{ID: "doc-1", Source: src}
	logs, err := r.hitsToLogRecords([]SearchHit{hit})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	l := logs[0]
	assert.Equal(t, "doc-1", l.ID)
	assert.Equal(t, "ERROR", l.Severity)
	assert.Equal(t, int32(17), l.SeverityNumber)
	assert.Equal(t, "boom", l.Body)
	assert.Equal(t, "svc", l.ServiceName)
	assert.Equal(t, "app-1", l.AppID)
	assert.Equal(t, time.Unix(0, 1_700_000_000_000_000_000), l.Timestamp)
}

// TestHitsToLogRecords_LegacySeverity verifies the compat path: old documents
// store severity under the legacy "severity" key and lack "severityText".
func TestHitsToLogRecords_LegacySeverity(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	src := mustJSON(t, map[string]any{
		"timeUnixNano": int64(0),
		"severity":     "WARN", // legacy field
		"serviceName":  "svc",
	})
	hit := SearchHit{ID: "doc-2", Source: src}
	logs, err := r.hitsToLogRecords([]SearchHit{hit})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, "WARN", logs[0].Severity, "compatLogRecord should backfill SeverityText from legacy severity")
}

// TestHitsToLogRecords_BadSource verifies a document that fails to unmarshal
// is skipped (warned), not fatal.
func TestHitsToLogRecords_BadSource(t *testing.T) {
	r := newTestLogReader(&fakeSearcher{})
	hits := []SearchHit{
		{ID: "bad", Source: json.RawMessage(`{not json`)},
		{ID: "good", Source: mustJSON(t, map[string]any{"timeUnixNano": int64(0), "serviceName": "svc"})},
	}
	logs, err := r.hitsToLogRecords(hits)
	require.NoError(t, err)
	require.Len(t, logs, 1, "bad doc skipped, good doc kept")
	assert.Equal(t, "good", logs[0].ID)
}

// ── helpers ──────────────────────────────────────────────────────────────

// mustAggs marshals a map to JSON and re-decodes it into
// map[string]json.RawMessage, mirroring how ES aggregation bodies arrive.
func mustAggs(t *testing.T, m map[string]any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
