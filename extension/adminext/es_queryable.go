package adminext

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/timestamp"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

type esQueryable struct {
	reader    observabilitystorageext.MetricReader
	logger    *zap.Logger
	maxSeries int
}

func newESQueryable(reader observabilitystorageext.MetricReader, logger *zap.Logger) *esQueryable {
	return &esQueryable{reader: reader, logger: logger, maxSeries: 5000}
}

func (q *esQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
	// Lazy-build the complete metric name reverse map on first Querier call.
	buildGlobalReverseMap(q.reader, q.logger)
	return &esQuerier{
		reader: q.reader, logger: q.logger,
		mint: mint, maxt: maxt, maxSeries: q.maxSeries,
	}, nil
}

// ── Querier ────────────────────────────────────────────

type esQuerier struct {
	reader    observabilitystorageext.MetricReader
	logger    *zap.Logger
	mint, maxt int64
	maxSeries int
	qCache    *queryCache
}

type queryCache struct {
	mu    sync.Mutex
	items map[string][]observabilitystorageext.MetricRawSeries
}

func (qc *queryCache) get(key string) []observabilitystorageext.MetricRawSeries {
	qc.mu.Lock(); defer qc.mu.Unlock()
	return qc.items[key]
}

func (qc *queryCache) set(key string, v []observabilitystorageext.MetricRawSeries) {
	qc.mu.Lock(); defer qc.mu.Unlock()
	if qc.items == nil { qc.items = make(map[string][]observabilitystorageext.MetricRawSeries) }
	qc.items[key] = v
}

// Select fetches metric data from ES. When the matchers specify a concrete
// metric name (__name__="foo"), it delegates to selectConcrete.  When the name
// is a regex or absent (e.g. {__name__=~".*"}) the set of matching metric
// names is resolved via ListMetricNames and selectConcrete is called
// concurrently for each metric, then the results are merged.
func (q *esQuerier) Select(ctx context.Context, sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	if len(matchers) == 0 {
		return storage.ErrSeriesSet(fmt.Errorf("empty matchers"))
	}

	metricName, isRegex := q.extractMetricNameEx(matchers)
	labelEq, labelRe := q.translateMatchers(matchers)

	q.logger.Debug("Select called",
		zap.String("metricName", metricName),
		zap.Bool("isRegex", isRegex),
		zap.Int64("mint", q.mint),
		zap.Int64("maxt", q.maxt),
	)

	// Convert PromQL-safe name (jvm_memory_used) → OTel dotted name (jvm.memory.used) for ES.
	if metricName != "" {
		originalName := metricName
		metricName = q.unsanitizeMetricName(metricName)
		q.logger.Debug("Select sanitization",
			zap.String("from", originalName),
			zap.String("to", metricName),
		)
	}

	startMS, endMS := q.mint, q.maxt
	if hints != nil {
		if hints.Start > 0 { startMS = maxInt64(startMS, hints.Start) }
		if hints.End   > 0 { endMS   = minInt64(endMS,   hints.End)   }
	}

	// Fast path: single concrete metric name
	if metricName != "" && !isRegex {
		raw, err := q.selectConcrete(ctx, metricName, labelEq, labelRe, startMS, endMS)
		if err != nil {
			q.logger.Error("select concrete failed", zap.String("metric", metricName), zap.Error(err))
			return storage.ErrSeriesSet(err)
		}
		sampleCount := 0
	for _, rs := range raw {
		sampleCount += len(rs.Samples)
	}
	q.logger.Debug("Select result", zap.String("metric", metricName), zap.Int("series", len(raw)), zap.Int("total_samples", sampleCount))
		return q.buildSeriesSet(raw, metricName, sortSeries)
	}

	// Slow path: resolve metric names from ES, select each concurrently.
	tr := observabilitystorageext.TimeRange{Start: timestamp.Time(q.mint), End: timestamp.Time(q.maxt)}
	allNames, err := q.reader.ListMetricNames(ctx, tr)
	if err != nil {
		return storage.ErrSeriesSet(fmt.Errorf("list metric names: %w", err))
	}

	// Filter to requested metric name(s). The regex is applied against the
	// sanitized (underscore) form of each dotted OTel name so that a
	// promql.Engine matcher like __name__="jvm_memory_used" matches.
	if metricName != "" && isRegex {
		filtered := make([]string, 0, len(allNames))
		re, err2 := compileSimpleRegex(metricName)
		if err2 == nil {
			for _, n := range allNames {
				if re.MatchString(sanitizeMetricName(n)) {
					filtered = append(filtered, n)
				}
			}
		}
		allNames = filtered
	}

	if len(allNames) == 0 {
		return storage.EmptySeriesSet()
	}

	return q.selectMultipleMetrics(ctx, allNames, labelEq, labelRe, startMS, endMS, sortSeries)
}

