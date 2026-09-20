// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// newFakeVM spins an httptest server emulating the VM endpoints the reader
// uses, driven by a router func. It records every request path+query.
func newFakeVM(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (VMClient, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if handle != nil {
			handle(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &Config{Endpoint: srv.URL}
	cfg.ApplyDefaults()
	return newHTTPVMClient(cfg), &calls
}

func newTestReader(c VMClient) *MetricReader {
	return newMetricReader(c, false, zap.NewNop())
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

// ── Query (instant) ─────────────────────────────────────────────────────────

func TestQuery_AppIsolationAndLabelStrip(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"m","app_id":"appA","k":"v"},"value":[1780000000,"42"]}]}}`)
	})
	rd := newTestReader(c)
	res, err := rd.Query(context.Background(), observabilitystorageext.MetricQuery{
		MetricName: "m", AppID: "appA",
		Time: time.Unix(1780000000, 0),
	})
	require.NoError(t, err)
	require.Len(t, res.Data, 1)
	assert.InDelta(t, 42.0, res.Data[0].Value, 1e-9)
	assert.Equal(t, "1780000000000", res.Data[0].TimeUnixMilli)
	// app_id stripped from returned labels
	assert.NotContains(t, res.Data[0].Labels, "app_id")
	assert.Equal(t, "v", res.Data[0].Labels["k"])
	// selector carried the app filter to VM
	require.Len(t, *calls, 1)
	assert.Contains(t, (*calls)[0], "query=m%7Bapp_id%3D%22appA%22%7D")
}

// ── QueryRange ───────────────────────────────────────────────────────────────

func TestQueryRange_GroupByTranslation(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"m","app_id":"a","svc":"x"},"values":[[1780000000,"1"],[1780000060,"2"]]}]}}`)
	})
	rd := newTestReader(c)
	res, err := rd.QueryRange(context.Background(), observabilitystorageext.MetricRangeQuery{
		MetricName: "m", AppID: "a",
		Aggregation: "sum", GroupBy: []string{"svc"},
		TimeRange: observabilitystorageext.TimeRange{Start: time.Unix(1780000000, 0), End: time.Unix(1780000100, 0)},
		Step:      time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, res.Data, 1)
	require.Len(t, res.Data[0].Values, 2)
	assert.Equal(t, "1780000000000", res.Data[0].Values[0].TimeUnixMilli)
	require.Len(t, *calls, 1)
	assert.Contains(t, (*calls)[0], "query=sum+by+%28svc%29+%28m%7Bapp_id%3D%22a%22%7D%29")
}

// ── QueryFlat ────────────────────────────────────────────────────────────────

