// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==================== copyMap ====================

func TestCopyMap_Nil(t *testing.T) {
	dst := copyMap(nil)
	assert.NotNil(t, dst)
	assert.Empty(t, dst)
}

func TestCopyMap_Empty(t *testing.T) {
	dst := copyMap(map[string]string{})
	assert.Empty(t, dst)
}

func TestCopyMap_Copies(t *testing.T) {
	src := map[string]string{"level": "ERROR", "service_name": "order-svc"}
	dst := copyMap(src)

	assert.Equal(t, src, dst)
	assert.NotSame(t, &src, &dst, "should be independent copy")

	// Mutating dst should not affect src.
	dst["level"] = "INFO"
	assert.Equal(t, "ERROR", src["level"])
}

// ==================== parseHistogramLayer ====================

func TestParseHistogramLayer_Basic(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [
				{"key": 1784771400000000000, "doc_count": 50},
				{"key": 1784771700000000000, "doc_count": 35},
				{"key": 1784772000000000000, "doc_count": 0}
			]
		}`),
	}
	labels := map[string]string{"level": "ERROR"}

	series := r.parseHistogramLayer(raw, labels)
	require.Len(t, series, 1)

	assert.Equal(t, map[string]string{"level": "ERROR"}, series[0].Labels)
	require.Len(t, series[0].Values, 3) // third bucket is trailing zero, included

	assert.Equal(t, int64(1784771400000000000), series[0].Values[0].TimestampNano)
	assert.Equal(t, float64(50), series[0].Values[0].Value)
	assert.Equal(t, float64(35), series[0].Values[1].Value)
	assert.Equal(t, float64(0), series[0].Values[2].Value)
}

func TestParseHistogramLayer_LeadingEmptyBucketsSkipped(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [
				{"key": 1784771400000000000, "doc_count": 0},
				{"key": 1784771700000000000, "doc_count": 0},
				{"key": 1784772000000000000, "doc_count": 10}
			]
		}`),
	}
	series := r.parseHistogramLayer(raw, nil)
	require.Len(t, series, 1)
	require.Len(t, series[0].Values, 1) // first two empty buckets skipped
	assert.Equal(t, float64(10), series[0].Values[0].Value)
}

func TestParseHistogramLayer_AllEmpty(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [
				{"key": 1784771400000000000, "doc_count": 0},
				{"key": 1784771700000000000, "doc_count": 0}
			]
		}`),
	}
	series := r.parseHistogramLayer(raw, nil)
	assert.Empty(t, series)
}

func TestParseHistogramLayer_MissingKey(t *testing.T) {
	r := &LogReader{}
	series := r.parseHistogramLayer(map[string]json.RawMessage{}, nil)
	assert.Empty(t, series)
}

func TestParseHistogramLayer_InvalidJSON(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{invalid}`),
	}
	series := r.parseHistogramLayer(raw, nil)
	assert.Empty(t, series)
}

func TestParseHistogramLayer_PreservesInputMap(t *testing.T) {
	// parseHistogramLayer should copy labels, not mutate the input.
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [{"key": 1784771400000000000, "doc_count": 10}]
		}`),
	}
	original := map[string]string{"level": "ERROR"}
	originalCopy := copyMap(original)

	series := r.parseHistogramLayer(raw, original)
	require.Len(t, series, 1)
	assert.Equal(t, originalCopy, original, "input labels should not be mutated")
}

// ==================== parseMetricAggResult / parseNestedAgg ====================

func TestParseMetricAggResult_NoGrouping(t *testing.T) {
	// No group-by labels → only histogram layer.
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [
				{"key": 1784771400000000000, "doc_count": 100}
			]
		}`),
	}
	series := r.parseMetricAggResult(raw, nil)
	require.Len(t, series, 1)
	assert.Empty(t, series[0].Labels)
	assert.Equal(t, float64(100), series[0].Values[0].Value)
}

func TestParseMetricAggResult_OneGroupBy(t *testing.T) {
	// One terms aggregation + histogram.
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"by_level": json.RawMessage(`{
			"buckets": [
				{
					"key": "ERROR",
					"over_time": {
						"buckets": [
							{"key": 1784771400000000000, "doc_count": 50}
						]
					}
				},
				{
					"key": "INFO",
					"over_time": {
						"buckets": [
							{"key": 1784771400000000000, "doc_count": 200}
						]
					}
				}
			]
		}`),
	}
	series := r.parseMetricAggResult(raw, []string{"level"})
	require.Len(t, series, 2)

	assert.Equal(t, map[string]string{"level": "ERROR"}, series[0].Labels)
	assert.Equal(t, float64(50), series[0].Values[0].Value)

	assert.Equal(t, map[string]string{"level": "INFO"}, series[1].Labels)
	assert.Equal(t, float64(200), series[1].Values[0].Value)
}

func TestParseMetricAggResult_TwoGroupBy(t *testing.T) {
	// Nested terms: service_name → level → histogram.
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"by_service_name": json.RawMessage(`{
			"buckets": [
				{
					"key": "order-service",
					"by_level": {
						"buckets": [
							{
								"key": "ERROR",
								"over_time": {
									"buckets": [
										{"key": 1784771400000000000, "doc_count": 10}
									]
								}
							},
							{
								"key": "INFO",
								"over_time": {
									"buckets": [
										{"key": 1784771400000000000, "doc_count": 100}
									]
								}
							}
						]
					}
				},
				{
					"key": "user-service",
					"by_level": {
						"buckets": [
							{
								"key": "ERROR",
								"over_time": {
									"buckets": [
										{"key": 1784771400000000000, "doc_count": 5}
									]
								}
							}
						]
					}
				}
			]
		}`),
	}
	series := r.parseMetricAggResult(raw, []string{"service_name", "level"})
	require.Len(t, series, 3)

	assert.Equal(t, map[string]string{"service_name": "order-service", "level": "ERROR"}, series[0].Labels)
	assert.Equal(t, float64(10), series[0].Values[0].Value)

	assert.Equal(t, map[string]string{"service_name": "order-service", "level": "INFO"}, series[1].Labels)
	assert.Equal(t, float64(100), series[1].Values[0].Value)

	assert.Equal(t, map[string]string{"service_name": "user-service", "level": "ERROR"}, series[2].Labels)
	assert.Equal(t, float64(5), series[2].Values[0].Value)
}

func TestParseMetricAggResult_MissingAggKey(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{}
	// Missing "by_level" key — should return nil.
	series := r.parseMetricAggResult(raw, []string{"level"})
	assert.Empty(t, series)
}

func TestParseMetricAggResult_InvalidTermsJSON(t *testing.T) {
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"by_level": json.RawMessage(`{invalid}`),
	}
	series := r.parseMetricAggResult(raw, []string{"level"})
	assert.Empty(t, series)
}

func TestParseMetricAggResult_EmptyGroupBy(t *testing.T) {
	// Empty group-by list: same as no grouping — should parse histogram directly.
	r := &LogReader{}
	raw := map[string]json.RawMessage{
		"over_time": json.RawMessage(`{
			"buckets": [{"key": 1784771400000000000, "doc_count": 42}]
		}`),
	}
	series := r.parseMetricAggResult(raw, []string{})
	require.Len(t, series, 1)
	assert.Equal(t, float64(42), series[0].Values[0].Value)
}
