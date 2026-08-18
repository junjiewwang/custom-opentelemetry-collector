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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	// flatSem bounds concurrent ES QueryFlat calls across a single query's
	// bisection tree, keeping us under the ES connection pool limit.
	flatSem chan struct{}
}

// flatSemCapacity bounds concurrent flat/density ES requests per query. The ES
// client's MaxConnsPerHost is 20; 8 leaves headroom for other traffic.
const flatSemCapacity = 8

func (q *esQuerier) flatSemaphore() chan struct{} {
	if q.flatSem == nil {
		q.flatSem = make(chan struct{}, flatSemCapacity)
	}
	return q.flatSem
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
	labelEq, labelRe, appID := q.translateMatchers(matchers)

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

	// rate/increase/irate/delta must read ONLY raw (non-rollup) indices: rollup
	// counter docs carry Value=last (cumulative at bucket-end) timestamped at
	// bucket-start, which inflates the computed delta. Gauge aggregations
	// (avg/sum/max/...) MUST read rollup too, or 2h-older data disappears — so
	// gate raw-only on the surrounding function, not unconditionally.
	forRateQuery := hints != nil && isRateFunc(hints.Func)

	// Fast path: single concrete metric name
	if metricName != "" && !isRegex {
		raw, err := q.selectConcrete(ctx, metricName, labelEq, labelRe, appID, startMS, endMS, forRateQuery)
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
	// Cap the metadata lookback to 2h ending at maxt: metric names are stable
	// and low-cardinality, so a bounded window avoids the 429 that a wide
	// [mint, maxt] (e.g. a 7-day regex query) would trigger on ListMetricNames.
	const metaLookback = 2 * time.Hour
	metaStart := q.maxt - int64(metaLookback/time.Millisecond)
	if metaStart < q.mint {
		metaStart = q.mint
	}
	tr := observabilitystorageext.TimeRange{Start: timestamp.Time(metaStart), End: timestamp.Time(q.maxt)}
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

	return q.selectMultipleMetrics(ctx, allNames, labelEq, labelRe, appID, startMS, endMS, sortSeries, forRateQuery)
}

// esFlatSliceResult is the per-slice aggregation of a QueryFlat sub-query in
// the PromQL engine's raw-doc read path.
type esFlatSliceResult struct {
	samples   []observabilitystorageext.MetricSample
	total     int64
	truncated bool
	err       error
}

// esFlatSliceFloor is the minimum slice width for bisectFlatSlice. A 1m window
// is ~4 raw samples for a 15s scrape — never worth splitting further.
const esFlatSliceFloor = time.Minute

// bisectFlatSlice fetches raw samples for [startMS, endMS). It uses a
// density probe (one cheap date_histogram aggregation) to slice the window
// up front into sub-ranges that each stay under adaptiveFlatMaxDocs, then
// fetches those slices concurrently. If the reader does not support density
// probing, it falls back to recursive divide-on-truncation.
//
// This replaces the earlier "probe each candidate slice, discard truncated
// result, halve, repeat" approach, which wasted ~59% of ES work on discarded
// probes. With density slicing, a 1h high-cardinality window goes from 7 ES
// requests (3 discarded) to 1 density + 4 effective fetches, all parallel.
func (q *esQuerier) bisectFlatSlice(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, forRateQuery bool) esFlatSliceResult {
	ctx, span := otel.Tracer("").Start(ctx, "bisectFlatSlice",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("metric.name", metricName),
			attribute.Int64("span_ms", endMS-startMS),
		),
	)
	defer span.End()

	// Try to slice up front via density probe.
	if slices, ok := q.planFlatSlices(ctx, metricName, labelEq, labelRe, appID, startMS, endMS); ok {
		span.SetAttributes(attribute.Int("planned_slices", len(slices)))
		return q.fetchFlatSlices(ctx, metricName, labelEq, labelRe, appID, slices, forRateQuery)
	}

	// Fallback: recursive divide-on-truncation.
	return q.bisectFlatSliceRecursive(ctx, metricName, labelEq, labelRe, appID, startMS, endMS, forRateQuery)
}

