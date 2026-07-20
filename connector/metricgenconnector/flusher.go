// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// metricFlusher periodically collects aggregated metrics from generators
// and exports them via the metrics consumer.
type metricFlusher struct {
	interval time.Duration
	consumer consumer.Metrics
	redGen   *REDGenerator
	sgGen    *ServiceGraphGenerator
	logger   *zap.Logger
}

// run is the background flush loop.
func (f *metricFlusher) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.flush()
		}
	}
}

// flush collects and exports all aggregated metrics.
func (f *metricFlusher) flush() {
	md := pmetric.NewMetrics()
	count := 0

	// RED metrics.
	if f.redGen != nil {
		if redSeries := f.redGen.Collect(); len(redSeries) > 0 {
			count += f.buildREDMetrics(md, redSeries)
		}
	}

	// ServiceGraph metrics.
	if f.sgGen != nil {
		if sgEdges := f.sgGen.Collect(); len(sgEdges) > 0 {
			count += f.buildSGMetrics(md, sgEdges)
		}
	}

	if count == 0 {
		return
	}

	ctx := context.Background()
	if err := f.consumer.ConsumeMetrics(ctx, md); err != nil {
		f.logger.Error("failed to export metrics", zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// pmetric builders
// ---------------------------------------------------------------------------

const (
	metricNameREDCallsTotal = "traces_spanmetrics_calls_total"
	metricNameREDLatency    = "traces_spanmetrics_latency"
	namespaceRED            = ""
)

// buildREDMetrics converts RED series into pmetric.Metrics.
func (f *metricFlusher) buildREDMetrics(md pmetric.Metrics, series []*redMetricSeries) int {
	now := pcommon.NewTimestampFromTime(time.Now())

	for _, s := range series {
		appID := s.appID
		// --- calls_total counter ---
		if calls := s.calls.Swap(); calls > 0 {
			rm := md.ResourceMetrics().AppendEmpty()
			setResourceAttr(rm.Resource(), appID)
			sm := rm.ScopeMetrics().AppendEmpty()
			m := sm.Metrics().AppendEmpty()
			m.SetName(metricNameREDCallsTotal)
			sum := m.SetEmptySum()
			sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			sum.SetIsMonotonic(true)
			dp := sum.DataPoints().AppendEmpty()
			dp.SetStartTimestamp(now)
			dp.SetTimestamp(now)
			dp.SetIntValue(calls)
			setLabels(dp.Attributes(), s.dims)
		}

		// --- latency histogram ---
		buckets, bounds, sumMicros, count := s.latency.Snapshot()
		if count > 0 {
			rm := md.ResourceMetrics().AppendEmpty()
			setResourceAttr(rm.Resource(), appID)
			sm := rm.ScopeMetrics().AppendEmpty()
			m := sm.Metrics().AppendEmpty()
			m.SetName(metricNameREDLatency)
			h := m.SetEmptyHistogram()
			h.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			dp := h.DataPoints().AppendEmpty()
			dp.SetStartTimestamp(now)
			dp.SetTimestamp(now)
			dp.SetCount(count)
			dp.SetSum(float64(sumMicros) / 1e6)
			dp.ExplicitBounds().FromRaw(bounds)
			dp.BucketCounts().FromRaw(buckets)
			setLabels(dp.Attributes(), s.dims)
		}
	}

	return len(series)
}

// ---------------------------------------------------------------------------
// ServiceGraph metric builders
// ---------------------------------------------------------------------------

const (
	metricNameSGRequestTotal       = "traces_service_graph_request_total"
	metricNameSGFailedTotal        = "traces_service_graph_request_failed_total"
	metricNameSGClientSeconds      = "traces_service_graph_request_client_seconds"
	metricNameSGServerSeconds      = "traces_service_graph_request_server_seconds"
	metricNameSGMessagingSeconds   = "traces_service_graph_request_messaging_system_seconds"
	metricNameSGMessageSize        = "traces_service_graph_request_message_size_bytes"
)

// buildSGMetrics converts ServiceGraph edges into pmetric.Metrics.
func (f *metricFlusher) buildSGMetrics(md pmetric.Metrics, edges []*sgEdgeSeries) int {
	now := pcommon.NewTimestampFromTime(time.Now())

	for _, e := range edges {
		appID := e.appID
		labels := map[string]string{
			"client":          e.key.client,
			"server":          e.key.server,
			"connection_type": e.key.connType,
		}

		// --- request_total counter ---
		if calls := e.requestTotal.Swap(); calls > 0 {
			f.emitCounter(md, metricNameSGRequestTotal, calls, labels, appID, now)
		}

		// --- failed_total counter ---
		if failed := e.failedTotal.Swap(); failed > 0 {
			f.emitCounter(md, metricNameSGFailedTotal, failed, labels, appID, now)
		}

		// --- client_seconds histogram ---
		f.emitHistogramIfNonEmpty(md, metricNameSGClientSeconds, e.clientSeconds, labels, appID, now)

		// --- server_seconds histogram ---
		f.emitHistogramIfNonEmpty(md, metricNameSGServerSeconds, e.serverSeconds, labels, appID, now)

		// --- messaging_system_seconds histogram ---
		f.emitHistogramIfNonEmpty(md, metricNameSGMessagingSeconds, e.msgSeconds, labels, appID, now)

		// --- message_size_bytes histogram ---
		f.emitHistogramIfNonEmpty(md, metricNameSGMessageSize, e.messageSize, labels, appID, now)
	}

	return len(edges)
}

func (f *metricFlusher) emitCounter(md pmetric.Metrics, name string, value int64, labels map[string]string, appID string, now pcommon.Timestamp) {
	rm := md.ResourceMetrics().AppendEmpty()
	setResourceAttr(rm.Resource(), appID)
	sm := rm.ScopeMetrics().AppendEmpty()
	m := sm.Metrics().AppendEmpty()
	m.SetName(name)
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	sum.SetIsMonotonic(true)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetStartTimestamp(now)
	dp.SetTimestamp(now)
	dp.SetIntValue(value)
	setLabelsSorted(dp.Attributes(), labels)
}

func (f *metricFlusher) emitHistogramIfNonEmpty(md pmetric.Metrics, name string, h *histogram, labels map[string]string, appID string, now pcommon.Timestamp) {
	buckets, bounds, sumMicros, count := h.Snapshot()
	if count == 0 {
		return
	}
	rm := md.ResourceMetrics().AppendEmpty()
	setResourceAttr(rm.Resource(), appID)
	sm := rm.ScopeMetrics().AppendEmpty()
	m := sm.Metrics().AppendEmpty()
	m.SetName(name)
	hist := m.SetEmptyHistogram()
	hist.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	dp := hist.DataPoints().AppendEmpty()
	dp.SetStartTimestamp(now)
	dp.SetTimestamp(now)
	dp.SetCount(count)
	dp.SetSum(float64(sumMicros) / 1e6)
	dp.ExplicitBounds().FromRaw(bounds)
	dp.BucketCounts().FromRaw(buckets)
	setLabelsSorted(dp.Attributes(), labels)
}

func setResourceAttr(res pcommon.Resource, appID string) {
	if appID != "" {
		res.Attributes().PutStr("app_id", appID)
	}
}

// setLabelsSorted writes labels to an attribute map in sorted key order.
// This ensures deterministic JSON serialization for ES composite aggregations,
// which rely on consistent key ordering for object hashing.
func setLabelsSorted(attrs pcommon.Map, labels map[string]string) {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		attrs.PutStr(k, labels[k])
	}
}

func setLabels(attrs pcommon.Map, ds dimensionSet) {
	for i, k := range ds.keys {
		attrs.PutStr(k, ds.values[i])
	}
}
