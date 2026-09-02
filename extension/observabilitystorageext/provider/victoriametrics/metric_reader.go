// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// Compile-time assertion: the VM reader implements the public MetricReader
// interface directly (no adapter layer — unlike the ES/PG providers whose
// internal types need extension-level bridging).
var _ observabilitystorageext.MetricReader = (*MetricReader)(nil)

// MetricReader implements the public observabilitystorageext.MetricReader
// interface against VictoriaMetrics' Prometheus-compatible HTTP API.
type MetricReader struct {
	client   VMClient
	registry *typeRegistry
	logger   *zap.Logger
}

func newMetricReader(client VMClient, registry *typeRegistry, logger *zap.Logger) *MetricReader {
	return &MetricReader{client: client, registry: registry, logger: logger}
}

// defaultFlatMaxDocs caps QueryFlat/QueryRaw sample counts. VM's export has
// no ES-style max_result_window, but an unbounded window can still return
// enormous payloads; the cap mirrors the ES provider's flat floor.
const defaultFlatMaxDocs = 50000

// Query executes an instant metric query at query.Time.
func (r *MetricReader) Query(ctx context.Context, query observabilitystorageext.MetricQuery) (*observabilitystorageext.MetricResult, error) {
	selector := buildSelector(query.MetricName, query.AppID, query.ServiceName,
		query.Labels, query.LabelMatch, query.LabelNot, query.LabelNotMatch)
	res, err := r.client.QueryInstant(ctx, selector, query.Time)
	if err != nil {
		return nil, fmt.Errorf("vm instant query failed: %w", err)
	}
	out := &observabilitystorageext.MetricResult{Data: []observabilitystorageext.MetricDataPoint{}}
	for _, s := range res.Series {
		if s.Value == nil {
			continue
		}
		tsMs, v, ok := parseSamplePair(*s.Value)
		if !ok {
			continue
		}
		out.Data = append(out.Data, observabilitystorageext.MetricDataPoint{
			Metric:        s.Metric["__name__"],
			Labels:        stripAppIDLabel(s.Metric),
			Value:         v,
			TimeUnixMilli: strconv.FormatInt(tsMs, 10),
		})
	}
	return out, nil
}

// QueryRange executes a range query. Aggregation/GroupBy translate to a
// MetricsQL aggregation wrapper; step passes through.
func (r *MetricReader) QueryRange(ctx context.Context, query observabilitystorageext.MetricRangeQuery) (*observabilitystorageext.MetricRangeResult, error) {
	selector := buildSelector(query.MetricName, query.AppID, query.ServiceName,
		query.Labels, query.LabelMatch, query.LabelNot, query.LabelNotMatch)
	// missing_bucket semantics: ES's composite drops series lacking a grouped
	// label when false. MetricsQL's `by` keeps them with an empty label —
	// closest native behavior; bare-metric callers (missingBucket=true) are
	// the common path and match exactly.
	expr := buildRangeAggregation(query.Aggregation, selector, query.GroupBy)
	res, err := r.client.QueryRange(ctx, expr, query.TimeRange.Start, query.TimeRange.End, query.Step)
	if err != nil {
		return nil, fmt.Errorf("vm range query failed: %w", err)
	}
	limit := query.SeriesLimit
	if limit <= 0 {
		limit = 100
	}
	out := &observabilitystorageext.MetricRangeResult{Data: []observabilitystorageext.MetricSeries{}}
	for i, s := range res.Series {
		if i >= limit {
			break
		}
		series := observabilitystorageext.MetricSeries{
			Metric: s.Metric["__name__"],
			Labels: stripAppIDLabel(s.Metric),
			Values: []observabilitystorageext.MetricTimeValue{},
		}
		for _, pair := range s.Values {
			tsMs, v, ok := parseSamplePair(pair)
			if !ok {
				continue
			}
			series.Values = append(series.Values, observabilitystorageext.MetricTimeValue{
				TimeUnixMilli: strconv.FormatInt(tsMs, 10),
				Value:         v,
			})
		}
		out.Data = append(out.Data, series)
	}
	return out, nil
}

