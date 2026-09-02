// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// vmLine is one line of VM's /api/v1/import JSON line format: a whole series
// with one or more (value, timestamp) samples.
type vmLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"` // milliseconds
}

// MetricWriter implements observabilitystorageext.MetricWriter against
// VictoriaMetrics: OTLP → storedmodel conversion (shared with ES) → VM JSON
// lines → buffered batch POST /api/v1/import.
type MetricWriter struct {
	client   VMClient
	config   *Config
	logger   *zap.Logger
	registry *typeRegistry

	mu      sync.Mutex
	buffer  map[string]*vmLine // seriesKey → line (merges samples of same series)
	order   []string           // insertion order of seriesKey for deterministic flush
	size    int                // number of lines buffered
	stopCh  chan struct{}
	flushCh chan struct{}
	doneCh  chan struct{}
}

// NewMetricWriter builds the writer. The background flush loop starts here.
func NewMetricWriter(client VMClient, config *Config, registry *typeRegistry, logger *zap.Logger) *MetricWriter {
	w := &MetricWriter{
		client:   client,
		config:   config,
		logger:   logger,
		registry: registry,
		buffer:   make(map[string]*vmLine),
		stopCh:   make(chan struct{}),
		flushCh:  make(chan struct{}, 1),
		doneCh:   make(chan struct{}),
	}
	go w.flushLoop()
	return w
}

// WriteMetrics converts and buffers a batch of OTLP metrics.
func (w *MetricWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	now := time.Now()
	rs := md.ResourceMetrics()
	for i := 0; i < rs.Len(); i++ {
		resource := rs.At(i).Resource()
		ilms := rs.At(i).ScopeMetrics()
		for j := 0; j < ilms.Len(); j++ {
			metrics := ilms.At(j).Metrics()
			for k := 0; k < metrics.Len(); k++ {
				metric := metrics.At(k)
				for _, pt := range storedmodel.ConvertOTLPMetric(metric, resource) {
					w.ingestPoint(pt, now)
				}
			}
		}
	}
	// Record type metadata (in-memory, cheap) per metric name.
	for k := 0; k < rs.Len(); k++ {
		ilms := rs.At(k).ScopeMetrics()
		for j := 0; j < ilms.Len(); j++ {
			metrics := ilms.At(j).Metrics()
			for m := 0; m < metrics.Len(); m++ {
				metric := metrics.At(m)
				w.registry.record(metric.Name(), metricTypeString(metric), metric.Unit(), now)
			}
		}
	}
	// Batch full → flush now (asynchronously) so the caller is not blocked on
	// network I/O unless the buffer keeps filling faster than flush drains.
	w.mu.Lock()
	full := w.size >= w.config.BatchSize
	w.mu.Unlock()
	if full {
		select {
		case w.flushCh <- struct{}{}:
		default: // a flush is already pending
		}
	}
	return nil
}

// metricTypeString maps the pmetric type to the storage string used by
// storedmodel ("gauge"/"counter"/"histogram"/"summary").
func metricTypeString(m pmetric.Metric) string {
	switch m.Type() {
	case pmetric.MetricTypeSum:
		if m.Sum().IsMonotonic() {
			return "counter"
		}
		return "gauge"
	case pmetric.MetricTypeHistogram:
		return "histogram"
	case pmetric.MetricTypeSummary:
		return "summary"
	default:
		return "gauge"
	}
}

// ingestPoint buffers one StoredMetricDataPoint as one or more vmLine entries.
func (w *MetricWriter) ingestPoint(pt storedmodel.StoredMetricDataPoint, now time.Time) {
	if pt.Type == "histogram" && len(pt.BucketCounts) > 0 {
		for _, line := range convertHistogram(pt, w.config.ExtraLabels) {
			w.addLine(line)
		}
		return
	}
	w.addLine(pointToLine(pt, w.config.ExtraLabels))
}

// pointToLine converts a gauge/counter/summary point to a single vmLine.
func pointToLine(pt storedmodel.StoredMetricDataPoint, extra map[string]string) vmLine {
	labels := baseLabels(pt, extra)
	return vmLine{
		Metric:     labels,
		Values:     []float64{pt.Value},
		Timestamps: []int64{pt.TimeUnixMilli},
	}
}

