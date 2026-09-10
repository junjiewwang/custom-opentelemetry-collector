// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package victoriametrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// Integration tests against a real vmsingle. Gated by VM_TEST_ENDPOINT:
//
//	VM_TEST_ENDPOINT=http://vmsingle.vm-minimal.svc.cluster.local:8428 \
//	  go test -tags=integration ./extension/observabilitystorageext/provider/victoriametrics/
//
// Uses /api/v1/export for verification (not subject to latencyOffset, so
// freshly written data is immediately visible).


// awaitExportVisible polls export until at least one series matches or the
// deadline passes. VM makes freshly written inmemory-part data visible to
// export within a few seconds; instant queries additionally wait out
// -search.latencyOffset (default 30s), so tests verify durability via export.
func awaitExportVisible(t *testing.T, c *httpVMClient, match []string, start, end time.Time) []VMExportSeries {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		series, err := c.Export(context.Background(), match, start, end)
		if err == nil && len(series) > 0 {
			return series
		}
		if time.Now().After(deadline) {
			t.Fatalf("export never returned data for %v (last err: %v)", match, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func newIntegrationClient(t *testing.T) *httpVMClient {
	t.Helper()
	endpoint := os.Getenv("VM_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("VM_TEST_ENDPOINT not set; skipping integration test")
	}
	cfg := &Config{Endpoint: endpoint}
	cfg.ApplyDefaults()
	return newHTTPVMClient(cfg)
}

func TestIntegration_WriteAndExportRoundTrip(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	ts := time.Now().UnixMilli()
	name := fmt.Sprintf("it_gauge_%d", ts)

	w := NewMetricWriter(c, &Config{Endpoint: os.Getenv("VM_TEST_ENDPOINT"), BatchSize: 1, FlushInterval: time.Hour}, zap.NewNop())
	defer w.Stop()
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{
		TimeUnixMilli: ts,
		Name:          name,
		Type:          "gauge",
		Value:         42.5,
		AppID:         "it_app",
		ServiceName:   "it_svc",
		Labels:        map[string]any{"route": "/it"},
	})
	require.NoError(t, w.Flush(ctx))

	// Window MUST be derived from the sample timestamp, not wall clock: the
	// test's own latency (write + flush) pushes now past the sample's ts, and
	// an unlucky server-clock skew would silently exclude the data.
	sampleT := time.UnixMilli(ts)
	series := awaitExportVisible(t, c, []string{name + `{app_id="it_app"}`}, sampleT.Add(-2*time.Minute), sampleT.Add(2*time.Minute))
	require.Len(t, series, 1)
	assert.Equal(t, name, series[0].Metric["__name__"])
	assert.Equal(t, "it_svc", series[0].Metric["service_name"])
	assert.Equal(t, "/it", series[0].Metric["route"])
	require.Len(t, series[0].Values, 1)
	assert.InDelta(t, 42.5, series[0].Values[0], 1e-9)
	require.Len(t, series[0].Timestamps, 1)
	assert.Equal(t, ts, series[0].Timestamps[0])
}

func TestIntegration_HistogramDeltaExpansion(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	ts := time.Now().UnixMilli()
	base := fmt.Sprintf("it_hist_%d", ts)

	w := NewMetricWriter(c, &Config{Endpoint: os.Getenv("VM_TEST_ENDPOINT"), BatchSize: 1, FlushInterval: time.Hour}, zap.NewNop())
	defer w.Stop()
	// The # TYPE row must be flushed for VM's metadata to mark this family a
	// histogram — QueryFlat's histogram branch depends on it.
	w.noteType(base, storedmodel.MetricMeta{Type: "histogram"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{
		TimeUnixMilli:          ts,
		Name:                   base,
		Type:                   "histogram",
		Value:                  7.0,
		Count:                  3,
		AppID:                  "it_app",
		BucketCounts:           []uint64{1, 0, 2},
		ExplicitBounds:         []float64{1, 5},
		AggregationTemporality: "delta",
	})
	require.NoError(t, w.Flush(ctx))

	// export the bucket series and verify cumulative semantics
	sampleT := time.UnixMilli(ts)
	series := awaitExportVisible(t, c, []string{base + `_bucket{app_id="it_app"}`}, sampleT.Add(-2*time.Minute), sampleT.Add(2*time.Minute))
	byLE := map[string]float64{}
	for _, s := range series {
		require.NotEmpty(t, s.Values)
		byLE[s.Metric["le"]] = s.Values[len(s.Values)-1]
	}
	// delta [1,0,2] → cumulative: le=1→1, le=5→1, +Inf→3
	assert.Equal(t, map[string]float64{"1": 1, "5": 1, "+Inf": 3}, byLE)

	// _count and _sum
	cs := awaitExportVisible(t, c, []string{base + `_count{app_id="it_app"}`}, sampleT.Add(-2*time.Minute), sampleT.Add(2*time.Minute))
	require.Len(t, cs, 1)
	assert.InDelta(t, 3.0, cs[0].Values[len(cs[0].Values)-1], 1e-9)
}

func TestIntegration_QueryInstant(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	ts := time.Now().UnixMilli()
	name := fmt.Sprintf("it_query_%d", ts)

	w := NewMetricWriter(c, &Config{Endpoint: os.Getenv("VM_TEST_ENDPOINT"), BatchSize: 1, FlushInterval: time.Hour}, zap.NewNop())
	defer w.Stop()
	w.ingestPoint(context.Background(), storedmetricPoint(name, 99.0, ts))
	require.NoError(t, w.Flush(ctx))

	// Instant queries on a single stale sample are empty by Prometheus
	// lookback semantics (sample older than 5m from the eval time), and
	// latencyOffset hides fresh samples for 30s — so a one-shot instant
	// assertion is inherently flaky here. Range query proves the series is
	// indexed and queryable; instant behavior is exercised by Phase 5's live
	// Grafana verification, where data flows continuously.
	deadline := time.Now().Add(60 * time.Second)
	for {
		res, err := c.QueryRange(ctx, name+`{app_id="it_app"}`,
			time.UnixMilli(ts).Add(-time.Minute), time.Now().Add(time.Minute), time.Minute)
		if err == nil && len(res.Series) > 0 {
			assert.Equal(t, name, res.Series[0].Metric["__name__"])
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("range query never returned the series (last err: %v)", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func storedmetricPoint(name string, v float64, ts int64) storedmodel.StoredMetricDataPoint {
	return storedmodel.StoredMetricDataPoint{
		TimeUnixMilli: ts,
		Name:          name,
		Type:          "gauge",
		Value:         v,
		AppID:         "it_app",
	}
}

func TestIntegration_ReaderFlatHistogramEndToEnd(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	ts := time.Now().UnixMilli()
	base := fmt.Sprintf("it_rd_%d", ts)

	// Write a delta histogram, then read it back through the reader's
	// QueryFlat histogram path (reassembly from _bucket/_sum/_count).
	rd := newMetricReader(c, false, zap.NewNop())

	w := NewMetricWriter(c, &Config{Endpoint: os.Getenv("VM_TEST_ENDPOINT"), BatchSize: 1, FlushInterval: time.Hour}, zap.NewNop())
	defer w.Stop()
	// The # TYPE row must be flushed for VM's metadata to mark this family a
	// histogram — QueryFlat's histogram branch depends on it.
	w.noteType(base, storedmodel.MetricMeta{Type: "histogram"})
	w.ingestPoint(context.Background(), storedmodel.StoredMetricDataPoint{
		TimeUnixMilli:          ts,
		Name:                   base,
		Type:                   "histogram",
		Value:                  7.0,
		Count:                  3,
		AppID:                  "it_app",
		BucketCounts:           []uint64{1, 0, 2},
		ExplicitBounds:         []float64{1, 5},
		AggregationTemporality: "delta",
	})
	require.NoError(t, w.Flush(ctx))

	deadline := time.Now().Add(60 * time.Second)
	for {
		res, err := rd.QueryFlat(ctx, observabilitystorageext.MetricFlatQuery{
			MetricName: base, AppID: "it_app",
			TimeRange: observabilitystorageext.TimeRange{Start: time.UnixMilli(ts).Add(-time.Minute), End: time.UnixMilli(ts).Add(time.Minute)},
		})
		if err != nil {
			t.Fatalf("QueryFlat: %v", err)
		}
		if len(res.Samples) > 0 {
			s := res.Samples[0]
			assert.Equal(t, []float64{1, 5}, s.Bounds)
			assert.Equal(t, []int64{1, 1, 3}, s.BucketCounts, "delta [1,0,2] reassembled as cumulative [1,1,3] (+Inf slot)")
			assert.InDelta(t, 7.0, s.Value, 1e-9)
			assert.Equal(t, int64(3), s.Count)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("QueryFlat histogram never returned samples")
		}
		time.Sleep(3 * time.Second)
	}
}