// planFlatSlices uses a density probe to compute leaf slice boundaries such
// that each slice's doc count stays under the flat-doc cap. Returns ok=false
// when the reader has no density support or the probe fails, so the caller
// falls back to recursive bisection.
func (q *esQuerier) planFlatSlices(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64) ([]flatSliceRange, bool) {
	prober, ok := q.reader.(observabilitystorageext.FlatDensityProber)
	if !ok {
		return nil, false
	}

	widthMs := esFlatSliceFloor.Milliseconds() // 1m buckets: fine enough to bound any slice
	dq := observabilitystorageext.FlatDensityQuery{
		AppID:         appID,
		MetricName:    metricName,
		Labels:        labelEq,
		LabelMatch:    labelRe,
		ServiceName:   "",
		TimeRange:     observabilitystorageext.TimeRange{Start: timestamp.Time(startMS), End: timestamp.Time(endMS)},
		BucketWidthMs: widthMs,
	}
	buckets, err := prober.QueryFlatDensity(ctx, dq)
	if err != nil {
		q.logger.Debug("flat density probe failed, falling back to recursive bisection",
			zap.String("metric", metricName), zap.Error(err))
		return nil, false
	}
	if len(buckets) == 0 {
		// No docs at all — caller will handle the empty result.
		return []flatSliceRange{{start: startMS, end: endMS}}, true
	}

	// Group consecutive buckets into slices whose cumulative doc count stays
	// under the flat cap. The cap matches adaptiveFlatMaxDocs floor: 10000.
	const flatCap = 10000
	var slices []flatSliceRange
	curStart := startMS
	curCount := int64(0)
	for _, b := range buckets {
		if curCount > 0 && curCount+b.DocCount > flatCap {
			// Close the current slice at this bucket boundary.
			slices = append(slices, flatSliceRange{start: curStart, end: b.StartMs})
			curStart = b.StartMs
			curCount = 0
		}
		curCount += b.DocCount
	}
	slices = append(slices, flatSliceRange{start: curStart, end: endMS})
	return slices, true
}

// flatSliceRange is a [start, end) time slice for a flat fetch.
type flatSliceRange struct {
	start int64
	end   int64
}

// fetchFlatSlices fetches each slice concurrently (bounded by the flat
// semaphore) and merges results in time order.
func (q *esQuerier) fetchFlatSlices(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, slices []flatSliceRange, forRateQuery bool) esFlatSliceResult {
	if len(slices) == 1 {
		// Density already confirmed this single slice is under the cap — fetch
		// it directly, no recursion.
		return q.fetchOneFlat(ctx, metricName, labelEq, labelRe, appID, slices[0].start, slices[0].end, forRateQuery)
	}

	sem := q.flatSemaphore()
	results := make([]esFlatSliceResult, len(slices))
	var wg sync.WaitGroup
	for i, s := range slices {
		i, s := i, s
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = q.fetchOneFlat(ctx, metricName, labelEq, labelRe, appID, s.start, s.end, forRateQuery)
		}()
	}
	wg.Wait()

	var merged esFlatSliceResult
	for _, r := range results {
		if r.err != nil {
			return esFlatSliceResult{err: r.err}
		}
		merged.samples = append(merged.samples, r.samples...)
		merged.total += r.total
		merged.truncated = merged.truncated || r.truncated
	}
	return merged
}

// fetchOneFlat issues a single QueryFlat for [startMS, endMS) without any
// further bisection. It is used for density-planned leaf slices.
func (q *esQuerier) fetchOneFlat(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, forRateQuery bool) esFlatSliceResult {
	fq := observabilitystorageext.MetricFlatQuery{
		MetricName: metricName, Labels: labelEq, LabelMatch: labelRe,
		TimeRange: observabilitystorageext.TimeRange{
			Start: timestamp.Time(startMS),
			End:   timestamp.Time(endMS),
		},
		MaxDocs:      0,
		AppID:        appID,
		ForRateQuery: forRateQuery,
	}
	fr, err := q.reader.QueryFlat(ctx, fq)
	if err != nil {
		return esFlatSliceResult{err: err}
	}
	if fr == nil {
		return esFlatSliceResult{}
	}
	return esFlatSliceResult{samples: fr.Samples, total: fr.Total, truncated: fr.Truncated}
}

