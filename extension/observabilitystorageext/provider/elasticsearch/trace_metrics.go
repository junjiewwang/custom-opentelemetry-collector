// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/unitconv"
	"go.uber.org/zap"
)

// QueryTraceMetrics executes a TraceQL metrics query using ES histogram
// aggregations on the startTimeUnixNano long field. Each metrics function is
// computed as:
//
//	rate()              → histogram with value_count sub-aggregation,
//	                       divided by bucket interval seconds
//	quantile_over_time  → histogram with percentiles sub-aggregation
//	                       on the specified field
//	histogram_over_time → histogram with value_count sub-aggregation
//	                       on the specified field
//
// Note: We use "histogram" (not "date_histogram") because startTimeUnixNano is
// a long field storing nanoseconds. ES date_histogram requires a date-type field
// and its fixed_interval only accepts time-unit strings (e.g. "75s"), not raw
// numbers. The histogram aggregation accepts a numeric interval directly.
//
// Group-by (by()) is supported via terms sub-aggregations.
func (r *TraceReader) QueryTraceMetrics(ctx context.Context, query TraceMetricsQuery) (*TraceMetricsResult, error) {
	if query.Step <= 0 {
		query.Step = 15 * time.Second
	}

	// Build base filter from span conditions.
	baseFilter := r.buildMetricsFilter(query)

	// Build histogram aggregation on the long nanosecond field.
	// startTimeUnixNano is a long field storing nanoseconds, so interval and
	// bounds must be expressed in nanoseconds as well.
	bucketAggName := "buckets"
	stepNanos := query.Step.Nanoseconds()
	histogramAgg := map[string]any{
		"field":         FieldStartTimeUnixNano,
		"interval":      stepNanos,
		"min_doc_count": 0,
		"extended_bounds": map[string]any{
			"min": query.TimeRange.Start.UnixNano(),
			"max": query.TimeRange.End.UnixNano(),
		},
	}

	// Build the complete aggregation tree (histogram + optional group-by).
	searchAggs := r.buildMetricsAggTree(query, histogramAgg, bucketAggName)

	searchReq := &SearchRequest{
		Query:        baseFilter,
		Size:         0,
		Aggregations: searchAggs,
	}

	indexPat := r.indexPatternForRange(query.AppID, query.TimeRange.Start, query.TimeRange.End)
	resp, err := r.searcher.Search(ctx, indexPat, searchReq)
	if err != nil {
		return nil, fmt.Errorf("trace metrics query failed: %w", err)
	}

	return r.parseMetricsResponse(resp, query, bucketAggName)
}