func TestQueryFlat_GaugeViaExport(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/export") {
			fmt.Fprint(w, `{"metric":{"__name__":"m","app_id":"a","k":"v"},"values":[1.0,2.0],"timestamps":[1780000000000,1780000060000]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	rd := newTestReader(c)
	res, err := rd.QueryFlat(context.Background(), observabilitystorageext.MetricFlatQuery{
		MetricName: "m", AppID: "a",
		TimeRange: observabilitystorageext.TimeRange{Start: time.Unix(1780000000, 0), End: time.Unix(1780000100, 0)},
	})
	require.NoError(t, err)
	require.Len(t, res.Samples, 2)
	assert.Equal(t, int64(1780000000000), res.Samples[0].TimestampMs)
	assert.NotContains(t, res.Samples[0].Labels, "app_id")
	assert.False(t, res.Truncated)
	// one metadata probe (histogram check) + one export
	require.Len(t, *calls, 2)
	assert.Contains(t, (*calls)[0], "/api/v1/metadata?metric=m")
	assert.Contains(t, (*calls)[1], "match%5B%5D=m%7Bapp_id%3D%22a%22%7D")
}

func TestQueryFlat_HistogramReassembly(t *testing.T) {
	// VM metadata says "http_dur" is a histogram → flat query must hit
	// _bucket/_sum/_count sub-series and reassemble BucketCounts/Bounds.
	var exportCalls []string
	c, _ := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/metadata") {
			writeJSON(w, `{"status":"success","data":{"http_dur":[{"type":"histogram"}]}}`)
			return
		}
		q := r.URL.Query().Get("match[]")
		exportCalls = append(exportCalls, q)
		switch {
		case strings.Contains(q, "http_dur_bucket"):
			fmt.Fprint(w,
				`{"metric":{"__name__":"http_dur_bucket","app_id":"a","le":"1"},"values":[10.0],"timestamps":[1780000000000]}`+"\n"+
					`{"metric":{"__name__":"http_dur_bucket","app_id":"a","le":"5"},"values":[12.0],"timestamps":[1780000000000]}`+"\n"+
					`{"metric":{"__name__":"http_dur_bucket","app_id":"a","le":"+Inf"},"values":[12.0],"timestamps":[1780000000000]}`)
		case strings.Contains(q, "http_dur_sum"):
			fmt.Fprint(w, `{"metric":{"__name__":"http_dur_sum","app_id":"a"},"values":[3.5],"timestamps":[1780000000000]}`)
		case strings.Contains(q, "http_dur_count"):
			fmt.Fprint(w, `{"metric":{"__name__":"http_dur_count","app_id":"a"},"values":[12.0],"timestamps":[1780000000000]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	rd := newTestReader(c)
	res, err := rd.QueryFlat(context.Background(), observabilitystorageext.MetricFlatQuery{
		MetricName: "http_dur", AppID: "a",
		TimeRange: observabilitystorageext.TimeRange{Start: time.Unix(1780000000, 0), End: time.Unix(1780000100, 0)},
	})
	require.NoError(t, err)
	require.Len(t, res.Samples, 1)
	s := res.Samples[0]
	assert.Equal(t, []float64{1, 5}, s.Bounds)
	assert.Equal(t, []int64{10, 12}, s.BucketCounts[:2])
	assert.Equal(t, 3.5, s.Value)
	assert.Equal(t, int64(12), s.Count)
	assert.Equal(t, "cumulative", s.Temporality)
	require.Len(t, exportCalls, 3)
}

// ── ListMetricNames (vm_* filtering) ────────────────────────────────────────

func TestMetricReader_DeclaresUnderscoreNameScheme(t *testing.T) {
	// VM ingests via the Prometheus text format (writer sanitizes dots to
	// underscores), so the reader must declare UsesDottedMetricNames()=false —
	// otherwise adminext reverse-maps jvm_memory_used → jvm.memory.used and
	// queries a dotted name VM never stored (0 series).
	c, _ := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":["jvm_memory_used"]}`)
	})
	rd := newTestReader(c)
	scheme, ok := any(rd).(observabilitystorageext.MetricNameScheme)
	require.True(t, ok, "VM MetricReader must implement MetricNameScheme")
	assert.False(t, scheme.UsesDottedMetricNames(), "VM stores underscored names verbatim")
}

func TestListMetricNames_FiltersVMPrefix(t *testing.T) {
	c, _ := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":["vm_rows","vmagent_rows","my_metric","another"]}`)
	})
	rd := newTestReader(c)
	names, err := rd.ListMetricNames(context.Background(), observabilitystorageext.TimeRange{
		Start: time.Now().Add(-time.Hour), End: time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"another", "my_metric"}, names)
}

// ── ListMetricTypes (VM native metadata + histogram expansion) ──────────────