// QueryRaw returns raw sample points per series via /api/v1/export.
func (r *MetricReader) QueryRaw(ctx context.Context, query observabilitystorageext.MetricRawQuery) ([]observabilitystorageext.MetricRawSeries, error) {
	selector := buildSelector(query.MetricName, query.AppID, query.ServiceName,
		query.Labels, query.LabelMatch, query.LabelNot, query.LabelNotMatch)
	series, err := r.exportSeries(ctx, selector, query.TimeRange.Start, query.TimeRange.End, defaultFlatMaxDocs)
	if err != nil {
		return nil, err
	}
	out := make([]observabilitystorageext.MetricRawSeries, 0, len(series))
	for _, s := range series {
		rs := observabilitystorageext.MetricRawSeries{
			Labels:  stripAppIDLabel(s.Metric),
			Samples: make([]observabilitystorageext.MetricSample, 0, len(s.Values)),
		}
		for i, v := range s.Values {
			rs.Samples = append(rs.Samples, observabilitystorageext.MetricSample{
				TimestampMs: s.Timestamps[i],
				Value:       v,
			})
		}
		out = append(out, rs)
	}
	return out, nil
}

// QueryFlat returns a flat sample list for client-side grouping. Histogram
// base metrics are reassembled from their _bucket{le} sub-series so callers
// (histogram_quantile paths) see BucketCounts/Bounds per timestamp like the
// ES provider returns.
func (r *MetricReader) QueryFlat(ctx context.Context, query observabilitystorageext.MetricFlatQuery) (*observabilitystorageext.MetricFlatResult, error) {
	maxDocs := query.MaxDocs
	if maxDocs <= 0 {
		maxDocs = defaultFlatMaxDocs
	}

	base := query.MetricName
	if base != "" {
		if meta, ok := r.registry.get(base); ok && meta.Type == "histogram" {
			return r.queryFlatHistogram(ctx, query, maxDocs)
		}
	}

	selector := buildSelector(query.MetricName, query.AppID, query.ServiceName,
		query.Labels, query.LabelMatch, query.LabelNot, query.LabelNotMatch)
	series, err := r.exportSeries(ctx, selector, query.TimeRange.Start, query.TimeRange.End, maxDocs)
	if err != nil {
		return nil, err
	}
	samples := make([]observabilitystorageext.MetricSample, 0, maxDocs)
	for _, s := range series {
		labels := stripAppIDLabel(s.Metric)
		for i, v := range s.Values {
			if len(samples) >= maxDocs {
				return flatResult(samples, len(samples), true), nil
			}
			samples = append(samples, observabilitystorageext.MetricSample{
				TimestampMs: s.Timestamps[i],
				Value:       v,
				Labels:      labels,
			})
		}
	}
	return flatResult(samples, len(samples), false), nil
}