// buildMetricsFilter builds the ES query filter from the span filter conditions.
func (r *TraceReader) buildMetricsFilter(query TraceMetricsQuery) map[string]any {
	must := []map[string]any{}
	must = append(must, r.timeRangeQuery(query.TimeRange))

	if query.ServiceName != "" {
		must = append(must, map[string]any{"term": map[string]any{FieldServiceName: query.ServiceName}})
	}
	if query.OperationName != "" {
		must = append(must, map[string]any{"term": map[string]any{FieldName: query.OperationName}})
	}
	if query.SpanKind != "" {
		// TraceQL uses lowercase (server, client), ES stores capitalized (Server, Client).
		must = append(must, map[string]any{"term": map[string]any{FieldKind: capitalizeFirst(query.SpanKind)}})
	}
	if query.Status != "" {
		// status.code is text (legacy) in some indices and keyword (current) in
		// others — match both casings. See statusTermsClause.
		must = append(must, statusTermsClause(query.Status))
	}
	if query.IsRoot {
		// Root span: parentSpanId field is absent (omitempty) for new data,
		// or "0000000000000000" for historical data written before the writer bug fix.
		must = append(must, map[string]any{
			"bool": map[string]any{
				"should": []map[string]any{
					{"bool": map[string]any{"must_not": []map[string]any{{"exists": map[string]any{"field": FieldParentSpanID}}}}},
					{"term": map[string]any{FieldParentSpanID: "0000000000000000"}},
				},
				"minimum_should_match": 1,
			},
		})
	}

	// ── Root span intrinsic filters ──
	if query.RootName != "" {
		must = append(must, map[string]any{
			"bool": map[string]any{"must": []map[string]any{
				{"term": map[string]any{FieldName: query.RootName}},
				{"bool": map[string]any{"must_not": []map[string]any{{"exists": map[string]any{"field": FieldParentSpanID}}}}},
			}},
		})
	}
	if query.RootService != "" {
		must = append(must, map[string]any{
			"bool": map[string]any{"must": []map[string]any{
				{"term": map[string]any{FieldServiceName: query.RootService}},
				{"bool": map[string]any{"must_not": []map[string]any{{"exists": map[string]any{"field": FieldParentSpanID}}}}},
			}},
		})
	}

	if query.MinDuration > 0 {
		must = append(must, map[string]any{
			"range": map[string]any{FieldDurationNano: map[string]any{"gte": query.MinDuration.Nanoseconds()}},
		})
	}
	if query.MaxDuration > 0 {
		must = append(must, map[string]any{
			"range": map[string]any{FieldDurationNano: map[string]any{"lte": query.MaxDuration.Nanoseconds()}},
		})
	}

	// ── Shared tag filters (Tags / TagsOr / TagsNot / TagsExists / TagsRegex) ──
	// Route through the same appendSharedTagFilters used by buildTraceSearchQuery
	// so that field resolution, value transformation (capitalizeFirst for
	// kind/status), status.message match-vs-term handling, and the unscoped
	// attribute dual-path (attributes.X + resource.X) are identical across the
	// search and metrics filter paths. Previously this inlined its own
	// resolver.Resolve + metricsTermClause logic, which skipped the value
	// transforms (e.g. span.kind "server" was not capitalized → matched nothing)
	// and the backward-compatible dual-path search.
	qb := esq.NewBuilder()
	appendSharedTagFilters(qb, query.Tags, query.TagsOr, query.TagsNot, query.TagsExists, query.TagsRegex)
	for _, clause := range qb.MustClauses() {
		must = append(must, clause)
	}

	if len(must) == 1 {
		return must[0]
	}
	return map[string]any{"bool": map[string]any{"must": must}}
}

// buildMetricsAggTree builds the nested aggregation tree for metrics with optional group-by.
func (r *TraceReader) buildMetricsAggTree(query TraceMetricsQuery, histogramAgg map[string]any, bucketAggName string) map[string]any {
	metricsAgg := r.buildMetricsSubAggregation(query)

	if len(query.ByLabels) == 0 {
		return map[string]any{
			bucketAggName: map[string]any{
				"histogram": histogramAgg,
				"aggs": map[string]any{
					"metric": metricsAgg,
				},
			},
		}
	}

	// Build nested terms aggregations bottom-up.
	outerAggs := map[string]any{
		bucketAggName: map[string]any{
			"histogram": histogramAgg,
			"aggs": map[string]any{
				"metric": metricsAgg,
			},
		},
	}

	resolver := &AttributeResolver{}
	for i := len(query.ByLabels) - 1; i >= 0; i-- {
		label := query.ByLabels[i]
		aggField := metricsAggField(resolver, label)
		outerAggs = map[string]any{
			"by_" + label: map[string]any{
				"terms": map[string]any{
					"field": aggField,
					// 100, not 1000: TraceQL metrics group-by labels are low-cardinality
					// (service.name, span.name, status, kind — at most a few hundred
					// distinct values). 1000 forced every shard to materialize 1000 buckets
					// × the histogram sub-buckets × every shard (16 indices × 3 shards =
					// 48 shards), which overflowed the 1.5GB heap node (parent circuit
					// breaker). 100 covers real cardinality with headroom.
					"size": 100,
				},
				"aggs": outerAggs,
			},
		}
	}

	return outerAggs
}

// defaultQuantiles matches Tempo's behaviour when quantile_over_time is called
// without explicit quantiles. Expressed as fractions, like TraceQL.
var defaultQuantiles = []float64{0.5, 0.95, 0.99}

// quantileToPercent converts a TraceQL quantile fraction (0.9) to the percentage
// scale ES expects (90). Values already on the 0-100 scale are passed through so
// that callers supplying percentages keep working.
func quantileToPercent(q float64) float64 {
	if q <= 1 {
		return q * 100
	}
	return q
}

// log2BucketCount bounds the generated power-of-two buckets: upper bounds run
// from 2^1 to 2^63, covering the full int64 nanosecond range.
const log2BucketCount = 64