// baseLabels builds the label set for a point: __name__ + service_name +
// extra labels + the point's own labels. app_id is injected by the caller
// via pt.AppID (see baseLabels impl below).
func baseLabels(pt storedmodel.StoredMetricDataPoint, extra map[string]string) map[string]string {
	labels := make(map[string]string, len(pt.Labels)+len(extra)+3)
	labels["__name__"] = pt.Name
	if pt.ServiceName != "" {
		labels["service_name"] = pt.ServiceName
	}
	if pt.AppID != "" {
		labels["app_id"] = pt.AppID
	}
	for k, v := range extra {
		labels[k] = v
	}
	for k, v := range pt.Labels {
		labels[k] = fmt.Sprintf("%v", v)
	}
	return labels
}

// convertHistogram expands a histogram point into Prometheus-convention
// sub-series: <base>_bucket{le=...} (cumulative), <base>_bucket{le="+Inf"},
// <base>_sum, <base>_count. Delta bucket_counts are accumulated to
// cumulative; cumulative bucket_counts are written as-is.
func convertHistogram(pt storedmodel.StoredMetricDataPoint, extra map[string]string) []vmLine {
	base := baseLabels(pt, extra)

	delta := pt.AggregationTemporality != "cumulative"
	cum := make([]uint64, len(pt.BucketCounts))
	var running uint64
	for i, bc := range pt.BucketCounts {
		if delta {
			running += bc
			cum[i] = running
		} else {
			cum[i] = bc
		}
	}

	var lines []vmLine
	for i, c := range cum {
		le := "+Inf"
		if i < len(pt.ExplicitBounds) {
			le = formatLE(pt.ExplicitBounds[i])
		}
		bucketLabels := copyLabels(base)
		bucketLabels["__name__"] = pt.Name + "_bucket"
		bucketLabels["le"] = le
		lines = append(lines, vmLine{
			Metric:     bucketLabels,
			Values:     []float64{float64(c)},
			Timestamps: []int64{pt.TimeUnixMilli},
		})
	}
	// _sum
	if pt.Value != 0 || len(pt.BucketCounts) == 0 {
		sumLabels := copyLabels(base)
		sumLabels["__name__"] = pt.Name + "_sum"
		lines = append(lines, vmLine{
			Metric:     sumLabels,
			Values:     []float64{pt.Value},
			Timestamps: []int64{pt.TimeUnixMilli},
		})
	}
	// _count
	countLabels := copyLabels(base)
	countLabels["__name__"] = pt.Name + "_count"
	lines = append(lines, vmLine{
		Metric:     countLabels,
		Values:     []float64{float64(pt.Count)},
		Timestamps: []int64{pt.TimeUnixMilli},
	})
	return lines
}

// formatLE renders a bucket bound the way Prometheus expects le values:
// shortest exact float representation.
func formatLE(bound float64) string {
	return strconv.FormatFloat(bound, 'g', -1, 64)
}

func copyLabels(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// seriesKey builds a stable key from sorted labels so repeated samples of the
// same series merge into one vmLine.
func seriesKey(metric map[string]string) string {
	keys := make([]string, 0, len(metric))
	for k := range metric {
		if k == "__name__" {
			continue // implied by sort position; keep key deterministic
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString(metric["__name__"])
	for _, k := range keys {
		b.WriteByte(',')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(metric[k])
	}
	return b.String()
}

// addLine merges the sample into the buffer: same series → append value/ts.
func (w *MetricWriter) addLine(line vmLine) {
	key := seriesKey(line.Metric)
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.buffer[key]; ok {
		existing.Values = append(existing.Values, line.Values...)
		existing.Timestamps = append(existing.Timestamps, line.Timestamps...)
		return
	}
	w.buffer[key] = &line
	w.order = append(w.order, key)
	w.size++
}

// flushLoop drains the buffer on interval or on demand.
func (w *MetricWriter) flushLoop() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.config.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		case <-w.flushCh:
		}
		if err := w.Flush(context.Background()); err != nil {
			w.logger.Warn("victoriametrics flush failed (batch dropped)", zap.Error(err))
		}
	}
}

// Flush serializes and posts all buffered lines.
func (w *MetricWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.order) == 0 {
		w.mu.Unlock()
		return nil
	}
	lines := make([]*vmLine, 0, len(w.order))
	for _, key := range w.order {
		if l, ok := w.buffer[key]; ok {
			lines = append(lines, l)
		}
	}
	w.buffer = make(map[string]*vmLine)
	w.order = nil
	w.size = 0
	w.mu.Unlock()

	if len(lines) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return fmt.Errorf("vm line encode failed: %w", err)
		}
	}
	return w.client.ImportLines(ctx, buf.Bytes())
}

// Stop terminates the flush loop.
func (w *MetricWriter) Stop() {
	close(w.stopCh)
	<-w.doneCh
}
