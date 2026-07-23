// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/adminext/logql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// ==================== computeMetricInterval ====================

func TestComputeMetricInterval_RangeDuration(t *testing.T) {
	// Priority 1: RangeDuration from metric expression.
	got := computeMetricInterval(5*time.Minute, time.Time{}, time.Time{}, time.Time{})
	assert.Equal(t, int64(5*time.Minute), got)
}

func TestComputeMetricInterval_Step(t *testing.T) {
	// Priority 2: HTTP step parameter (when no RangeDuration).
	step := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	got := computeMetricInterval(0, step, time.Time{}, time.Time{})
	assert.Equal(t, int64(step.Sub(time.Unix(0, 0))), got)
}

func TestComputeMetricInterval_RangeDurationOverridesStep(t *testing.T) {
	// RangeDuration has highest priority — step is ignored.
	step := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	got := computeMetricInterval(10*time.Minute, step, time.Time{}, time.Time{})
	assert.Equal(t, int64(10*time.Minute), got)
}

func TestComputeMetricInterval_AutoCalculate(t *testing.T) {
	// Priority 3: auto-calculate from time range (target ~100 buckets).
	start := time.Unix(1784770000, 0)
	end := time.Unix(1784773600, 0) // 3600 seconds = 1 hour
	got := computeMetricInterval(0, time.Time{}, start, end)

	// 3600s / 100 = 36s per bucket → 36_000_000_000 nanos
	assert.Equal(t, int64(36_000_000_000), got)
}

func TestComputeMetricInterval_AutoCalculate_Min1Second(t *testing.T) {
	// Very short range: ensure minimum 1 second interval.
	start := time.Unix(1784770000, 0)
	end := time.Unix(1784770010, 0) // 10 seconds
	got := computeMetricInterval(0, time.Time{}, start, end)

	// 10s / 100 = 0.1s → should clamp to 1s
	assert.Equal(t, int64(1_000_000_000), got)
}

func TestComputeMetricInterval_ZeroRange(t *testing.T) {
	// Zero range → fallback to 5 minutes.
	got := computeMetricInterval(0, time.Time{}, time.Time{}, time.Time{})
	assert.Equal(t, int64(5*time.Minute), got)
}

// ==================== writeLokiMatrixResponse ====================

func TestWriteLokiMatrixResponse_Empty(t *testing.T) {
	// Empty result — should produce empty result array, not null.
	w := httptest.NewRecorder()
	result := &observabilitystorageext.LogMetricResult{Series: nil}
	writeLokiMatrixResponse(w, result)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp lokiMatrixResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "matrix", resp.Data.ResultType)
	assert.NotNil(t, resp.Data.Result)
	assert.Len(t, resp.Data.Result, 0)
}

func TestWriteLokiMatrixResponse_SingleSeries(t *testing.T) {
	w := httptest.NewRecorder()
	result := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{
				Labels: map[string]string{"level": "ERROR"},
				Values: []observabilitystorageext.LogMetricValue{
					{TimestampNano: 1784771400123456789, Value: 50},
					{TimestampNano: 1784771700987654321, Value: 35},
				},
			},
		},
	}
	writeLokiMatrixResponse(w, result)

	assert.Equal(t, 200, w.Code)

	var resp lokiMatrixResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Data.Result, 1)

	row := resp.Data.Result[0]
	assert.Equal(t, map[string]string{"level": "ERROR"}, row.Metric)
	require.Len(t, row.Values, 2)

	// Verify timestamp format: seconds.nanoseconds as JSON number.
	assert.Len(t, row.Values[0], 2)
	assert.Equal(t, "50", row.Values[0][1])

	// Check timestamp is a number with a decimal (json.Number).
	tsRaw, _ := json.Marshal(row.Values[0][0])
	assert.Contains(t, string(tsRaw), "1784771400.123")
}

func TestWriteLokiMatrixResponse_MultiSeries(t *testing.T) {
	w := httptest.NewRecorder()
	result := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{
				Labels: map[string]string{"level": "ERROR"},
				Values: []observabilitystorageext.LogMetricValue{
					{TimestampNano: 1784771400100000000, Value: 50},
				},
			},
			{
				Labels: map[string]string{"level": "INFO", "service_name": "order-service"},
				Values: []observabilitystorageext.LogMetricValue{
					{TimestampNano: 1784771400200000000, Value: 300},
				},
			},
		},
	}
	writeLokiMatrixResponse(w, result)

	var resp lokiMatrixResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Len(t, resp.Data.Result, 2)

	assert.Equal(t, map[string]string{"level": "ERROR"}, resp.Data.Result[0].Metric)
	assert.Equal(t, map[string]string{
		"level":        "INFO",
		"service_name": "order-service",
	}, resp.Data.Result[1].Metric)
}

func TestWriteLokiMatrixResponse_TimestampFormat(t *testing.T) {
	// Verify Loki/Prometheus convention: timestamp as seconds.nanoseconds.
	w := httptest.NewRecorder()
	result := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{
				Labels: map[string]string{},
				Values: []observabilitystorageext.LogMetricValue{
					{TimestampNano: 1784771400123456789, Value: 100},
				},
			},
		},
	}
	writeLokiMatrixResponse(w, result)

	// Read raw JSON to verify the timestamp is a number, not a string.
	raw := w.Body.Bytes()
	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]interface{} `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))

	// First value of first point should be a float64-like number.
	ts := parsed.Data.Result[0].Values[0][0]
	assert.IsType(t, float64(0), ts, "timestamp should be a JSON number, not string")
	assert.InDelta(t, 1784771400.123, ts, 0.001)
}

// ==================== isMetricQuery ====================

func TestIsMetricQuery_HealthCheck(t *testing.T) {
	assert.True(t, isMetricQuery("vector(1)+vector(1)"))
	assert.True(t, isMetricQuery("1+1"))
}

func TestIsMetricQuery_MetricQueries(t *testing.T) {
	assert.True(t, isMetricQuery("sum by (level) (count_over_time({}[5m]))"))
	assert.True(t, isMetricQuery("count_over_time({}[5m])"))
	assert.True(t, isMetricQuery("rate({}[1m])"))
}

func TestIsMetricQuery_LogQueries(t *testing.T) {
	assert.False(t, isMetricQuery(`{app="foo"} |= "error"`))
	assert.False(t, isMetricQuery(`{app="foo"}`))
	assert.False(t, isMetricQuery(""))
}

func TestIsMetricQuery_DelegatesToLogql(t *testing.T) {
	// Verify delegation: same results as logql.IsMetricQuery for non-health-check input.
	assert.Equal(t, logql.IsMetricQuery("rate({}[1m])"), isMetricQuery("rate({}[1m])"))
	assert.Equal(t, logql.IsMetricQuery(`{app="foo"}`), isMetricQuery(`{app="foo"}`))
}

// ==================== Value format in matrix response ====================

func TestWriteLokiMatrixResponse_ValueIsString(t *testing.T) {
	// Loki/Prometheus convention: value is a string representation of a number.
	w := httptest.NewRecorder()
	result := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{
				Labels: map[string]string{},
				Values: []observabilitystorageext.LogMetricValue{
					{TimestampNano: 1784771400000000000, Value: 1037},
				},
			},
		},
	}
	writeLokiMatrixResponse(w, result)

	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]interface{} `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&parsed))

	val := parsed.Data.Result[0].Values[0][1]
	assert.Equal(t, "1037", val, "value should be string representation")
}