// log2BucketRanges builds ES range-aggregation buckets whose upper bounds are
// successive powers of two, mirroring Tempo's Log2Bucketize. The bucket key is
// the upper bound in the field's own unit; extractMetricValues converts it to
// seconds for the __bucket label.
//
// Tempo's Log2Bucketize rounds a value up to the next power of two, so a value v
// belongs to the bucket with upper bound 2^ceil(log2(v)). Expressing that as
// half-open ranges (2^(n-1), 2^n] means each ES range is [from, to) shifted by
// one: we use from=2^(n-1)+1, to=2^n+1 to keep the inclusive upper bound.
//
// Buckets with no documents are dropped when parsing, so a query only emits
// series for durations that actually occurred.
func log2BucketRanges() []map[string]any {
	ranges := make([]map[string]any, 0, log2BucketCount)
	for n := 1; n < log2BucketCount; n++ {
		upper := uint64(1) << n
		lower := uint64(1) << (n - 1)
		ranges = append(ranges, map[string]any{
			"key":  strconv.FormatUint(upper, 10),
			"from": float64(lower) + 1,
			"to":   float64(upper) + 1,
		})
	}
	return ranges
}

// buildMetricsSubAggregation builds the sub-aggregation for the specific metrics function.
func (r *TraceReader) buildMetricsSubAggregation(query TraceMetricsQuery) map[string]any {
	switch query.Function {
	case "rate":
		// rate = count per bucket / bucket_seconds.
		// Count documents via value_count on the _doc pseudo-field, NOT _id.
		// Aggregating on _id forces ES to load every document's _id into
		// fielddata (hundreds of MB across the cluster — the parent circuit
		// breaker trips and bulk writes 429). _doc counts documents without
		// any fielddata; it is the ES-canonical "count docs in bucket".
		return map[string]any{
			"value_count": map[string]any{
				"field": "_doc",
			},
		}

	case "quantile_over_time":
		if len(query.Percentiles) == 0 {
			query.Percentiles = defaultQuantiles
		}
		// TraceQL expresses quantiles as fractions (0.9); ES "percents" wants
		// percentages (90). Without this conversion 0.9 requests the 0.9th
		// percentile — effectively the minimum — which silently produced
		// duration values ~500x too small.
		percs := make([]float64, 0, len(query.Percentiles))
		for _, p := range query.Percentiles {
			percs = append(percs, quantileToPercent(p))
		}
		return map[string]any{
			"percentiles": map[string]any{
				"field":    r.fieldForIntrinsic(query.Field),
				"percents": percs,
			},
		}

	case "histogram_over_time":
		// Tempo buckets each value into a log2 (power-of-two) bucket and emits one
		// series per bucket labeled __bucket, so Grafana can render a heatmap.
		// ES has no log-scale bucketing, so we use an explicit range aggregation
		// with power-of-two bounds — this avoids scripting (often disabled) and
		// reproduces Log2Bucketize exactly: a value lands in the first bucket
		// whose upper bound is >= it.
		return map[string]any{
			"range": map[string]any{
				"field":  r.fieldForIntrinsic(query.Field),
				"keyed":  true,
				"ranges": log2BucketRanges(),
			},
		}

	default:
		// See the "rate" case: value_count on _doc, never _id (fielddata blowup).
		return map[string]any{
			"value_count": map[string]any{"field": "_doc"},
		}
	}
}

// metricsAggField resolves the correct ES aggregation field for a by() label.
// Delegates to the per-signal aggregatableField helper (see field_type.go):
// intrinsic keyword/long fields and explicitly-mapped resource keyword fields
// keep no suffix; everything else (text fields from the dynamic template) gets
// a .keyword suffix so terms aggregation works.
//
// Trace metrics operate on the trace signal, so the trace aggregatable table
// is used.
//
// This covers all cases uniformly:
//   - intrinsic keyword: kind, name, spanId, status.code → no suffix
//   - intrinsic text: status.message → gets .keyword
//   - resource keyword: resource.host.name → no suffix
//   - resource text: resource.app_id, resource.service.instance.id → gets .keyword
//   - custom attributes: attributes.http.method, attributes.db.system → gets .keyword
func metricsAggField(resolver *AttributeResolver, label string) string {
	field := resolver.Resolve(label).ESField
	return aggregatableField("trace", field)
}

