// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

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
		Query: baseFilter,
		Size:  0,
		Aggregations: searchAggs,
	}

	indexPat := r.indexPattern(query.AppID)
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
		// ES stores status.code values as-is (lowercase from OTel enum String()).
		// No capitalizeFirst needed — verified with ES: 277K lowercase "error" docs.
		must = append(must, map[string]any{"term": map[string]any{FieldStatus + ".code": query.Status}})
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

	resolver := &AttributeResolver{}
	for k, v := range query.Tags {
		field := resolver.Resolve(k).ESField
		must = append(must, metricsTermClause(field, v))
	}

	// Handle TagsOr: each group becomes a should, groups are AND-ed together.
	for _, orGroup := range query.TagsOr {
		should := []map[string]any{}
		for _, branch := range orGroup {
			branchMust := []map[string]any{}
			for k, v := range branch {
				field := resolver.Resolve(k).ESField
				branchMust = append(branchMust, metricsTermClause(field, v))
			}
			if len(branchMust) == 1 {
				should = append(should, branchMust[0])
			} else if len(branchMust) > 0 {
				should = append(should, map[string]any{"bool": map[string]any{"must": branchMust}})
			}
		}
		if len(should) > 0 {
			must = append(must, map[string]any{
				"bool": map[string]any{"should": should, "minimum_should_match": 1},
			})
		}
	}

	// Handle TagsNot (Sprint 2): != value → must_not + term.
	for k, v := range query.TagsNot {
		field := resolver.Resolve(k).ESField
		must = append(must, map[string]any{
			"bool": map[string]any{
				"must_not": []map[string]any{{"term": map[string]any{field: v}}},
			},
		})
	}

	// Handle TagsExists (Sprint 2): != nil → exists.
	for _, k := range query.TagsExists {
		field := resolver.Resolve(k).ESField
		must = append(must, map[string]any{
			"exists": map[string]any{"field": field},
		})
	}

	// Handle TagsRegex (Sprint 2): =~ regex → regexp query.
	for k, pattern := range query.TagsRegex {
		field := resolver.Resolve(k).ESField
		must = append(must, map[string]any{
			"regexp": map[string]any{
				field: map[string]any{
					"value": pattern,
				},
			},
		})
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
					"size":  1000,
				},
				"aggs": outerAggs,
			},
		}
	}

	return outerAggs
}

// buildMetricsSubAggregation builds the sub-aggregation for the specific metrics function.
func (r *TraceReader) buildMetricsSubAggregation(query TraceMetricsQuery) map[string]any {
	switch query.Function {
	case "rate":
		// rate = count per bucket / bucket_seconds
		// Use value_count which counts documents in each bucket.
		return map[string]any{
			"value_count": map[string]any{
				"field": "_id",
			},
		}

	case "quantile_over_time":
		if len(query.Percentiles) == 0 {
			query.Percentiles = []float64{50, 95, 99}
		}
		// ES percentiles expects values in [0, 100].
		var percs []float64
		for _, p := range query.Percentiles {
			percs = append(percs, p)
		}
		return map[string]any{
			"percentiles": map[string]any{
				"field":    r.fieldForIntrinsic(query.Field),
				"percents": percs,
			},
		}

	case "histogram_over_time":
		// For histogram_over_time, each date_histogram bucket already has doc_count.
		// Use value_count on the duration field to get count per bucket.
		// Grafana Tempo displays this as a time-series of counts (simplified heatmap).
		return map[string]any{
			"value_count": map[string]any{
				"field": r.fieldForIntrinsic(query.Field),
			},
		}

	default:
		return map[string]any{
			"value_count": map[string]any{"field": "_id"},
		}
	}
}

// metricsAggField resolves the correct ES aggregation field for a by() label.
// Strategy: instead of listing fields that need .keyword, we maintain a set of
// known keyword/long fields that DON'T need .keyword. Everything else is assumed
// to be a dynamic text field (via ES dynamic template) and gets .keyword suffix.
//
// This covers all three cases uniformly:
//   - intrinsic keyword: kind, name, spanId → no suffix
//   - intrinsic text: status.code, status.message → gets .keyword
//   - resource keyword: resource.host.name → no suffix
//   - resource text: resource.app_id, resource.service.instance.id → gets .keyword
//   - custom attributes: attributes.http.method, attributes.db.system → gets .keyword
func metricsAggField(resolver *AttributeResolver, label string) string {
	field := resolver.Resolve(label).ESField

	// Known keyword/long/numeric fields from the ES index template.
	// These support aggregation directly without .keyword suffix.
	if knownAggregatableFields[field] {
		return field
	}
	// Default: text field from dynamic template → must use .keyword for aggregation.
	return field + ".keyword"
}

