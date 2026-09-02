// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ── convertHistogram: temporality branches (spec-critical) ──────────────────

func TestConvertHistogram_DeltaAccumulates(t *testing.T) {
	pt := storedmodel.StoredMetricDataPoint{
		TimeUnixMilli:           1700000000000,
		Name:                    "http_duration",
		Type:                    "histogram",
		Value:                   12.5,
		Count:                   3,
		AppID:                   "app1",
		ServiceName:             "svc1",
		Labels:                  map[string]any{"route": "/api"},
		BucketCounts:            []uint64{1, 0, 2},
		ExplicitBounds:          []float64{1, 5},
		AggregationTemporality:  "delta",
	}
	lines := convertHistogram(pt, nil)

	byName := map[string]vmLine{}
	for _, l := range lines {
		byName[l.Metric["__name__"]+"|"+l.Metric["le"]] = l
	}
	// delta [1,0,2] → cumulative [1,1,3]
	require.Contains(t, byName, "http_duration_bucket|1")
	assert.Equal(t, 1.0, byName["http_duration_bucket|1"].Values[0])
	require.Contains(t, byName, "http_duration_bucket|5")
	assert.Equal(t, 1.0, byName["http_duration_bucket|5"].Values[0])
	require.Contains(t, byName, "http_duration_bucket|+Inf")
	assert.Equal(t, 3.0, byName["http_duration_bucket|+Inf"].Values[0])
	assert.Equal(t, 3.0, byName["http_duration_count|"].Values[0])
	assert.Equal(t, 12.5, byName["http_duration_sum|"].Values[0])
	// labels propagated
	assert.Equal(t, "app1", byName["http_duration_count|"].Metric["app_id"])
	assert.Equal(t, "svc1", byName["http_duration_count|"].Metric["service_name"])
	assert.Equal(t, "/api", byName["http_duration_count|"].Metric["route"])
}

func TestConvertHistogram_CumulativeAsIs(t *testing.T) {
	pt := storedmodel.StoredMetricDataPoint{
		TimeUnixMilli:          1700000000000,
		Name:                   "http_duration",
		Type:                   "histogram",
		Count:                  12,
		BucketCounts:           []uint64{10, 10, 12},
		ExplicitBounds:         []float64{1, 5},
		AggregationTemporality: "cumulative",
	}
	lines := convertHistogram(pt, nil)
	byLE := map[string]float64{}
	for _, l := range lines {
		if l.Metric["__name__"] == "http_duration_bucket" {
			byLE[l.Metric["le"]] = l.Values[0]
		}
	}
	// cumulative values written verbatim — NO re-accumulation (10+10=20 would be wrong)
	assert.Equal(t, map[string]float64{"1": 10, "5": 10, "+Inf": 12}, byLE)
}

// ── writer: JSON line format + same-series merge + error contract ───────────

// newTestWriter spins a MetricWriter against a recording httptest server.
func newTestWriter(t *testing.T, status int, cfg *Config) (*MetricWriter, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		buf.ReadFrom(r.Body)
		bodies = append(bodies, buf.Bytes())
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	if cfg == nil {
		cfg = &Config{Endpoint: srv.URL}
	} else {
		cfg = &Config{Endpoint: srv.URL, BatchSize: cfg.BatchSize, FlushInterval: cfg.FlushInterval, MaxRetries: cfg.MaxRetries, WriteTimeout: cfg.WriteTimeout}
	}
	cfg.ApplyDefaults()
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 2 * time.Second
	}
	w := NewMetricWriter(newHTTPVMClient(cfg), cfg, newTypeRegistry(""), zap.NewNop())
	t.Cleanup(w.Stop)
	return w, &bodies
}

func TestWriteMetrics_JSONLineFormat(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusNoContent, nil)

	pt := storedmodel.StoredMetricDataPoint{
		TimeUnixMilli: 1700000000000,
		Name:          "jvm_memory_used",
		Type:          "gauge",
		Value:         42,
		AppID:         "appA",
		ServiceName:   "svcA",
		Labels:        map[string]any{"pool": "heap"},
	}
	w.ingestPoint(pt, time.Now())
	require.NoError(t, w.Flush(context.Background()))

	require.Len(t, *bodies, 1)
	lines := strings.Split(strings.TrimSpace(string((*bodies)[0])), "\n")
	require.Len(t, lines, 1)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &parsed))
	metric := parsed["metric"].(map[string]any)
	assert.Equal(t, "jvm_memory_used", metric["__name__"])
	assert.Equal(t, "appA", metric["app_id"])
	assert.Equal(t, "svcA", metric["service_name"])
	assert.Equal(t, "heap", metric["pool"])
	assert.Equal(t, []any{42.0}, parsed["values"])
	assert.Equal(t, []any{1700000000000.0}, parsed["timestamps"])
}