// metricsTermClause generates the correct ES match/term clause based on field type.
// Fields that need analyzed full-text matching (status.message) use "match";
// all others use "term". See needsMatchQuery in field_type.go.
func metricsTermClause(field, value string) map[string]any {
	if needsMatchQuery(field) {
		return map[string]any{"match": map[string]any{field: value}}
	}
	return map[string]any{"term": map[string]any{field: value}}
}

// fieldForIntrinsic maps a TraceQL intrinsic field name to the ES field name.
func (r *TraceReader) fieldForIntrinsic(name string) string {
	switch name {
	case "duration":
		return FieldDurationNano
	default:
		r.logger.Warn("trace metrics: unknown intrinsic field, falling back to duration",
			zap.String("field", name))
		return FieldDurationNano
	}
}

// parseMetricsResponse parses the ES aggregation response into time-series data.
func (r *TraceReader) parseMetricsResponse(resp *SearchResponse, query TraceMetricsQuery, bucketAggName string) (*TraceMetricsResult, error) {
	result := &TraceMetricsResult{}
	stepSeconds := query.Step.Seconds()

	if len(query.ByLabels) == 0 {
		// No group-by: single series.
		series, err := r.parseSingleSeries(resp.Aggregations, bucketAggName, query, stepSeconds)
		if err != nil {
			return nil, err
		}
		result.Series = series
		return result, nil
	}

	// With group-by: walk the terms tree.
	groupedSeries, err := r.parseGroupedSeries(resp.Aggregations, query.ByLabels, 0, nil, bucketAggName, query, stepSeconds)
	if err != nil {
		return nil, err
	}
	result.Series = groupedSeries
	return result, nil
}

