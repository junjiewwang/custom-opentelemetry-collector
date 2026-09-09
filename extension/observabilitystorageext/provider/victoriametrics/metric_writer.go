// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// textSample is one buffered sample line: metric name + labels + value + ts.
type textSample struct {
	metric     string // sanitized __name__
	labels     map[string]string
	value      float64
	timeUnixMi int64
}

// MetricWriter implements observabilitystorageext.MetricWriter against
// VictoriaMetrics using the Prometheus text exposition format
// (/api/v1/import/prometheus). Text was chosen over the VM JSON line format
// specifically because `# TYPE` / `# HELP` comment rows make VM store metric
// metadata natively (vmselect /api/v1/metadata) — the ES meta-index
// equivalent comes for free with this protocol, so the provider carries no
// type sidecar of its own. The trade-off (one line per sample instead of
// one line per series) is irrelevant at this scale.
type MetricWriter struct {
	client VMClient
	config *Config
	logger *zap.Logger

	mu         sync.Mutex
	buffer     []textSample
	sentTypes  map[string]storedmodel.MetricMeta // metric name → type already emitted in a previous flush
	pendingTyp []string                          // names needing a # TYPE row in the next flush
	size       int
	stopCh     chan struct{}
	flushCh    chan struct{}
	doneCh     chan struct{}
}

// metadataRefreshInterval is how often the writer forgets which # TYPE rows it
// has already sent, forcing the next flush to re-emit every active metric's
// # TYPE row. VictoriaMetrics' metricsmetadata table independently garbage-
// collects entries whose series go stale (observed live: 3210 inserted rows
// shrank to 0 while the collector kept running, because # TYPE was sent exactly
// once per metric via sentTypes). Re-sending is idempotent (VM overwrites) and
// the rows are tiny, so the cost is negligible against the 3s flush cadence.
const metadataRefreshInterval = 5 * time.Minute

// NewMetricWriter builds the writer. The background flush loop starts here.
func NewMetricWriter(client VMClient, config *Config, logger *zap.Logger) *MetricWriter {
	w := &MetricWriter{
		client:    client,
		config:    config,
		logger:    logger,
		sentTypes: make(map[string]storedmodel.MetricMeta),
		stopCh:    make(chan struct{}),
		flushCh:   make(chan struct{}, 1),
		doneCh:    make(chan struct{}),
	}
	go w.flushLoop()
	return w
}