func TestListMetricTypes_FromVMMetadata(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":{
			"vm_rows":[{"type":"counter"}],
			"counter_m":[{"type":"counter"}],
			"jvm_memory_used":[{"type":"gauge"}],
			"http_dur":[{"type":"histogram"}]}}`)
	})
	rd := newTestReader(c)
	types, err := rd.ListMetricTypes(context.Background(), observabilitystorageext.TimeRange{})
	require.NoError(t, err)
	assert.Equal(t, "counter", types["counter_m"].Type)
	assert.Equal(t, "gauge", types["jvm_memory_used"].Type)
	// vm_* filtered
	_, ok := types["vm_rows"]
	assert.False(t, ok, "vm_* self-monitoring must not surface")
	// histogram sub-series expand to counter
	assert.Equal(t, "counter", types["http_dur_bucket"].Type)
	assert.Equal(t, "counter", types["http_dur_sum"].Type)
	assert.Equal(t, "counter", types["http_dur_count"].Type)
	// queried VM, not a local registry
	require.Len(t, *calls, 1)
	assert.Contains(t, (*calls)[0], "/api/v1/metadata")
}

// ── ListLabelValuesForMetric / app_id isolation ─────────────────────────────

func TestListLabelValuesForMetric_MatchScoped(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":["GET","POST"]}`)
	})
	rd := newTestReader(c)
	values, err := rd.ListLabelValuesForMetric(context.Background(), "http_method", "http_requests", observabilitystorageext.TimeRange{
		Start: time.Now().Add(-time.Hour), End: time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"GET", "POST"}, values)
	require.Len(t, *calls, 1)
	assert.Contains(t, (*calls)[0], "match%5B%5D=http_requests")
}

func TestListLabelValues_AppIDNeverSurfaced(t *testing.T) {
	c, _ := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":["appA","appB"]}`)
	})
	rd := newTestReader(c)
	values, err := rd.ListLabelValues(context.Background(), "app_id", observabilitystorageext.TimeRange{
		Start: time.Now().Add(-time.Hour), End: time.Now(),
	})
	require.NoError(t, err)
	assert.Empty(t, values, "app_id is internal isolation; must not leak")
}

// ── ListLabelCombinations ───────────────────────────────────────────────────

func TestListLabelCombinations(t *testing.T) {
	c, _ := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":[
			{"__name__":"m","a":"1","b":"x"},
			{"__name__":"m","a":"1","b":"x"},
			{"__name__":"m","a":"2","b":"y"}]}`)
	})
	rd := newTestReader(c)
	res, err := rd.ListLabelCombinations(context.Background(), observabilitystorageext.LabelCombinationsQuery{
		MetricName: "m", LabelKeys: []string{"a", "b"},
	})
	require.NoError(t, err)
	require.Len(t, res.Combinations, 2, "duplicates deduped")
	assert.Equal(t, map[string]string{"a": "1", "b": "x"}, res.Combinations[0])
}

func TestBuildHeatmapRangeExpr(t *testing.T) {
	got := buildHeatmapRangeExpr("traces_spanmetrics_latency", "appX", "", []string{"service_name"}, "5m")
	assert.Contains(t, got, "sum by (")
	assert.Contains(t, got, "le")
	assert.Contains(t, got, "service_name")
	assert.Contains(t, got, "rate(traces_spanmetrics_latency_bucket{app_id=\"appX\"}[5m])")
}

func TestQueryHeatmapRange_NativeDelegation(t *testing.T) {
	c, calls := newFakeVM(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"le":"10"},"values":[[1788437880,"0.46"],[1788437940,"0.47"]]},
			{"metric":{"le":"+Inf"},"values":[[1788437880,"1.0"]]}
		]}}`)
	})
	rd := newTestReader(c)
	res, err := rd.QueryHeatmapRange(context.Background(), observabilitystorageext.MetricHeatmapRangeQuery{
		MetricName:    "traces_spanmetrics_latency",
		AppID:         "appX",
		TimeRange:     observabilitystorageext.TimeRange{Start: time.Unix(1788434000, 0), End: time.Unix(1788441000, 0)},
		Step:          60 * time.Second,
		RangeDuration: 5 * time.Minute,
		GroupBy:       []string{"service_name"},
	})
	require.NoError(t, err)
	require.Len(t, res.Data, 2)
	// The emitted request must be the native MetricsQL heatmap, not a bucket export.
	require.Len(t, *calls, 1)
	assert.Contains(t, (*calls)[0], "query_range")
	assert.Contains(t, (*calls)[0], "sum+by+%28le%2Cservice_name%29+%28rate%28traces_spanmetrics_latency_bucket")
	assert.Equal(t, "le", func() string {
		for k := range res.Data[0].Labels {
			if k == "le" {
				return k
			}
		}
		return ""
	}())
}