// parseSingleSeries extracts the time series for one group from the time-bucket
// aggregation.
//
// rate/count_over_time yield exactly one series. quantile_over_time and
// histogram_over_time fan out: Tempo emits one series per quantile (labeled "p")
// and one per log2 duration bucket (labeled "__bucket") respectively, so the
// value extracted per time bucket is a map of sub-series keyed by that label.
func (r *TraceReader) parseSingleSeries(raw map[string]json.RawMessage, bucketAggName string, query TraceMetricsQuery, stepSeconds float64) ([]TraceMetricsSeries, error) {
	var agg struct {
		Buckets []struct {
			Key    float64         `json:"key"` // histogram returns float64 keys
			Metric json.RawMessage `json:"metric"`
		} `json:"buckets"`
	}

	bucketRaw, ok := raw[bucketAggName]
	if !ok {
		return nil, fmt.Errorf("bucket aggregation '%s' not found in response", bucketAggName)
	}
	if err := json.Unmarshal(bucketRaw, &agg); err != nil {
		return nil, fmt.Errorf("parse histogram: %w", err)
	}

	// Determine source unit for duration normalization (once per series, not per point).
	sourceUnit := unitconv.SourceUnitForTraceReader(query.Function, query.Field)

	// splitLabel is the label that fans a single group out into several series.
	// Empty for scalar functions, which keep the single-series shape.
	splitLabel := seriesSplitLabel(query.Function)

	// Accumulate points per sub-series. A bucket/quantile only appears in the
	// time buckets where it has data, so the set is built up across the loop and
	// sorted at the end rather than relying on first-seen order.
	byKey := make(map[string][]TraceMetricsPoint)
	sortKeys := make(map[string]float64)

	for _, b := range agg.Buckets {
		// Bucket key is in nanoseconds (histogram on long field returns float64).
		// Convert to milliseconds for Grafana consumption.
		tsMs := int64(b.Key) / 1_000_000

		vals, err := r.extractMetricValues(b.Metric, query)
		if err != nil {
			r.logger.Warn("trace metrics: skip bucket value", zap.Float64("bucket_ts", b.Key), zap.Error(err))
			continue
		}

		for _, kv := range vals {
			val := kv.value
			// For rate, divide by step interval.
			if query.Function == "rate" && stepSeconds > 0 {
				val = val / stepSeconds
			}
			// Normalize duration units to seconds (Tempo protocol standard).
			// For non-duration functions, sourceUnit is DurationUnitNone → no-op.
			val = unitconv.ToSeconds(val, sourceUnit)

			sortKeys[kv.key] = kv.sortKey
			byKey[kv.key] = append(byKey[kv.key], TraceMetricsPoint{
				TimestampMs: tsMs,
				Value:       math.Round(val*1e6) / 1e6, // 6 decimal places for sub-ms precision in seconds
			})
		}
	}

	if splitLabel == "" {
		// Scalar function: single unlabeled series (missing key == "").
		return []TraceMetricsSeries{{Values: byKey[""]}}, nil
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	// Order by the label's numeric value: Grafana's heatmap reads bucket bounds
	// off the series in order, and quantile series should read p50, p90, p99.
	sort.Slice(keys, func(i, j int) bool { return sortKeys[keys[i]] < sortKeys[keys[j]] })

	series := make([]TraceMetricsSeries, 0, len(keys))
	for _, k := range keys {
		series = append(series, TraceMetricsSeries{
			Labels: map[string]string{splitLabel: k},
			Values: byKey[k],
		})
	}
	return series, nil
}

// seriesSplitLabel returns the label a metrics function fans out on, matching
// Tempo's internal label names, or "" for functions producing a single series.
func seriesSplitLabel(function string) string {
	switch function {
	case "quantile_over_time":
		return "p"
	case "histogram_over_time":
		return "__bucket"
	default:
		return ""
	}
}

// parseGroupedSeries walks the terms aggregation tree to extract labeled time series.
func (r *TraceReader) parseGroupedSeries(raw map[string]json.RawMessage, byLabels []string, depth int, parentLabels map[string]string, bucketAggName string, query TraceMetricsQuery, stepSeconds float64) ([]TraceMetricsSeries, error) {
	if depth >= len(byLabels) {
		// Leaf: extract the actual timeseries.
		series, err := r.parseSingleSeries(raw, bucketAggName, query, stepSeconds)
		if err != nil {
			return nil, err
		}
		for i := range series {
			if series[i].Labels == nil {
				series[i].Labels = make(map[string]string)
			}
			for k, v := range parentLabels {
				series[i].Labels[k] = v
			}
		}
		return series, nil
	}

	labelKey := byLabels[depth]
	byName := "by_" + labelKey

	byRaw, ok := raw[byName]
	if !ok {
		r.logger.Warn("trace metrics: group-by aggregation not found",
			zap.String("agg_name", byName), zap.Int("depth", depth))
		return nil, nil
	}

	// Parse the terms aggregation, preserving the inner raw message.
	var termsRaw struct {
		Buckets []json.RawMessage `json:"buckets"`
	}
	if err := json.Unmarshal(byRaw, &termsRaw); err != nil {
		return nil, fmt.Errorf("parse terms at depth %d: %w", depth, err)
	}

	var allSeries []TraceMetricsSeries
	for _, bucketRaw := range termsRaw.Buckets {
		var bucket map[string]json.RawMessage
		if err := json.Unmarshal(bucketRaw, &bucket); err != nil {
			continue
		}

		// Parse the bucket key. ES terms aggregation returns keys as their
		// native JSON type: string for keyword/text fields, number for long/
		// integer fields. Use interface{} to handle both.
		var bucketMeta struct {
			RawKey interface{} `json:"key"`
		}
		if err := json.Unmarshal(bucketRaw, &bucketMeta); err != nil {
			continue
		}
		keyStr := fmt.Sprintf("%v", bucketMeta.RawKey)

		// Merge parent labels with current bucket label.
		labels := make(map[string]string, len(parentLabels)+1)
		for k, v := range parentLabels {
			labels[k] = v
		}
		labels[labelKey] = keyStr

		// Recurse with the bucket's nested aggregations.
		series, err := r.parseGroupedSeries(bucket, byLabels, depth+1, labels, bucketAggName, query, stepSeconds)
		if err != nil {
			r.logger.Warn("trace metrics: error parsing grouped series",
				zap.Error(err), zap.String("label", labelKey), zap.String("value", keyStr))
			continue
		}
		allSeries = append(allSeries, series...)
	}

	return allSeries, nil
}

// metricValue is one extracted value plus the sub-series key it belongs to.
// key is "" for scalar functions, the quantile for quantile_over_time, and the
// bucket upper bound (in seconds) for histogram_over_time.
//
// sortKey is the key's numeric value, kept separately because the label is
// formatted with %g and can come out in scientific notation ("3.2768e-05"),
// where lexical ordering would not match numeric ordering.
type metricValue struct {
	key     string
	sortKey float64
	value   float64
}

// extractMetricValues reads the metric value(s) from a sub-aggregation result.
// The raw input is the JSON object for the "metric" sub-aggregation within a
// time bucket, e.g. {"value": 42} for value_count,
// {"values": {"50.0": 123, "95.0": 456}} for percentiles, or
// {"buckets": {"1024": {"doc_count": 7}, ...}} for the keyed range aggregation.
//
// quantile_over_time and histogram_over_time return one entry per sub-series;
// scalar functions return a single entry keyed "".
func (r *TraceReader) extractMetricValues(raw json.RawMessage, query TraceMetricsQuery) ([]metricValue, error) {
	switch query.Function {
	case "quantile_over_time":
		// ES percentiles returns {"values": {"50.0": 123, "95.0": 456}}.
		// Emit one series per requested quantile, keyed by the TraceQL fraction
		// so the label reads p=0.9 rather than p=90.
		var result struct {
			Values map[string]float64 `json:"values"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("unmarshal percentiles: %w", err)
		}
		quantiles := query.Percentiles
		if len(quantiles) == 0 {
			quantiles = defaultQuantiles
		}
		out := make([]metricValue, 0, len(result.Values))
		for pct, v := range result.Values {
			p, err := strconv.ParseFloat(pct, 64)
			if err != nil {
				continue
			}
			if math.IsNaN(v) {
				// ES reports NaN for empty buckets; report 0 so the series stays contiguous.
				v = 0
			}
			q := quantileFor(p, quantiles)
			out = append(out, metricValue{key: formatSeriesKey(q), sortKey: q, value: v})
		}
		// Map iteration is random — sort so series order is deterministic.
		sort.Slice(out, func(i, j int) bool { return out[i].sortKey < out[j].sortKey })
		return out, nil

	case "histogram_over_time":
		// Keyed range aggregation returns
		// {"buckets": {"1024": {"doc_count": 7}, "2048": {...}}}.
		// Each key is the bucket's power-of-two upper bound in the field's unit
		// (nanoseconds for duration); Tempo labels buckets in seconds.
		var result struct {
			Buckets map[string]struct {
				DocCount float64 `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("unmarshal range buckets: %w", err)
		}
		out := make([]metricValue, 0, len(result.Buckets))
		for bound, b := range result.Buckets {
			if b.DocCount == 0 {
				continue
			}
			v := bucketBoundValue(bound, query.Field)
			out = append(out, metricValue{
				key:     formatSeriesKey(v),
				sortKey: v,
				value:   b.DocCount,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].sortKey < out[j].sortKey })
		return out, nil

	default:
		// rate / count_over_time and any unknown function use value_count,
		// which returns {"value": <number>}.
		var result struct {
			Value float64 `json:"value"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			if query.Function == "rate" {
				return nil, fmt.Errorf("unmarshal value_count: %w", err)
			}
			return []metricValue{{value: 0}}, nil
		}
		return []metricValue{{value: result.Value}}, nil
	}
}

// quantileFor maps an ES percentile key back to the quantile the caller asked
// for.
//
// ES echoes the percent back as a string ("99.9"), but dividing that by 100
// reintroduces binary float error — 99.9/100 is 0.9990000000000001, which would
// surface in the label. Matching against the requested quantiles keeps the label
// exactly as the user wrote it; the divided form is only a fallback.
func quantileFor(percent float64, requested []float64) float64 {
	for _, q := range requested {
		if math.Abs(quantileToPercent(q)-percent) < 1e-9 {
			return q
		}
	}
	return percent / 100
}

// formatSeriesKey renders a fan-out label value without trailing zeros, so the
// label reads p=0.9 rather than p=0.900000.
func formatSeriesKey(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// bucketBoundValue converts a range-aggregation bucket bound into the numeric
// __bucket value. Duration bounds are nanoseconds and Tempo labels buckets in
// seconds (see bucketizeDuration in Tempo's ast_metrics.go); other fields are
// reported in their own unit.
func bucketBoundValue(bound, field string) float64 {
	v, err := strconv.ParseFloat(bound, 64)
	if err != nil {
		return 0
	}
	if field == "duration" {
		v /= float64(time.Second)
	}
	return v
}