// selectConcreteSliced splits a large time window into concurrent QueryFlat
// sub-queries (each ≤ maxSlice) and merges the results. This avoids ES
// timeouts on 6h–24h windows where a single FlatQuery would time out.
func (q *esQuerier) selectConcreteSliced(ctx context.Context, metricName string, labelEq, labelRe map[string]string, startMS, endMS int64, cacheKey string, maxSlice time.Duration) ([]observabilitystorageext.MetricRawSeries, error) {
	type sliceResult struct {
		samples []observabilitystorageext.MetricSample
		total   int64
		err     error
	}

	sliceSize := int64(maxSlice / time.Millisecond)
	slices := int((endMS-startMS)/sliceSize + 1)
	results := make([]sliceResult, slices)

	var wg sync.WaitGroup
	for i := 0; i < slices; i++ {
		i := i
		sStart := startMS + int64(i)*sliceSize
		sEnd := sStart + sliceSize
		if sEnd > endMS {
			sEnd = endMS
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fq := observabilitystorageext.MetricFlatQuery{
				MetricName: metricName, Labels: labelEq, LabelMatch: labelRe,
				TimeRange: observabilitystorageext.TimeRange{
					Start: timestamp.Time(sStart),
					End:   timestamp.Time(sEnd),
				},
				MaxDocs: 0,
			}
			fr, err := q.reader.QueryFlat(ctx, fq)
			if err != nil {
				results[i].err = err
				return
			}
			if fr != nil {
				results[i].samples = fr.Samples
				results[i].total = fr.Total
			}
		}()
	}
	wg.Wait()

	// Merge: group all samples by label set across slices.
	seriesMap := make(map[string]*observabilitystorageext.MetricRawSeries)
	var flatTotal int64
	for _, r := range results {
		if r.err != nil {
			q.logger.Warn("QueryFlat slice failed",
				zap.String("metric", metricName),
				zap.Error(r.err),
			)
			continue
		}
		flatTotal += r.total
		for _, sm := range r.samples {
			key := labelsToSortedString(sm.Labels)
			series, ok := seriesMap[key]
			if !ok {
				labelsCopy := make(map[string]string, len(sm.Labels))
				for k, v := range sm.Labels {
					labelsCopy[k] = v
				}
				series = &observabilitystorageext.MetricRawSeries{Labels: labelsCopy}
				seriesMap[key] = series
			}
			series.Samples = append(series.Samples, observabilitystorageext.MetricSample{
				TimestampMs: sm.TimestampMs, Value: sm.Value,
				BucketCounts: sm.BucketCounts, Bounds: sm.Bounds,
			})
		}
	}

	// Sort and deduplicate.
	out := make([]observabilitystorageext.MetricRawSeries, 0, len(seriesMap))
	totalSamples := 0
	for _, series := range seriesMap {
		sort.Slice(series.Samples, func(i, j int) bool {
			return series.Samples[i].TimestampMs < series.Samples[j].TimestampMs
		})
		if len(series.Samples) > 1 {
			deduped := series.Samples[:1]
			for i := 1; i < len(series.Samples); i++ {
				if series.Samples[i].TimestampMs != deduped[len(deduped)-1].TimestampMs {
					deduped = append(deduped, series.Samples[i])
				}
			}
			series.Samples = deduped
		}
		totalSamples += len(series.Samples)
		out = append(out, *series)
	}

	q.logger.Info("Range query result (QueryFlat sliced)",
		zap.String("metric", metricName),
		zap.Int("series", len(out)),
		zap.Int("total_samples", totalSamples),
		zap.Int64("range_sec", (endMS-startMS)/1000),
		zap.Int("slices", slices),
		zap.Int64("flat_total", flatTotal),
	)

	if len(out) == 0 {
		return nil, nil
	}
	q.qCache.set(cacheKey, out)
	return out, nil
}