// bisectFlatSliceRecursive is the fallback divide-on-truncation path. It keeps
// the original semantics (probe, halve, recurse) for readers without density
// support and for the rare single-bucket-overflow case.
func (q *esQuerier) bisectFlatSliceRecursive(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, forRateQuery bool) esFlatSliceResult {
	fq := observabilitystorageext.MetricFlatQuery{
		MetricName: metricName, Labels: labelEq, LabelMatch: labelRe,
		TimeRange: observabilitystorageext.TimeRange{
			Start: timestamp.Time(startMS),
			End:   timestamp.Time(endMS),
		},
		MaxDocs:      0,
		AppID:        appID,
		ForRateQuery: forRateQuery,
	}
	fr, err := q.reader.QueryFlat(ctx, fq)
	if err != nil {
		return esFlatSliceResult{err: err}
	}
	if fr == nil {
		return esFlatSliceResult{}
	}

	// Not truncated — return as-is.
	if !fr.Truncated {
		return esFlatSliceResult{samples: fr.Samples, total: fr.Total, truncated: false}
	}

	// Truncated: halve the window and recurse, unless we've hit the floor.
	spanMS := endMS - startMS
	if spanMS <= esFlatSliceFloor.Milliseconds() {
		return esFlatSliceResult{samples: fr.Samples, total: fr.Total, truncated: true}
	}

	q.logger.Info("bisectFlatSlice: truncated, halving window",
		zap.String("metric", metricName),
		zap.Int64("total", fr.Total),
		zap.Int("returned", len(fr.Samples)),
		zap.Int64("span_ms", spanMS),
		zap.Int64("next_ms", spanMS/2),
	)

	midMS := startMS + spanMS/2
	left := q.bisectFlatSliceRecursive(ctx, metricName, labelEq, labelRe, appID, startMS, midMS, forRateQuery)
	right := q.bisectFlatSliceRecursive(ctx, metricName, labelEq, labelRe, appID, midMS, endMS, forRateQuery)

	return esFlatSliceResult{
		samples:   append(left.samples, right.samples...),
		total:     left.total + right.total,
		truncated: left.truncated || right.truncated,
	}
}

// selectConcreteSliced splits a large time window into concurrent QueryFlat
// sub-queries (each ≤ maxSlice) and merges the results. This avoids ES
// timeouts on 6h–24h windows where a single FlatQuery would time out.
func (q *esQuerier) selectConcreteSliced(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, cacheKey string, maxSlice time.Duration, forRateQuery bool) ([]observabilitystorageext.MetricRawSeries, error) {
	sliceSize := int64(maxSlice / time.Millisecond)
	slices := int((endMS-startMS)/sliceSize + 1)
	results := make([]esFlatSliceResult, slices)

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
			results[i] = q.bisectFlatSlice(ctx, metricName, labelEq, labelRe, appID, sStart, sEnd, forRateQuery)
		}()
	}
	wg.Wait()

	// Merge: group all samples by label set across slices.
	seriesMap := make(map[string]*observabilitystorageext.MetricRawSeries)
	var flatTotal int64
	anyTruncated := false
	for _, r := range results {
		if r.err != nil {
			q.logger.Warn("QueryFlat slice failed",
				zap.String("metric", metricName),
				zap.Error(r.err),
			)
			continue
		}
		flatTotal += r.total
		anyTruncated = anyTruncated || r.truncated
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
		zap.Bool("any_truncated", anyTruncated),
	)

	if len(out) == 0 {
		return nil, nil
	}
	out = q.expandHistogramBuckets(out)
	q.qCache.set(cacheKey, out)
	return out, nil
}