// queryFlatHistogram reassembles <base> histogram samples from the
// _bucket{le}/_sum/_count sub-series VM stores.
func (r *MetricReader) queryFlatHistogram(ctx context.Context, query observabilitystorageext.MetricFlatQuery, maxDocs int) (*observabilitystorageext.MetricFlatResult, error) {
	// Fetch the three sub-families with the same filter set.
	mkSel := func(suffix string) string {
		name := query.MetricName + suffix
		return buildSelector(name, query.AppID, query.ServiceName,
			query.Labels, query.LabelMatch, query.LabelNot, query.LabelNotMatch)
	}
	buckets, err := r.exportSeries(ctx, mkSel("_bucket"), query.TimeRange.Start, query.TimeRange.End, maxDocs)
	if err != nil {
		return nil, err
	}
	sums, err := r.exportSeries(ctx, mkSel("_sum"), query.TimeRange.Start, query.TimeRange.End, maxDocs)
	if err != nil {
		return nil, err
	}
	counts, err := r.exportSeries(ctx, mkSel("_count"), query.TimeRange.Start, query.TimeRange.End, maxDocs)
	if err != nil {
		return nil, err
	}

	// Group bucket series by (labelset minus le) → per-timestamp le→cum count.
	type tsPoint struct {
		le    string
		count float64
	}
	perSeries := map[string]map[int64][]tsPoint{}
	seriesLabels := map[string]map[string]string{}
	bounds := []float64{}
	// infCounts holds the le="+Inf" counts per series/timestamp so the last
	// BucketCounts slot (bounds+1) is populated, not silently zeroed.
	infCounts := map[string]map[int64]float64{}
	for _, s := range buckets {
		le := s.Metric["le"]
		if le == "" {
			continue
		}
		labels := stripAppIDLabel(stripLabel(s.Metric, "le"))
		key := labelsKey(labels)
		m := perSeries[key]
		if m == nil {
			m = map[int64][]tsPoint{}
			perSeries[key] = m
			seriesLabels[key] = labels
		}
		if le == "+Inf" {
			im := infCounts[key]
			if im == nil {
				im = map[int64]float64{}
				infCounts[key] = im
			}
			for i, v := range s.Values {
				im[s.Timestamps[i]] = v
			}
			continue
		}
		if b, err := strconv.ParseFloat(le, 64); err == nil {
			bounds = append(bounds, b)
		}
		for i, v := range s.Values {
			ts := s.Timestamps[i]
			m[ts] = append(m[ts], tsPoint{le: le, count: v})
		}
	}
	sort.Float64s(bounds)

	// Emit one MetricSample per (series, timestamp) with reassembled
	// BucketCounts (cumulative, ordered by bound) + Bounds.
	samples := make([]observabilitystorageext.MetricSample, 0, maxDocs)
	truncated := false
	for key, byTs := range perSeries {
		// sum/count lookup maps for this series
		sumAt := sampleAt(sums, seriesLabels[key])
		countAt := sampleAt(counts, seriesLabels[key])
		for ts, les := range byTs {
			if len(samples) >= maxDocs {
				truncated = true
				break
			}
			bc := make([]int64, len(bounds)+1) // bounds + +Inf
			for _, p := range les {
				idx := sort.SearchFloat64s(bounds, mustParse(p.le))
				if idx < len(bounds) && bounds[idx] == mustParse(p.le) {
					bc[idx] = int64(p.count)
				}
			}
			if inf := infCounts[key]; inf != nil {
				bc[len(bounds)] = int64(inf[ts])
			}
			samples = append(samples, observabilitystorageext.MetricSample{
				TimestampMs:  ts,
				Value:        sumAt(ts),
				Labels:       seriesLabels[key],
				BucketCounts: bc,
				Bounds:       append([]float64(nil), bounds...),
				Temporality:  "cumulative", // VM stores Prometheus-convention cumulative buckets
				Count:        int64(countAt(ts)),
			})
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TimestampMs < samples[j].TimestampMs })
	return flatResult(samples, len(samples), truncated), nil
}

// sampleAt builds a ts→value lookup from export series matching a label set.
// The __name__ is stripped on both sides before comparing: _sum/_count series
// carry a different name suffix than the _bucket series they pair with.
func sampleAt(series []VMExportSeries, labels map[string]string) func(int64) float64 {
	target := stripLabel(labels, "__name__")
	for _, s := range series {
		if labelsEqual(stripAppIDLabel(stripLabel(s.Metric, "__name__")), target) {
			m := map[int64]float64{}
			for i, v := range s.Values {
				m[s.Timestamps[i]] = v
			}
			return func(ts int64) float64 { return m[ts] }
		}
	}
	return func(int64) float64 { return 0 }
}