func TestWriteMetrics_SameSeriesMergesOneLine(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusNoContent, nil)
	base := storedmodel.StoredMetricDataPoint{
		Name: "m", Type: "gauge", AppID: "a", ServiceName: "s",
		Labels: map[string]any{"k": "v"},
	}
	p1, p2, p3 := base, base, base
	p1.TimeUnixMilli, p1.Value = 1000, 1.0
	p2.TimeUnixMilli, p2.Value = 2000, 2.0
	p3.TimeUnixMilli, p3.Value = 3000, 3.0
	w.ingestPoint(p1, time.Now())
	w.ingestPoint(p2, time.Now())
	w.ingestPoint(p3, time.Now())
	require.NoError(t, w.Flush(context.Background()))

	lines := strings.Split(strings.TrimSpace(string((*bodies)[0])), "\n")
	require.Len(t, lines, 1, "same series must merge into one line")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &parsed))
	assert.Equal(t, []any{1.0, 2.0, 3.0}, parsed["values"])
	assert.Equal(t, []any{1000.0, 2000.0, 3000.0}, parsed["timestamps"])
}

func TestWriteMetrics_4xxFailsFastNoRetry(t *testing.T) {
	// 400 must NOT be retried: one request only.
	w, bodies := newTestWriter(t, http.StatusBadRequest, nil)
	w.ingestPoint(storedmodel.StoredMetricDataPoint{Name: "m", Type: "gauge", Value: 1, TimeUnixMilli: 1}, time.Now())
	err := w.Flush(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no retry")
	assert.Len(t, *bodies, 1, "4xx must not be retried")
}

func TestWriteMetrics_5xxRetriesThenFails(t *testing.T) {
	// 500 retries MaxRetries times then surfaces the error (drop contract).
	w, bodies := newTestWriter(t, http.StatusInternalServerError, &Config{MaxRetries: 2})
	w.ingestPoint(storedmodel.StoredMetricDataPoint{Name: "m", Type: "gauge", Value: 1, TimeUnixMilli: 1}, time.Now())
	err := w.Flush(context.Background())
	require.Error(t, err)
	assert.Len(t, *bodies, 3, "initial + 2 retries")
}

// ── metricsql builders ──────────────────────────────────────────────────────

func TestBuildSelector(t *testing.T) {
	s := buildSelector("m", "appA", "", map[string]string{"a": "1"}, map[string]string{"b": "x|y"}, nil, nil)
	assert.Equal(t, `m{app_id="appA",a="1",b=~"x|y"}`, s)
	// empty everything → bare name
	assert.Equal(t, "m", buildSelector("m", "", "", nil, nil, nil, nil))
	// escaping
	s2 := buildSelector("m", `a"b`, "", nil, nil, nil, nil)
	assert.Contains(t, s2, `app_id="a\"b"`)
}

func TestBuildRangeAggregation(t *testing.T) {
	assert.Equal(t, "m{app_id=\"a\"}", buildRangeAggregation("", "m{app_id=\"a\"}", nil))
	assert.Equal(t, "avg(m)", buildRangeAggregation("avg", "m", nil))
	assert.Equal(t, "sum by (a,b) (m)", buildRangeAggregation("sum", "m", []string{"b", "a"}))
}

func TestStripAppIDLabel(t *testing.T) {
	in := map[string]string{"app_id": "a", "x": "1"}
	out := stripAppIDLabel(in)
	assert.NotContains(t, out, "app_id")
	assert.Equal(t, "1", out["x"])
	// input not mutated
	assert.Equal(t, "a", in["app_id"])
	// no-op when absent
	same := map[string]string{"x": "1"}
	assert.Equal(t, same, stripAppIDLabel(same))
}

// ── type registry ───────────────────────────────────────────────────────────

func TestTypeRegistry_PersistAndReload(t *testing.T) {
	path := t.TempDir() + "/registry.json"
	r1 := newTypeRegistry(path)
	r1.record("counter_metric", "counter", "1", time.Now())
	r1.record("gauge_metric", "gauge", "By", time.Now())
	require.NoError(t, r1.flushToFile())

	r2 := newTypeRegistry(path)
	require.NoError(t, r2.loadFromFile())
	got, ok := r2.get("counter_metric")
	require.True(t, ok)
	assert.Equal(t, "counter", got.Type)
	assert.Equal(t, "1", got.Unit)
	_, ok = r2.get("nonexistent")
	assert.False(t, ok)
}

func TestTypeRegistry_SnapshotTimeFilter(t *testing.T) {
	r := newTypeRegistry("")
	old := time.Now().Add(-2 * time.Hour)
	r.record("stale_metric", "gauge", "", old)
	r.record("fresh_metric", "gauge", "", time.Now())
	snap := r.snapshot(time.Now().Add(-1*time.Hour), time.Time{})
	assert.NotContains(t, snap, "stale_metric")
	assert.Contains(t, snap, "fresh_metric")
}