// selectConcrete fetches data for a single metric from ES, with per-Querier
// caching to avoid duplicate ES round-trips when the engine queries the same
// metric+timestamp combination multiple times.
func (q *esQuerier) selectConcrete(ctx context.Context, metricName string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, forRateQuery bool) ([]observabilitystorageext.MetricRawSeries, error) {
	// Lazy-init per-Querier cache
	if q.qCache == nil {
		q.qCache = &queryCache{}
	}

	cacheKey := fmt.Sprintf("%s|%s|%d|%d|raw=%v", metricName, joinLabels(labelEq), startMS, endMS, forRateQuery)
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
			return q.selectConcreteSliced(ctx, metricName, labelEq, labelRe, appID, startMS, endMS, cacheKey, maxSliceWindow, forRateQuery)
		}

		// ≤2h: fetch via bisectFlatSlice — the same divide-on-truncation path
		// used for larger windows. If the single QueryFlat is truncated (more
		// docs matched than MaxDocs returned), it recursively halves the window
		// down to a 1m floor, so a high-cardinality metric that exceeds the
		// adaptiveFlatMaxDocs cap even inside a ≤2h window is fetched in full
		// rather than silently dropping the tail. Previously this branch used a
		// one-shot QueryFlat with a fixed 15m fallback, which was neither
		// adaptive nor shared with the >2h path.
		res := q.bisectFlatSlice(ctx, metricName, labelEq, labelRe, appID, startMS, endMS, forRateQuery)
		if res.err != nil {
			return nil, fmt.Errorf("query flat %s: %w", metricName, res.err)
		}
		if len(res.samples) == 0 {
			return nil, nil
		}
		if res.truncated {
			// Only reachable if even a 1m floor slice is truncated (≈ single
			// series with >10000 docs/min — practically impossible). Warn rather
			// than fail: we still return the samples we managed to fetch.
			q.logger.Warn("QueryFlat still truncated after bisection",
				zap.String("metric", metricName),
				zap.Int64("total", res.total),
				zap.Int("returned", len(res.samples)),
				zap.Int64("range_ms", endMS-startMS),
			)
		}

		// Group samples by label set.
		seriesMap := make(map[string]*observabilitystorageext.MetricRawSeries)
		for _, sm := range res.samples {
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
			zap.Int64("flat_total", res.total),
		)
		out = q.expandHistogramBuckets(out)
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
			Labels: dp.Labels,
			Samples: []observabilitystorageext.MetricSample{{
				TimestampMs:  tsMs,
				Value:        dp.Value,
				BucketCounts: dp.BucketCounts,
				Bounds:       dp.ExplicitBounds,
			}},
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
func (q *esQuerier) selectMultipleMetrics(ctx context.Context, names []string, labelEq, labelRe map[string]string, appID string, startMS, endMS int64, sortSeries bool, forRateQuery bool) storage.SeriesSet {
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
			raw, err := q.selectConcrete(ctx, name, labelEq, labelRe, appID, startMS, endMS, forRateQuery)
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

// expandHistogramBuckets converts OTel histogram samples into per-bucket
// series with "le" labels so that sum by (le) (rate(...)) and histogram_quantile
// work correctly in Grafana Metrics Drilldown heatmaps.
//
// Each bucket (bounds[i], bucket_counts[i]) becomes its own series with
// cumulative count as Value. Non-histogram series pass through unchanged.
//
// IMPORTANT: This only runs in the PromQL engine path (isComplexPromQL=true).
// Simple queries without rate()/delta()/deriv() (e.g. plain gauge reads,
// avg(), sum()) go through the subset parser and are unaffected.
func (q *esQuerier) expandHistogramBuckets(series []observabilitystorageext.MetricRawSeries) []observabilitystorageext.MetricRawSeries {
	hasHistogram := false
	for _, s := range series {
		if len(s.Samples) > 0 && len(s.Samples[0].BucketCounts) > 0 {
			hasHistogram = true
			break
		}
	}
	if !hasHistogram {
		return series
	}

	var out []observabilitystorageext.MetricRawSeries
	for _, s := range series {
		if len(s.Samples) == 0 || len(s.Samples[0].BucketCounts) == 0 {
			out = append(out, s)
			continue
		}
		bc := s.Samples[0].BucketCounts
		bds := s.Samples[0].Bounds

		// Emit per-bucket series with "le" labels.
		perBucket := make([]observabilitystorageext.MetricRawSeries, len(bc))
		for i := range perBucket {
			lbls := make(map[string]string, len(s.Labels)+1)
			for k, v := range s.Labels {
				lbls[k] = v
			}
			if i < len(bds) {
				lbls["le"] = strconv.FormatFloat(bds[i], 'f', -1, 64)
			} else {
				lbls["le"] = "+Inf"
			}
			perBucket[i].Labels = lbls
		}

		// The collector's self-telemetry histogram is exported with DELTA
		// temporality (the framework's lowMemory selector): each 60s export
		// carries the per-interval bucket increments, NOT a cumulative count.
		// Prometheus rate()/histogram_quantile expect a cumulative histogram, so
		// accumulate bucket_counts ACROSS samples (in time order) to reconstruct
		// the monotonic cumulative distribution. The old code reset cum=0 per
		// sample, which collapsed every le series to "single-hit" values and made
		// sum by (le) (rate(...)) return 0.
		//
		// BucketCounts[i] is the per-bucket increment (delta). The emitted value
		// for le=bounds[i] must be the CUMULATIVE count ≤bounds[i], i.e. the
		// running sum of all buckets up to i.
		cumCounts := make([]float64, len(bc))
		for _, sm := range s.Samples {
			for bi := range sm.BucketCounts {
				if bi >= len(cumCounts) {
					break
				}
				cumCounts[bi] += float64(sm.BucketCounts[bi])
			}
			cum := float64(0)
			for bi := range cumCounts {
				cum += cumCounts[bi]
				perBucket[bi].Samples = append(perBucket[bi].Samples,
					observabilitystorageext.MetricSample{TimestampMs: sm.TimestampMs, Value: cum})
			}
		}
		out = append(out, perBucket...)
	}
	return out
}

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

func (q *esQuerier) translateMatchers(matchers []*labels.Matcher) (eq, re map[string]string, appID string) {
	eq, re = make(map[string]string), make(map[string]string)
	for _, m := range matchers {
		if m.Name == labels.MetricName { continue }
		// Skip Grafana-internal labels (__ignore_usage__, __grafana__,
		// etc.) that don't exist as real metric labels in storage.
		if strings.HasPrefix(m.Name, "__") { continue }
		// app_id/appId is a ROUTING label (index name segment), not a data label.
		// Extract it as the appID and don't filter on a non-existent label field.
		if (m.Name == "app_id" || m.Name == "appId") && m.Type == labels.MatchEqual {
			appID = m.Value
			continue
		}
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

	// Last resort: return the name unchanged. The old heuristic of replacing
	// every underscore with a dot is WRONG for underscore-native names like
	// "traces_spanmetrics_calls_total" (→ "traces.spanmetrics.calls.total",
	// which does not exist in ES). Dotted OTel names are already covered by
	// metricNameReverseMap and the global reverse map; if neither has the name,
	// it is safest to treat the sanitized name as already being the storage
	// name rather than guessing.
	return safeName
}

var (
	muReverseBuild         sync.Mutex
	globalMetricReverseMap map[string]string
	lastReverseBuildAttempt time.Time
)

func buildGlobalReverseMap(reader observabilitystorageext.MetricReader, logger *zap.Logger) {
	muReverseBuild.Lock()
	defer muReverseBuild.Unlock()
	if globalMetricReverseMap != nil {
		return
	}
	// Throttle retries: if a build recently failed (e.g. ES 429), don't hammer
	// ES on every incoming query — wait a short interval before retrying.
	if !lastReverseBuildAttempt.IsZero() && time.Since(lastReverseBuildAttempt) < 30*time.Second {
		return
	}
	lastReverseBuildAttempt = time.Now()

	// Use a short lookback window (2h, not 24h) to keep the ListMetricNames
	// terms aggregation bounded. Metric names are stable and low-cardinality:
	// a 2h window discovers every currently-active metric, while a 24h window
	// scanned ~10M documents and triggered ES 429 circuit_breaker (fielddata
	// >1.3GB). See Obsidian: custom-otel-collector metadata query design.
	tr := observabilitystorageext.TimeRange{
		Start: time.Now().Add(-2 * time.Hour),
		End:   time.Now(),
	}
	names, err := reader.ListMetricNames(context.Background(), tr)
	if err != nil {
		// Keep globalMetricReverseMap nil on failure so a later Querier call
		// retries the build (after the throttle interval). Setting it to an
		// empty map here would permanently disable the reverse map and make
		// unsanitizeMetricName fall through to the (now safe) identity fallback.
		logger.Warn("failed to build global metric reverse map (will retry)", zap.Error(err))
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

// isRateFunc reports whether a PromQL engine hints.Func denotes a range-vector
// counter function whose flat read must be raw-only (rollup's Value=last is
// cumulative-at-bucket-end and would inflate the delta). Gauge aggregations
// (avg/sum/max/min/count) and everything else return false so they still read
// rollup tier and don't lose >2h-old data.
func isRateFunc(fn string) bool {
	switch fn {
	case "rate", "increase", "irate", "delta", "deriv", "idelta":
		return true
	}
	return false
}

var _ = math.MaxFloat64
