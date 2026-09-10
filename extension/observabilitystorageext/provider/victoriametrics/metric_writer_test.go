// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"bytes"
	"context"
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
		TimeUnixMilli:          1700000000000,
		Name:                   "http_duration",
		Type:                   "histogram",
		Value:                  12.5,
		Count:                  3,
		AppID:                  "app1",
		ServiceName:            "svc1",
		Labels:                 map[string]any{"route": "/api"},
		BucketCounts:           []uint64{1, 0, 2},
		ExplicitBounds:         []float64{1, 5},
		AggregationTemporality: "delta",
	}
	samples := convertHistogram(pt, nil)
	byKey := map[string]textSample{}
	for _, s := range samples {
		byKey[s.metric+"|"+s.labels["le"]] = s
	}
	// delta [1,0,2] → cumulative [1,1,3]
	require.Contains(t, byKey, "http_duration_bucket|1")
	assert.Equal(t, 1.0, byKey["http_duration_bucket|1"].value)
	require.Contains(t, byKey, "http_duration_bucket|5")
	assert.Equal(t, 1.0, byKey["http_duration_bucket|5"].value)
	require.Contains(t, byKey, "http_duration_bucket|+Inf")
	assert.Equal(t, 3.0, byKey["http_duration_bucket|+Inf"].value)
	assert.Equal(t, 3.0, byKey["http_duration_count|"].value)
	assert.Equal(t, 12.5, byKey["http_duration_sum|"].value)
	assert.Equal(t, "app1", byKey["http_duration_count|"].labels["app_id"])
	assert.Equal(t, "svc1", byKey["http_duration_count|"].labels["service_name"])
	assert.Equal(t, "/api", byKey["http_duration_count|"].labels["route"])
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
	samples := convertHistogram(pt, nil)
	byLE := map[string]float64{}
	for _, s := range samples {
		if s.metric == "http_duration_bucket" {
			byLE[s.labels["le"]] = s.value
		}
	}
	// cumulative written verbatim — NO re-accumulation
	assert.Equal(t, map[string]float64{"1": 10, "5": 10, "+Inf": 12}, byLE)
}

// ── writer: text format + type rows + error contract ───────────────────────

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
	w := NewMetricWriter(newHTTPVMClient(cfg), cfg, zap.NewNop())
	t.Cleanup(w.Stop)
	return w, &bodies
}

func TestWriteMetrics_TextFormat(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusNoContent, nil)
	w.noteType("jvm_memory_used", storedmodel.MetricMeta{Type: "gauge"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{
		TimeUnixMilli: 1700000000000,
		Name:          "jvm_memory_used",
		Type:          "gauge",
		Value:         42,
		AppID:         "appA",
		ServiceName:   "svcA",
		Labels:        map[string]any{"pool": "heap"},
	})
	require.NoError(t, w.Flush(context.Background()))

	require.Len(t, *bodies, 1)
	body := string((*bodies)[0])
	lines := strings.Split(strings.TrimSpace(body), "\n")
	require.Len(t, lines, 2, "one TYPE row + one sample row")
	assert.Equal(t, "# TYPE jvm_memory_used gauge", lines[0])
	assert.Equal(t, `jvm_memory_used{app_id="appA",pool="heap",service_name="svcA"} 42 1700000000000`, lines[1])
}

func TestWriteMetrics_TypeRowEmittedOnce(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusNoContent, nil)
	w.noteType("counter_m", storedmodel.MetricMeta{Type: "counter"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "counter_m", Type: "counter", Value: 1, TimeUnixMilli: 1000})
	require.NoError(t, w.Flush(context.Background()))
	// Second flush: same type → no repeated # TYPE row
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "counter_m", Type: "counter", Value: 2, TimeUnixMilli: 2000})
	require.NoError(t, w.Flush(context.Background()))

	first := string((*bodies)[0])
	second := string((*bodies)[1])
	assert.Contains(t, first, "# TYPE counter_m counter")
	assert.NotContains(t, second, "# TYPE", "type row must not repeat")
	assert.Contains(t, second, "counter_m 2 2000")
}

func TestWriteMetrics_4xxFailsFastNoRetry(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusBadRequest, nil)
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "m", Type: "gauge", Value: 1, TimeUnixMilli: 1})
	err := w.Flush(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no retry")
	assert.Len(t, *bodies, 1, "4xx must not be retried")
}

func TestWriteMetrics_5xxRetriesThenFails(t *testing.T) {
	w, bodies := newTestWriter(t, http.StatusInternalServerError, &Config{MaxRetries: 2})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "m", Type: "gauge", Value: 1, TimeUnixMilli: 1})
	err := w.Flush(context.Background())
	require.Error(t, err)
	assert.Len(t, *bodies, 3, "initial + 2 retries")
}

// ── helpers ─────────────────────────────────────────────────────────────────

func TestRenderSampleLine(t *testing.T) {
	s := textSample{metric: "m", labels: map[string]string{"b": "2", "a": "1"}, value: 1.5, timeUnixMi: 100}
	assert.Equal(t, `m{a="1",b="2"} 1.5 100`, renderSampleLine(s))
	// escaping
	s2 := textSample{metric: "m", labels: map[string]string{"path": "/x\"y"}, value: 1, timeUnixMi: 1}
	assert.Contains(t, renderSampleLine(s2), `/x\"y`)
}

func TestSanitizeName(t *testing.T) {
	assert.Equal(t, "jvm_memory_used", sanitizeName("jvm.memory.used"))
	assert.Equal(t, "a_b_c", sanitizeName("a-b.c"))
	assert.Equal(t, "plain", sanitizeName("plain"))
}

func TestWriteMetrics_TypeRowReEmittedAfterSentTypesReset(t *testing.T) {
	// The metadata refresh loop clears sentTypes so VM's (independently GC'd)
	// metricsmetadata table gets re-populated. Verify the writer re-emits a
	// # TYPE row after sentTypes is reset — the idempotent path the refresh
	// ticker drives.
	w, bodies := newTestWriter(t, http.StatusNoContent, nil)
	w.noteType("counter_m", storedmodel.MetricMeta{Type: "counter"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "counter_m", Type: "counter", Value: 1, TimeUnixMilli: 1000})
	require.NoError(t, w.Flush(context.Background()))
	require.Contains(t, string((*bodies)[0]), "# TYPE counter_m counter")

	// Simulate the refresh ticker: clear sentTypes.
	w.mu.Lock()
	w.sentTypes = make(map[string]storedmodel.MetricMeta)
	w.mu.Unlock()

	// Next write → noteType sees the name as new → re-emits # TYPE.
	w.noteType("counter_m", storedmodel.MetricMeta{Type: "counter"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{Name: "counter_m", Type: "counter", Value: 2, TimeUnixMilli: 2000})
	require.NoError(t, w.Flush(context.Background()))
	assert.Contains(t, string((*bodies)[1]), "# TYPE counter_m counter",
		"after sentTypes reset, # TYPE must be re-emitted")
}