// WriteMetrics converts and buffers a batch of OTLP metrics.
func (w *MetricWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	rs := md.ResourceMetrics()
	for i := 0; i < rs.Len(); i++ {
		resource := rs.At(i).Resource()
		ilms := rs.At(i).ScopeMetrics()
		for j := 0; j < ilms.Len(); j++ {
			metrics := ilms.At(j).Metrics()
			for k := 0; k < metrics.Len(); k++ {
				metric := metrics.At(k)
				meta := storedmodel.MetricMeta{Type: metricTypeString(metric)}
				w.noteType(metric.Name(), meta)
				for _, pt := range storedmodel.ConvertOTLPMetric(metric, resource) {
					w.ingestPoint(pt)
				}
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

// noteType records that a metric's type row should be emitted once. Histogram
// sub-series types (_bucket/_sum/_count → counter) are derived at read time
// (ListMetricTypes), not written — VM's metadata answers per family name.
func (w *MetricWriter) noteType(name string, meta storedmodel.MetricMeta) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if prev, ok := w.sentTypes[name]; ok && prev.Type == meta.Type {
		return
	}
	w.sentTypes[name] = meta
	w.pendingTyp = append(w.pendingTyp, name)
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

// ingestPoint buffers one StoredMetricDataPoint as one or more text samples.
func (w *MetricWriter) ingestPoint(pt storedmodel.StoredMetricDataPoint) {
	if pt.Type == "histogram" && len(pt.BucketCounts) > 0 {
		for _, s := range convertHistogram(pt, w.config.ExtraLabels) {
			w.addSample(s)
		}
		return
	}
	w.addSample(pointToSample(pt, w.config.ExtraLabels))
}

// pointToSample converts a gauge/counter/summary point to one text sample.
func pointToSample(pt storedmodel.StoredMetricDataPoint, extra map[string]string) textSample {
	return textSample{
		metric:     sanitizeName(pt.Name),
		labels:     baseLabels(pt, extra),
		value:      pt.Value,
		timeUnixMi: pt.TimeUnixMilli,
	}
}

// baseLabels builds the label set for a point: service_name + extra labels +
// the point's own labels. __name__ is NOT in this map (text format has the
// name outside the braces). app_id is injected from pt.AppID.
func baseLabels(pt storedmodel.StoredMetricDataPoint, extra map[string]string) map[string]string {
	labels := make(map[string]string, len(pt.Labels)+len(extra)+3)
	if pt.ServiceName != "" {
		labels["service_name"] = pt.ServiceName
	}
	if pt.AppID != "" {
		labels["app_id"] = pt.AppID
	}
	if pt.TenantID != "" {
		labels["tenant_id"] = pt.TenantID
	}
	for k, v := range extra {
		labels[k] = v
	}
	for k, v := range pt.Labels {
		labels[sanitizeName(k)] = fmt.Sprintf("%v", v)
	}
	return labels
}

// convertHistogram expands a histogram point into Prometheus-convention
// sub-series samples: <base>_bucket{le=...} (cumulative), <base>_bucket{le="+Inf"},
// <base>_sum, <base>_count. Delta bucket_counts are accumulated to
// cumulative; cumulative bucket_counts are written as-is.
func convertHistogram(pt storedmodel.StoredMetricDataPoint, extra map[string]string) []textSample {
	base := baseLabels(pt, extra)
	name := sanitizeName(pt.Name)

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

	var out []textSample
	for i, c := range cum {
		le := "+Inf"
		if i < len(pt.ExplicitBounds) {
			le = formatLE(pt.ExplicitBounds[i])
		}
		bucketLabels := copyLabels(base)
		bucketLabels["le"] = le
		out = append(out, textSample{metric: name + "_bucket", labels: bucketLabels,
			value: float64(c), timeUnixMi: pt.TimeUnixMilli})
	}
	if pt.Value != 0 || len(pt.BucketCounts) == 0 {
		out = append(out, textSample{metric: name + "_sum", labels: copyLabels(base),
			value: pt.Value, timeUnixMi: pt.TimeUnixMilli})
	}
	out = append(out, textSample{metric: name + "_count", labels: copyLabels(base),
		value: float64(pt.Count), timeUnixMi: pt.TimeUnixMilli})
	return out
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

// sanitizeName replaces dots and dashes with underscores so names are valid
// Prometheus identifiers (mirrors adminext's sanitizeMetricName; the ES
// provider achieved the same effect through its dotted-name lookup layer).
func sanitizeName(s string) string {
	b := make([]byte, 0, len(s))
	for _, ch := range []byte(s) {
		if ch == '.' || ch == '-' {
			b = append(b, '_')
		} else {
			b = append(b, ch)
		}
	}
	return string(b)
}

// addSample buffers one sample line.
func (w *MetricWriter) addSample(s textSample) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer = append(w.buffer, s)
	w.size++
}

// flushLoop drains the buffer on interval or on demand.
func (w *MetricWriter) flushLoop() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.config.FlushInterval)
	defer ticker.Stop()
	refresh := time.NewTicker(metadataRefreshInterval)
	defer refresh.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		case <-w.flushCh:
		case <-refresh.C:
			// Forget which # TYPE rows were sent so the next flush re-emits them
			// (VM's metricsmetadata table GCs stale entries independently).
			w.mu.Lock()
			w.sentTypes = make(map[string]storedmodel.MetricMeta)
			w.mu.Unlock()
		}
		if err := w.Flush(context.Background()); err != nil {
			w.logger.Warn("victoriametrics flush failed (batch dropped)", zap.Error(err))
		}
	}
}

// Flush serializes and posts all buffered samples plus any pending # TYPE rows.
func (w *MetricWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffer) == 0 && len(w.pendingTyp) == 0 {
		w.mu.Unlock()
		return nil
	}
	samples := w.buffer
	typeRows := w.pendingTyp
	typeMeta := make(map[string]storedmodel.MetricMeta, len(typeRows))
	for _, n := range typeRows {
		typeMeta[n] = w.sentTypes[n]
	}
	w.buffer = nil
	w.pendingTyp = nil
	w.size = 0
	w.mu.Unlock()

	var buf bytes.Buffer
	// Metadata rows first (VM stores them via its metricsmetadata table when
	// -enableMetadata is on, the default).
	for _, n := range typeRows {
		meta := typeMeta[n]
		fmt.Fprintf(&buf, "# TYPE %s %s\n", sanitizeName(n), meta.Type)
	}
	for _, s := range samples {
		buf.WriteString(renderSampleLine(s))
		buf.WriteByte('\n')
	}
	return w.client.ImportText(ctx, buf.Bytes())
}

// renderSampleLine renders `name{labels} value ts` with sorted label keys for
// deterministic output. Label values are escaped for a double-quoted string.
func renderSampleLine(s textSample) string {
	var b strings.Builder
	b.WriteString(s.metric)
	if len(s.labels) > 0 {
		keys := make([]string, 0, len(s.labels))
		for k := range s.labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteString(`="`)
			b.WriteString(escapeText(s.labels[k]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(s.value, 'g', -1, 64))
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(s.timeUnixMi, 10))
	return b.String()
}

// escapeText escapes a label value per the Prometheus text format
// (backslash, double quote, newline).
func escapeText(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// Stop terminates the flush loop.
func (w *MetricWriter) Stop() {
	close(w.stopCh)
	<-w.doneCh
}