// selectConcrete fetches data for a single metric from ES, with per-Querier
// caching to avoid duplicate ES round-trips when the engine queries the same
// metric+timestamp combination multiple times.
func (q *esQuerier) selectConcrete(ctx context.Context, metricName string, labelEq, labelRe map[string]string, startMS, endMS int64) ([]observabilitystorageext.MetricRawSeries, error) {
	// Lazy-init per-Querier cache
	if q.qCache == nil {
		q.qCache = &queryCache{}
	}

	cacheKey := fmt.Sprintf("%s|%s|%d|%d", metricName, joinLabels(labelEq), startMS, endMS)
	if cached := q.qCache.get(cacheKey); cached != nil {
		return cached, nil
	}

	isRange := endMS - startMS > 30_000

	// Range query: use QueryFlat to fetch all sample points across the full
	// time range in a single ES call (no aggregation). Flat documents carry
	// per-sample labels, so we group by label set in Go. This gives the
	// PromQL engine dense enough data for rate()/delta() at any lookback.
	//
	// For windows > maxSliceWindow, we slice the query into concurrent
	// sub-queries to avoid ES timeouts on large time ranges.
	if isRange {
		const maxSliceWindow = 2 * time.Hour
		window := time.Duration(endMS-startMS) * time.Millisecond
		if window > maxSliceWindow {
			return q.selectConcreteSliced(ctx, metricName, labelEq, labelRe, startMS, endMS, cacheKey, maxSliceWindow)
		}

		flatQuery := observabilitystorageext.MetricFlatQuery{
			MetricName: metricName, Labels: labelEq, LabelMatch: labelRe,
			TimeRange: observabilitystorageext.TimeRange{
				Start: timestamp.Time(startMS),
				End:   timestamp.Time(endMS),
			},
			MaxDocs: 0, // use adaptive default (floor 10000, ceiling 50000)
		}
		flatResult, err := q.reader.QueryFlat(ctx, flatQuery)
		if err != nil {
			return nil, fmt.Errorf("query flat %s: %w", metricName, err)
		}
		if flatResult == nil || len(flatResult.Samples) == 0 {
			return nil, nil
		}

		// Group samples by label set.
		seriesMap := make(map[string]*observabilitystorageext.MetricRawSeries)
		for _, sm := range flatResult.Samples {
			key := labelsToSortedString(sm.Labels)
			series, ok := seriesMap[key]
			if !ok {
				labelsCopy := make(map[string]string, len(sm.Labels))
				for k, v := range sm.Labels {
					labelsCopy[k] = v
				}
				series = &observabilitystorageext.MetricRawSeries{Labels: labelsCopy}
				seriesMap[key] = series
			}
			series.Samples = append(series.Samples, observabilitystorageext.MetricSample{
				TimestampMs: sm.TimestampMs, Value: sm.Value,
				BucketCounts: sm.BucketCounts, Bounds: sm.Bounds,
			})
		}

		// Sort and deduplicate.
		out := make([]observabilitystorageext.MetricRawSeries, 0, len(seriesMap))
		totalSamples := 0
		for _, series := range seriesMap {
			sort.Slice(series.Samples, func(i, j int) bool {
				return series.Samples[i].TimestampMs < series.Samples[j].TimestampMs
			})
			if len(series.Samples) > 1 {
				deduped := series.Samples[:1]
				for i := 1; i < len(series.Samples); i++ {
					if series.Samples[i].TimestampMs != deduped[len(deduped)-1].TimestampMs {
						deduped = append(deduped, series.Samples[i])
					}
				}
				series.Samples = deduped
			}
			totalSamples += len(series.Samples)
			out = append(out, *series)
		}

		q.logger.Debug("Range query result (QueryFlat)",
			zap.String("metric", metricName),
			zap.Int("series", len(out)),
			zap.Int("total_samples", totalSamples),
			zap.Int64("range_sec", (endMS-startMS)/1000),
			zap.Int64("flat_total", flatResult.Total),
		)
		q.qCache.set(cacheKey, out)
		return out, nil
	}

	// Instant query: single-point lookup via Query.
	qry := observabilitystorageext.MetricQuery{
		MetricName: metricName, Labels: labelEq, LabelMatch: labelRe,
		Time: timestamp.Time(endMS),
	}
	result, err := q.reader.Query(ctx, qry)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", metricName, err)
	}
	if len(result.Data) == 0 {
		return nil, nil
	}

	var out []observabilitystorageext.MetricRawSeries
	for _, dp := range result.Data {
		tsMs, _ := strconv.ParseInt(dp.TimeUnixMilli, 10, 64)
		out = append(out, observabilitystorageext.MetricRawSeries{
			Labels:  dp.Labels,
			Samples: []observabilitystorageext.MetricSample{{TimestampMs: tsMs, Value: dp.Value}},
		})
	}
	q.qCache.set(cacheKey, out)
	return out, nil
}

func joinLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// selectMultipleMetrics concurrently fetches data for each metric name and
// merges the results into a single SeriesSet.
func (q *esQuerier) selectMultipleMetrics(ctx context.Context, names []string, labelEq, labelRe map[string]string, startMS, endMS int64, sortSeries bool) storage.SeriesSet {
	var (
		mu     sync.Mutex
		series []storage.Series
		wg     sync.WaitGroup
	)
	const concurrency = 8
	sem := make(chan struct{}, concurrency)

	for _, name := range names {
		name := name
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw, err := q.selectConcrete(ctx, name, labelEq, labelRe, startMS, endMS)
			if err != nil {
				q.logger.Debug("select metric skipped", zap.String("metric", name), zap.Error(err))
				return
			}
			mu.Lock()
			for _, rs := range raw {
				lb := translateLabelsToPrometheus(rs.Labels)
				lb = append(lb, labels.Label{Name: labels.MetricName, Value: sanitizeMetricName(name)})
				sort.Sort(lb)
				series = append(series, &esSeries{lbls: lb, samples: rs.Samples})
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(series) > q.maxSeries {
		series = series[:q.maxSeries]
	}

	if sortSeries {
		sort.Slice(series, func(i, j int) bool {
			return labels.Compare(series[i].Labels(), series[j].Labels()) < 0
		})
	}
	return &esSeriesSet{series: series, pos: -1}
}

func (q *esQuerier) buildSeriesSet(rawData []observabilitystorageext.MetricRawSeries, metricName string, sortSeries bool) storage.SeriesSet {
	if len(rawData) > q.maxSeries {
		rawData = rawData[:q.maxSeries]
	}
	series := make([]storage.Series, 0, len(rawData))
	for _, rs := range rawData {
		lb := translateLabelsToPrometheus(rs.Labels)
		lb = append(lb, labels.Label{Name: labels.MetricName, Value: sanitizeMetricName(metricName)})
		sort.Sort(lb)
		series = append(series, &esSeries{lbls: lb, samples: rs.Samples})
	}
	if sortSeries {
		sort.Slice(series, func(i, j int) bool {
			return labels.Compare(series[i].Labels(), series[j].Labels()) < 0
		})
	}
	return &esSeriesSet{series: series, pos: -1}
}

func (q *esQuerier) LabelValues(ctx context.Context, name string, _ *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	metricName, _ := q.extractMetricNameEx(matchers)
	tr := observabilitystorageext.TimeRange{Start: timestamp.Time(q.mint), End: timestamp.Time(q.maxt)}
	var vals []string
	var err error
	if metricName == "" {
		vals, err = q.reader.ListLabelValues(ctx, name, tr)
	} else {
		vals, err = q.reader.ListLabelValuesForMetric(ctx, name, metricName, tr)
	}
	return vals, nil, err
}

func (q *esQuerier) LabelNames(ctx context.Context, _ *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	metricName, _ := q.extractMetricNameEx(matchers)
	tr := observabilitystorageext.TimeRange{Start: timestamp.Time(q.mint), End: timestamp.Time(q.maxt)}
	names, err := q.reader.ListLabelNames(ctx, tr, metricName)
	return names, nil, err
}

func (q *esQuerier) Close() error { return nil }

// labelsToSortedString serializes a label map to a deterministic string key.
func labelsToSortedString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// ── Matchers ───────────────────────────────────────────

func (q *esQuerier) extractMetricNameEx(matchers []*labels.Matcher) (name string, isRegex bool) {
	for _, m := range matchers {
		if m.Name == labels.MetricName {
			if m.Type == labels.MatchEqual {
				return m.Value, false
			}
			return m.Value, true
		}
	}
	return "", false
}

func (q *esQuerier) translateMatchers(matchers []*labels.Matcher) (eq, re map[string]string) {
	eq, re = make(map[string]string), make(map[string]string)
	for _, m := range matchers {
		if m.Name == labels.MetricName { continue }
		// Skip Grafana-internal labels (__ignore_usage__, __grafana__,
		// etc.) that don't exist as real metric labels in storage.
		if strings.HasPrefix(m.Name, "__") { continue }
		switch m.Type {
		case labels.MatchEqual:
			eq[m.Name] = m.Value
		case labels.MatchRegexp, labels.MatchNotEqual, labels.MatchNotRegexp:
			re[m.Name] = m.Value
		}
	}
	return
}

func translateLabelsToPrometheus(otelLabels map[string]string) labels.Labels {
	result := make(labels.Labels, 0, len(otelLabels))
	for k, v := range otelLabels {
		result = append(result, labels.Label{Name: translateLabelToPromQL(k), Value: v})
	}
	return result
}

// sanitizeMetricName replaces dots with underscores so the metric name is
// valid as a Prometheus __name__ label (alphanum + underscore + colon only).
func sanitizeMetricName(otelName string) string {
	b := make([]byte, 0, len(otelName))
	for _, ch := range []byte(otelName) {
		if ch == '.' || ch == '-' {
			b = append(b, '_')
		} else {
			b = append(b, ch)
		}
	}
	return string(b)
}

// unsanitizeMetricName maps a Prometheus-safe underscored name back to the
// original OTel dotted name. Uses a lazily-built complete reverse map from
// ListMetricNames, falling back to a simple batch of underscore→dot
// heuristics for names not yet cached.
func (q *esQuerier) unsanitizeMetricName(safeName string) string {
	if orig, ok := metricNameReverseMap[safeName]; ok {
		return orig
	}
	// Try lazy-populated global reverse map built from ListMetricNames.
	muReverseBuild.Lock()
	if orig, ok := globalMetricReverseMap[safeName]; ok {
		muReverseBuild.Unlock()
		return orig
	}
	muReverseBuild.Unlock()

	// Last resort: try the simplest heuristic — replace all underscores
	// back to dots. This is correct for metric names where every underscore
	// came from a dot replacement (the common case for OTel dotted names).
	// Not safe for names like "traces_spanmetrics_calls_total" (mixed),
	// but these will be cached correctly on the first ListMetricNames pass.
	heuristic := strings.ReplaceAll(safeName, "_", ".")
	return heuristic
}

var (
	muReverseBuild         sync.Mutex
	globalMetricReverseMap map[string]string
)

func buildGlobalReverseMap(reader observabilitystorageext.MetricReader, logger *zap.Logger) {
	muReverseBuild.Lock()
	defer muReverseBuild.Unlock()
	if globalMetricReverseMap != nil {
		return
	}
	tr := observabilitystorageext.TimeRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now(),
	}
	names, err := reader.ListMetricNames(context.Background(), tr)
	if err != nil {
		logger.Warn("failed to build global metric reverse map", zap.Error(err))
		globalMetricReverseMap = make(map[string]string)
		return
	}
	globalMetricReverseMap = make(map[string]string, len(names))
	for _, n := range names {
		globalMetricReverseMap[sanitizeMetricName(n)] = n
	}
	logger.Info("built global metric reverse map", zap.Int("count", len(globalMetricReverseMap)))
}

// reverseMetricNameLookup uses the known OTel metric name mapping.
func reverseMetricNameLookup(safeName string) string {
	if orig, ok := metricNameReverseMap[safeName]; ok {
		return orig
	}
	return safeName
}

// metricNameReverseMap is the reverse of normalizeQueryForPromQL.
var metricNameReverseMap = map[string]string{}
func init() {
	pairs := map[string]string{
		"jvm_memory_used":                "jvm.memory.used",
		"jvm_memory_committed":           "jvm.memory.committed",
		"jvm_memory_limit":               "jvm.memory.limit",
		"jvm_memory_used_after_last_gc":  "jvm.memory.used_after_last_gc",
		"jvm_gc_duration":                "jvm.gc.duration",
		"jvm_thread_count":               "jvm.thread.count",
		"jvm_cpu_recent_utilization":     "jvm.cpu.recent_utilization",
		"jvm_cpu_time":                   "jvm.cpu.time",
		"jvm_cpu_count":                  "jvm.cpu.count",
		"jvm_class_loaded":               "jvm.class.loaded",
		"jvm_class_unloaded":             "jvm.class.unloaded",
		"jvm_class_count":                "jvm.class.count",
		"demo_runtime_jvm_gc_timeOrCount_v2": "demo.runtime.jvm.gc.timeOrCount.v2",
	}
	for k, v := range pairs {
		metricNameReverseMap[k] = v
	}
}

// ── Regex helper ───────────────────────────────────────

func compileSimpleRegex(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}

// ── Series ─────────────────────────────────────────────

type esSeries struct {
	lbls    labels.Labels
	samples []observabilitystorageext.MetricSample
}

func (s *esSeries) Labels() labels.Labels { return s.lbls }
func (s *esSeries) Iterator(_ chunkenc.Iterator) chunkenc.Iterator {
	return &esSeriesIterator{samples: s.samples, pos: -1}
}

type esSeriesSet struct {
	series []storage.Series
	pos    int
}

func (s *esSeriesSet) Next() bool                      { s.pos++; return s.pos < len(s.series) }
func (s *esSeriesSet) At() storage.Series               { return s.series[s.pos] }
func (s *esSeriesSet) Err() error                       { return nil }
func (s *esSeriesSet) Warnings() annotations.Annotations { return nil }

type esSeriesIterator struct {
	samples []observabilitystorageext.MetricSample
	pos     int
}

func (it *esSeriesIterator) Next() chunkenc.ValueType {
	it.pos++
	if it.pos >= len(it.samples) { return chunkenc.ValNone }
	return chunkenc.ValFloat
}

func (it *esSeriesIterator) Seek(t int64) chunkenc.ValueType {
	targetMs := timestamp.FromTime(timestamp.Time(t))
	it.pos = sort.Search(len(it.samples), func(i int) bool {
		return it.samples[i].TimestampMs >= targetMs
	})
	if it.pos >= len(it.samples) { return chunkenc.ValNone }
	return chunkenc.ValFloat
}

func (it *esSeriesIterator) At() (int64, float64) {
	s := it.samples[it.pos]
	return timestamp.FromTime(time.UnixMilli(s.TimestampMs)), s.Value
}

func (it *esSeriesIterator) AtHistogram(_ *histogram.Histogram) (int64, *histogram.Histogram)  { return 0, nil }
func (it *esSeriesIterator) AtFloatHistogram(_ *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) { return 0, nil }
func (it *esSeriesIterator) AtT() int64 { t, _ := it.At(); return t }
func (it *esSeriesIterator) Err() error { return nil }

func maxInt64(a, b int64) int64 { if a < b { return b }; return a }
func minInt64(a, b int64) int64 { if a > b { return b }; return a }
var _ = math.MaxFloat64