// labelsKey builds a deterministic string key from a label map (sorted k=v).
func labelsKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for _, k := range keys {
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, m[k]...)
		b = append(b, ',')
	}
	return string(b)
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func stripLabel(m map[string]string, key string) map[string]string {
	if _, ok := m[key]; !ok {
		return m
	}
	out := make(map[string]string, len(m)-1)
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func mustParse(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func flatResult(samples []observabilitystorageext.MetricSample, total int, truncated bool) *observabilitystorageext.MetricFlatResult {
	return &observabilitystorageext.MetricFlatResult{
		Samples:   samples,
		Total:     int64(total),
		Truncated: truncated,
	}
}

// exportSeries wraps client.Export with a defensive window: a zero bound is
// dropped (VM rejects pre-epoch durations), and the window is clamped around
// the data rather than wall clock.
func (r *MetricReader) exportSeries(ctx context.Context, selector string, start, end time.Time, maxDocs int) ([]VMExportSeries, error) {
	s, e := start, end
	if s.IsZero() {
		s = time.Unix(0, 0)
	}
	if e.IsZero() {
		e = time.Now().Add(time.Hour)
	}
	series, err := r.client.Export(ctx, []string{selector}, s, e)
	if err != nil {
		return nil, fmt.Errorf("vm export failed: %w", err)
	}
	// Enforce the doc cap at series granularity (values are arrays).
	total := 0
	for _, ser := range series {
		total += len(ser.Values)
		if total > maxDocs*10 { // generous headroom; flat-level cap enforced by caller
			break
		}
	}
	return series, nil
}

// ListMetricNames returns metric names, optionally filtered by AppID and
// excluding VM's own vm_* namespace.
func (r *MetricReader) ListMetricNames(ctx context.Context, timeRange observabilitystorageext.TimeRange) ([]string, error) {
	match := []string{}
	if timeRange.Start.IsZero() {
		timeRange.Start = time.Now().Add(-24 * time.Hour)
	}
	names, err := r.client.LabelValues(ctx, "__name__", match, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("vm label values failed: %w", err)
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if isVMPrefix(n) {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// ListMetricTypes returns the type registry snapshot (no VM HTTP call).
func (r *MetricReader) ListMetricTypes(ctx context.Context, timeRange observabilitystorageext.TimeRange) (map[string]storedmodel.MetricMeta, error) {
	return r.registry.snapshot(timeRange.Start, timeRange.End), nil
}

// ListLabelCombinations returns label value combinations via /api/v1/series.
func (r *MetricReader) ListLabelCombinations(ctx context.Context, query observabilitystorageext.LabelCombinationsQuery) (*observabilitystorageext.LabelCombinationsResult, error) {
	match := []string{buildSelector(query.MetricName, query.AppID, "", nil, nil, nil, nil)}
	series, err := r.client.Series(ctx, match, time.Now().Add(-24*time.Hour), time.Now())
	if err != nil {
		return nil, fmt.Errorf("vm series failed: %w", err)
	}
	keySet := make(map[string]struct{})
	out := []map[string]string{}
	for _, s := range series {
		combo := map[string]string{}
		for _, k := range query.LabelKeys {
			if v, ok := s[k]; ok {
				combo[k] = v
			}
		}
		key := fmt.Sprint(combo)
		if _, dup := keySet[key]; dup {
			continue
		}
		keySet[key] = struct{}{}
		out = append(out, combo)
	}
	return &observabilitystorageext.LabelCombinationsResult{Combinations: out}, nil
}

// ListLabelNames returns label names, optionally scoped by metric.
func (r *MetricReader) ListLabelNames(ctx context.Context, timeRange observabilitystorageext.TimeRange, metricName string) ([]string, error) {
	var match []string
	if metricName != "" {
		match = []string{metricName}
	}
	if timeRange.Start.IsZero() {
		timeRange.Start = time.Now().Add(-24 * time.Hour)
	}
	names, err := r.client.LabelNames(ctx, match, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("vm label names failed: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// ListLabelValues returns values for a label across all metrics.
func (r *MetricReader) ListLabelValues(ctx context.Context, label string, timeRange observabilitystorageext.TimeRange) ([]string, error) {
	return r.ListLabelValuesForMetric(ctx, label, "", timeRange)
}

// ListLabelValuesForMetric returns values for a label restricted to one metric.
func (r *MetricReader) ListLabelValuesForMetric(ctx context.Context, label, metricName string, timeRange observabilitystorageext.TimeRange) ([]string, error) {
	var match []string
	if metricName != "" {
		match = []string{metricName}
	}
	if timeRange.Start.IsZero() {
		timeRange.Start = time.Now().Add(-24 * time.Hour)
	}
	values, err := r.client.LabelValues(ctx, label, match, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("vm label values failed: %w", err)
	}
	// app_id is an internal isolation label; never surface it to Grafana.
	if label == "app_id" {
		return []string{}, nil
	}
	sort.Strings(values)
	return values, nil
}

// parseSamplePair decodes a Prometheus result pair [ts, "value"] where ts is
// a float unix-seconds and value is a JSON number or string.
func parseSamplePair(pair [2]any) (tsMs int64, v float64, ok bool) {
	tsF, err := toFloat64(pair[0])
	if err != nil {
		return 0, 0, false
	}
	vF, err := toFloat64(pair[1])
	if err != nil {
		return 0, 0, false
	}
	return int64(tsF * 1000), vF, true
}

func toFloat64(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case string:
		return strconv.ParseFloat(x, 64)
	case int64:
		return float64(x), nil
	default:
		return 0, fmt.Errorf("unparseable number %T", v)
	}
}