// knownAggregatableFields lists ES fields that are explicitly mapped as keyword,
// long, or other aggregatable types (not text) in the index template.
// Any field NOT in this set is assumed to be a dynamic text field requiring
// .keyword suffix for terms aggregation.
var knownAggregatableFields = map[string]bool{
	// Intrinsic top-level fields (from admin.go template).
	FieldKind:                true, // keyword
	FieldName:                true, // keyword
	FieldSpanID:              true, // keyword
	FieldTraceID:             true, // keyword
	FieldParentSpanID:        true, // keyword
	FieldServiceName:         true, // keyword
	FieldStartTimeUnixNano:   true, // long
	FieldEndTimeUnixNano:     true, // long
	FieldDurationNano:        true, // long

	// Dynamic numeric fields — discovered via ES mapping validation.
	// These resolve via ES dot-nesting to numeric sub-fields (e.g.
	// attributes.thread.id → attributes.thread{object} → id{long}).
	// They have NO .keyword sub-field → must NOT add .keyword suffix.
	FieldAttributes + ".thread.id": true, // long

	// resource.* fields explicitly mapped as keyword in template.
	FieldResource + ".service.name":      true,
	FieldResource + ".host.name":         true,
	FieldResource + ".service.namespace": true,
	FieldResource + ".service.version":   true,
	FieldResource + ".process.pid":       true,
}

// metricsTermClause generates the correct ES match/term clause based on field type.
// Text fields (status.message) must use match query instead of term.
func metricsTermClause(field, value string) map[string]any {
	if field == FieldStatus+".message" {
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

// parseSingleSeries extracts a single time series from the histogram aggregation.
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

	var values []TraceMetricsPoint
	for _, b := range agg.Buckets {
		val, err := r.extractMetricValue(b.Metric, query)
		if err != nil {
			r.logger.Warn("trace metrics: skip bucket value", zap.Float64("bucket_ts", b.Key), zap.Error(err))
			continue
		}
		// For rate, divide by step interval.
		if query.Function == "rate" && stepSeconds > 0 {
			val = val / stepSeconds
		}
		// Normalize duration units to seconds (Tempo protocol standard).
		// For non-duration functions, sourceUnit is DurationUnitNone → no-op.
		val = unitconv.ToSeconds(val, sourceUnit)

		// Bucket key is in nanoseconds (histogram on long field returns float64).
		// Convert to milliseconds for Grafana consumption.
		tsMs := int64(b.Key) / 1_000_000
		values = append(values, TraceMetricsPoint{
			TimestampMs: tsMs,
			Value:       math.Round(val*1e6) / 1e6, // 6 decimal places for sub-ms precision in seconds
		})
	}

	return []TraceMetricsSeries{{Values: values}}, nil
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

// extractMetricValue reads the metric value from a sub-aggregation result.
// The raw input is the JSON object for the "metric" sub-aggregation within a
// date_histogram bucket, e.g. {"value": 42} for value_count, or
// {"values": {"50.0": 123, "95.0": 456}} for percentiles.
func (r *TraceReader) extractMetricValue(raw json.RawMessage, query TraceMetricsQuery) (float64, error) {
	switch query.Function {
	case "rate", "histogram_over_time":
		// Both use value_count which returns {"value": <number>}.
		var result struct {
			Value float64 `json:"value"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return 0, fmt.Errorf("unmarshal value_count: %w", err)
		}
		return result.Value, nil

	case "quantile_over_time":
		// ES percentiles returns {"values": {"50.0": 123, "95.0": 456}}.
		var result struct {
			Values map[string]float64 `json:"values"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return 0, fmt.Errorf("unmarshal percentiles: %w", err)
		}
		if len(result.Values) > 0 {
			var sum float64
			for _, v := range result.Values {
				sum += v
			}
			return sum / float64(len(result.Values)), nil
		}
		return 0, nil

	default:
		// Fallback: try to extract "value" field.
		var result struct {
			Value float64 `json:"value"`
		}
		if err := json.Unmarshal(raw, &result); err == nil {
			return result.Value, nil
		}
		return 0, nil
	}
}
