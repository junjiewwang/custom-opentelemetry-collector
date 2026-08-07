// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// MetricReader implements metric query operations against Elasticsearch.
// Metrics are stored as per-datapoint documents with fields:
//
//	timeUnixMilli, name, type, serviceName, value, labels, resource
type MetricReader struct {
	searcher Searcher
	config   *Config
	logger   *zap.Logger
}

// NewMetricReader creates a new MetricReader instance.
func NewMetricReader(searcher Searcher, config *Config, logger *zap.Logger) *MetricReader {
	return &MetricReader{
		searcher: searcher,
		config:   config,
		logger:   logger.Named("metric-reader"),
	}
}

// Query executes an instant metric query, returning the latest value(s) before the given time.
// AppID is optional: when empty, queries all app indices (admin mode).
//
// Returns one data point per distinct label set (the latest sample for each
// series), matching the PromQL instant-vector contract consumed by the
// Prometheus handler. Grouping is done in Go (not via an ES terms aggregation
// on the "labels" object field, which ES silently returns empty for — object
// fields have no terms to aggregate).
func (r *MetricReader) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	// Use buildMetricFilter for consistent label/labelMatch handling across all query paths.
	var timeRange TimeRange
	if !query.Time.IsZero() {
		timeRange = TimeRange{End: query.Time}
	}
	filterResult := r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, timeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
	esQuery := filterResult.Query

	// Fetch matching documents sorted newest-first, then dedupe by label set in
	// Go. top_hits cannot group by an arbitrary object field, and a terms agg on
	// "labels" returns no buckets. Size is a per-query cap on documents scanned
	// for dedup; it bounds the result series count (latest-per-series) and the
	// worst-case scan when many series exist.
	const instantScanSize = 500
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  instantScanSize,
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
		Source: []string{
			FieldMetricTimeUnixMilli, FieldMetricValue, FieldMetricLabels,
			FieldMetricBucketCounts, FieldMetricExplicitBounds,
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric query failed: %w", err)
	}

	// Dedupe by label set: since hits are newest-first, the first hit seen for
	// a given label set is that series' latest sample.
	seen := make(map[string]struct{}, len(resp.Hits.Hits))
	result := &MetricResult{Data: make([]MetricDataPoint, 0, len(resp.Hits.Hits))}
	for _, hit := range resp.Hits.Hits {
		dp := r.hitToDataPoint(hit)
		key := labelsKey(dp.Labels)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result.Data = append(result.Data, dp)
	}

	// Post-filter for complex regex patterns unsupported by ES flattened fields.
	result.Data = postFilterDataPoints(result.Data, filterResult.PostFilters)

	return result, nil
}

// labelsKey returns a stable string key for a metric label set, for dedup.
// Empty/nil labels map to "". Keys are sorted for determinism.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// queryDirect was removed: Query no longer uses an object-field aggregation
// that could fail, so the direct-search fallback is unnecessary. Query itself
// now fetches newest-first and dedupes by label set in Go.

// QueryRange executes a range metric query, returning time series data.
// AppID is optional: when empty, queries all app indices (admin mode).
//
// Supports InfluxQL-aligned semantics:
//   - aggregation: avg, sum, max, min, count, last, first, p50, p90, p95, p99
//   - groupBy: composite aggregation by label keys
//   - fill: null, none, 0, previous, linear
//   - seriesLimit: max number of series to return
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	// 1. Validate and get the aggregation function.
	aggFunc, err := GetAggregation(query.Aggregation)
	if err != nil {
		return nil, err
	}

	// 2. Build ES query filter (metric name + labels + labelMatch + service + time range).
	filterResult := r.buildQueryFilter(query)
	esQuery := filterResult.Query

	// 3. Calculate interval for date_histogram.
	interval := r.calculateInterval(query.TimeRange, query.Step)

	// 4. Determine min_doc_count based on fill strategy.
	minDocCount := 0
	if query.Fill == "none" {
		minDocCount = 1
	}

	// 5. Build ES aggregations (with or without groupBy).
	aggs := r.buildAggregation(query.GroupBy, interval, aggFunc, minDocCount, query.SeriesLimit, query.MissingBucket)

	searchReq := &SearchRequest{
		Query:        esQuery,
		Size:         0,
		Aggregations: aggs,
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		if strings.Contains(err.Error(), "too_many_buckets") {
			return nil, fmt.Errorf("metric range query: time range too large for the given step, try a larger step or shorter time range")
		}
		return nil, fmt.Errorf("metric range query failed: %w", err)
	}

	// 6. Parse the result (simple or grouped).
	result, err := r.parseQueryRangeResult(resp, len(query.GroupBy) > 0, aggFunc)
	if err != nil {
		return nil, err
	}

	// 7. Post-filter series for unsupported regex patterns.
	result.Data = postFilterSeries(result.Data, filterResult.PostFilters)

	// 8. Apply fill strategy (post-processing).
	fillFn := GetFillStrategy(query.Fill)
	for i := range result.Data {
		result.Data[i].Values = fillFn(result.Data[i].Values)
	}

	// 9. Normalize labels (ensure non-nil).
	for i := range result.Data {
		if result.Data[i].Labels == nil {
			result.Data[i].Labels = make(map[string]string)
		}
	}

	return result, nil
}

// ListMetricNames returns all available metric names within the time range.
func (r *MetricReader) ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"metric_names": map[string]any{
				"terms": map[string]any{
					"field": FieldName,
					"size":  5000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list metric names failed: %w", err)
	}

	raw, ok := resp.Aggregations["metric_names"]
	if !ok {
		return nil, nil
	}
	names, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metric_names aggregation: %w", err)
	}
	return names, nil
}

// ListMetricTypes returns each metric name mapped to its stored OTel-derived
// type ("gauge", "counter", "histogram", "summary").
//
// The type is written per data point by storedmodel (Sum → counter, Gauge →
// gauge, ...), so a terms sub-aggregation recovers it without scanning
// documents. A metric that somehow carries more than one type reports the most
// frequent one, which is what the terms ordering yields first.
func (r *MetricReader) ListMetricTypes(ctx context.Context, timeRange TimeRange) (map[string]string, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"metric_names": map[string]any{
				"terms": map[string]any{
					"field": FieldName,
					"size":  5000,
				},
				"aggs": map[string]any{
					"metric_type": map[string]any{
						"terms": map[string]any{
							"field": FieldMetricType,
							"size":  1,
						},
					},
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list metric types failed: %w", err)
	}

	raw, ok := resp.Aggregations["metric_names"]
	if !ok {
		return nil, nil
	}

	var agg struct {
		Buckets []struct {
			Key        string `json:"key"`
			MetricType struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"metric_type"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse metric_names aggregation: %w", err)
	}

	out := make(map[string]string, len(agg.Buckets))
	for _, b := range agg.Buckets {
		if len(b.MetricType.Buckets) > 0 {
			out[b.Key] = b.MetricType.Buckets[0].Key
		} else {
			out[b.Key] = ""
		}
	}
	return out, nil
}

// ListLabelNames returns label names for the specified metric within the time range.
// If metricName is empty, all label names across all metrics are returned.
func (r *MetricReader) ListLabelNames(ctx context.Context, timeRange TimeRange, metricName string) ([]string, error) {
	// Sampling the newest documents is a heuristic, not an exhaustive scan: the
	// label set is the union of whatever these hits happen to carry. Keep the
	// sample large, because the unscoped call competes across every metric and
	// the highest-frequency writers otherwise crowd out rarer labels entirely —
	// the unscoped list came back MISSING labels that the metric-scoped list
	// returned. A larger sample narrows that gap; it cannot close it.
	const labelSampleSize = 2000
	searchReq := &SearchRequest{
		Query:  r.timeRangeQuery(timeRange),
		Size:   labelSampleSize,
		Source: []string{FieldMetricLabels},
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
	}

	// Filter by metric name if specified.
	if metricName != "" {
		searchReq.Query = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					searchReq.Query,
					{"term": map[string]any{FieldName: metricName}},
				},
			},
		}
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list label names failed: %w", err)
	}

	labelSet := make(map[string]struct{})
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			Labels map[string]any `json:"labels"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err == nil {
			for k := range doc.Labels {
				labelSet[k] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(labelSet))
	for k := range labelSet {
		names = append(names, k)
	}
	return names, nil
}

// ListLabelValues returns values for a specific label within the time range,
// across all metrics.
func (r *MetricReader) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	return r.ListLabelValuesForMetric(ctx, label, "", timeRange)
}

// ListLabelValuesForMetric returns values for a specific label, restricted to a
// single metric. An empty metricName means "across all metrics".
//
// Scoping matters for Prometheus /label/{name}/values?match[]=...: Grafana's
// breakdown UI uses it to populate the value picker, and an unscoped list offers
// values that exist on some other metric entirely and yield an empty panel.
func (r *MetricReader) ListLabelValuesForMetric(ctx context.Context, label, metricName string, timeRange TimeRange) ([]string, error) {
	// Metric labels are dynamic text+keyword fields — terms aggregation
	// requires the .keyword sub-field (aggregating on the bare text field is
	// rejected by ES with "Text fields are not optimised for aggregations").
	fieldPath := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", storedmodel.SanitizeMetricKey(label)))

	esQuery := r.timeRangeQuery(timeRange)
	if metricName != "" {
		esQuery = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					esQuery,
					{"term": map[string]any{FieldName: metricName}},
				},
			},
		}
	}

	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Aggregations: map[string]any{
			"label_values": map[string]any{
				"terms": map[string]any{
					"field": fieldPath,
					"size":  1000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list label values failed: %w", err)
	}

	raw, ok := resp.Aggregations["label_values"]
	if !ok {
		return nil, nil
	}
	values, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label_values aggregation: %w", err)
	}
	return values, nil
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for metrics.
// When appID is provided, returns an app-scoped pattern; otherwise falls back to global wildcard.
func (r *MetricReader) indexPattern(appID ...string) string {
	id := ""
	if len(appID) > 0 {
		id = appID[0]
	}
	return esq.IndexPattern(r.config.Metrics.IndexPrefix, id)
}

// buildMetricQuery constructs the ES query for metric search.
func (r *MetricReader) buildMetricQuery(metricName string, labels map[string]string, serviceName string) map[string]any {
	qb := esq.NewBuilder()

	if metricName != "" {
		qb.Term(FieldName, metricName)
	}
	if serviceName != "" {
		qb.Term(FieldServiceName, serviceName)
	}
	for k, v := range labels {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		qb.Term(field, v)
	}

	return qb.Build()
}

// buildQueryFilter builds the complete ES bool query for a MetricRangeQuery,
// including metric name, service, labels, labelMatch (regex), and time range.
// Uses buildMetricFilter for consistent regex→ES query translation.
func (r *MetricReader) buildQueryFilter(query MetricRangeQuery) metricFilterResult {
	return r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
}

// buildAggregation constructs the ES aggregation for metric range queries.
// Uses simple date_histogram when groupBy is empty, or composite+date_histogram when grouping.
// missingBucket controls whether documents lacking a grouped label are dropped
// (false, for explicit "by (label)" queries) or included as null-key buckets
// (true, for bare-metric selectors that must return every series).
func (r *MetricReader) buildAggregation(groupBy []string, interval string, aggFunc *AggregationFunc, minDocCount int, seriesLimit int, missingBucket bool) map[string]any {
	// The sub-aggregation for each time bucket.
	timeAgg := map[string]any{
		"date_histogram": map[string]any{
			"field":          FieldMetricTimeUnixMilli,
			"fixed_interval": interval,
			"min_doc_count":  minDocCount,
		},
		"aggs": map[string]any{
			"agg_value": aggFunc.Build(FieldMetricValue),
		},
	}

	if len(groupBy) == 0 {
		// Simple case: single time_series aggregation.
		return map[string]any{"time_series": timeAgg}
	}

	// Grouped case: composite aggregation by label keys.
	if seriesLimit <= 0 {
		seriesLimit = 100
	}

	sources := make([]map[string]any, 0, len(groupBy))
	for _, label := range groupBy {
		esKey := translateLabelKey(label)
		// Metric labels are text fields from ES dynamic template — must use
		// .keyword sub-field for terms/composite aggregation.
		aggField := aggregatableField("metric", fmt.Sprintf("%s.%s", FieldMetricLabels, esKey))
		sources = append(sources, map[string]any{
			label: map[string]any{
				"terms": map[string]any{
					"field": aggField,
					// missing_bucket=false (explicit "by (label)"): documents lacking
					// this label are dropped — matching PromQL "by" semantics.
					// missing_bucket=true (bare-metric selector): documents lacking
					// this label fall into a null-key bucket — every series appears
					// regardless of which labels it has.
					"missing_bucket": missingBucket,
				},
			},
		})
	}

	return map[string]any{
		"by_group": map[string]any{
			"composite": map[string]any{
				"size":    seriesLimit,
				"sources": sources,
			},
			"aggs": map[string]any{
				"time_series": timeAgg,
			},
		},
	}
}

// parseQueryRangeResult parses the ES aggregation response into a MetricRangeResult.
func (r *MetricReader) parseQueryRangeResult(resp *SearchResponse, grouped bool, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	if grouped {
		return r.parseGroupedResult(resp, aggFunc)
	}
	return r.parseSimpleResult(resp, aggFunc)
}

// parseSimpleResult parses a non-grouped date_histogram aggregation.
// Includes all buckets (including empty ones with NilValue sentinel) so fill strategies work.
func (r *MetricReader) parseSimpleResult(resp *SearchResponse, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	result := &MetricRangeResult{}

	raw, ok := resp.Aggregations["time_series"]
	if !ok {
		return result, nil
	}

	var agg struct {
		Buckets []struct {
			Key      int64           `json:"key"`
			AggValue json.RawMessage `json:"agg_value"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return result, nil
	}

	series := MetricSeries{
		Labels: make(map[string]string),
		Values: make([]MetricDataPoint, 0, len(agg.Buckets)),
	}
	for _, b := range agg.Buckets {
		v := aggFunc.ParseValue(b.AggValue)
		dp := MetricDataPoint{
			Labels: make(map[string]string),
			Time:   time.UnixMilli(b.Key),
		}
		if v != nil {
			dp.Value = *v
		} else {
			dp.Value = NilValue // sentinel for empty bucket
		}
		series.Values = append(series.Values, dp)
	}
	result.Data = append(result.Data, series)

	return result, nil
}

// parseGroupedResult parses a composite + date_histogram aggregation response.
// Includes all buckets (including empty ones with NilValue sentinel) so fill strategies work.
func (r *MetricReader) parseGroupedResult(resp *SearchResponse, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	result := &MetricRangeResult{}

	raw, ok := resp.Aggregations["by_group"]
	if !ok {
		return result, nil
	}

	var composite struct {
		Buckets []struct {
			Key        map[string]any `json:"key"`
			TimeSeries struct {
				Buckets []struct {
					Key      int64           `json:"key"`
					AggValue json.RawMessage `json:"agg_value"`
				} `json:"buckets"`
			} `json:"time_series"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &composite); err != nil {
		return result, nil
	}

	for _, group := range composite.Buckets {
		// Extract labels from composite key.
		labels := make(map[string]string)
		for k, v := range group.Key {
			if v != nil {
				labels[k] = fmt.Sprintf("%v", v)
			}
		}

		series := MetricSeries{
			Labels: labels,
			Values: make([]MetricDataPoint, 0, len(group.TimeSeries.Buckets)),
		}
		for _, b := range group.TimeSeries.Buckets {
			v := aggFunc.ParseValue(b.AggValue)
			dp := MetricDataPoint{
				Labels: labels,
				Time:   time.UnixMilli(b.Key),
			}
			if v != nil {
				dp.Value = *v
			} else {
				dp.Value = NilValue // sentinel for empty bucket
			}
			series.Values = append(series.Values, dp)
		}
		result.Data = append(result.Data, series)
	}

	return result, nil
}

// timeRangeQuery returns a millisecond-precision time range filter for metrics.
// Uses TimeRangeFilterMilli because metric fields are stored as ES date type with epoch_millis format.
func (r *MetricReader) timeRangeQuery(tr TimeRange) map[string]any {
	return esq.TimeRangeFilterMilli(FieldMetricTimeUnixMilli, tr)
}

// calculateInterval determines the appropriate histogram interval,
// ensuring bucket count stays within ES max_buckets limit.
// Delegates to esq.SafeInterval which implements clamping when a user-
// specified step would produce too many buckets.
func (r *MetricReader) calculateInterval(tr TimeRange, step time.Duration) string {
	duration := time.Duration(0)
	if !tr.Start.IsZero() && !tr.End.IsZero() {
		duration = tr.End.Sub(tr.Start)
	}

	interval, clamped := esq.SafeInterval(esq.BucketParams{
		Duration:   duration,
		Step:       step,
		MaxBuckets: esq.DefaultMaxBuckets,
	})

	if clamped {
		r.logger.Warn("metric range query step clamped to avoid too_many_buckets",
			zap.Duration("original_step", step),
			zap.String("clamped_interval", interval),
			zap.Duration("duration", duration),
			zap.Int("max_buckets", esq.DefaultMaxBuckets),
		)
	}

	return interval
}

// hitToDataPoint converts an ES search hit to a MetricDataPoint.
func (r *MetricReader) hitToDataPoint(hit SearchHit) MetricDataPoint {
	var doc struct {
		TimeUnixMilli  int64             `json:"timeUnixMilli"`
		Value          float64           `json:"value"`
		Labels         map[string]string `json:"labels"`
		BucketCounts   []int64           `json:"bucket_counts"`
		ExplicitBounds []float64         `json:"explicit_bounds"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricDataPoint{}
	}

	return MetricDataPoint{
		Labels:         doc.Labels,
		Value:          doc.Value,
		Time:           time.UnixMilli(doc.TimeUnixMilli),
		BucketCounts:   doc.BucketCounts,
		ExplicitBounds: doc.ExplicitBounds,
	}
}

// hitToSample converts an ES search hit to a MetricSample with labels.
// Used by QueryFlat to return samples with their original labels for Go-side grouping.
func (r *MetricReader) hitToSample(hit SearchHit) MetricSample {
	var doc struct {
		TimeUnixMilli  int64             `json:"timeUnixMilli"`
		Value          float64           `json:"value"`
		Labels         map[string]string `json:"labels"`
		BucketCounts   []int64           `json:"bucket_counts"`
		ExplicitBounds []float64         `json:"explicit_bounds"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricSample{}
	}

	return MetricSample{
		TimestampMs:  doc.TimeUnixMilli,
		Value:        doc.Value,
		BucketCounts: doc.BucketCounts,
		Bounds:       doc.ExplicitBounds,
		Labels:       doc.Labels,
	}
}

// QueryRaw returns raw sample points for series matching the criteria.
// Unlike QueryRange which returns aggregated buckets, QueryRaw returns
// original data points (sorted by time ASC) for PromQL functions like
// rate() and increase() that need the original sample sequence.
//
// Uses ES composite aggregation to group by label set, then top_hits
// within each group to retrieve individual sample points.
func (r *MetricReader) QueryRaw(ctx context.Context, query MetricRawQuery) ([]MetricRawSeries, error) {
	// 1. Build ES filter (metric name + labels + labelMatch + service + time range).
	filterResult := r.buildRawQueryFilter(query)
	esQuery := filterResult.Query

	// Set limit, default 100.
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	// 3. Use composite aggregation by label set + top_hits within each group.
	// Each group returns raw (timestamp, value) pairs sorted by time ASC.
	// ES 7.x compatible: use composite aggregation with script-based
	// deterministic concatenation of label doc values. This avoids the
	// object-field key-ordering issue (multi_terms requires ES 7.12+).
	aggs := map[string]any{
		"by_series": map[string]any{
			"composite": map[string]any{
				"size": 100,
				"sources": []map[string]any{
					{"labels_hash": map[string]any{
						"terms": map[string]any{
							"script": map[string]any{
								"source": `doc['labels.client'].value + '|' + doc['labels.server'].value + '|' + doc['labels.connection_type'].value`,
								"lang":   "painless",
							},
						},
					}},
				},
			},
			"aggs": map[string]any{
				"samples": map[string]any{
					"top_hits": map[string]any{
						"size":    limit,
						"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
						"_source": []string{FieldMetricTimeUnixMilli, FieldMetricValue, FieldMetricLabels, FieldMetricBucketCounts, FieldMetricExplicitBounds},
					},
				},
			},
		},
	}

	searchReq := &SearchRequest{
		Query:        esQuery,
		Size:         0,
		Aggregations: aggs,
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric raw query failed: %w", err)
	}

	series, err := r.parseRawResult(resp)
	if err != nil {
		return nil, err
	}

	// Post-filter series for unsupported regex patterns.
	series = postFilterRawSeries(series, filterResult.PostFilters)

	return series, nil
}

// metricFilterResult holds the ES query and any regex patterns that require post-filtering.
type metricFilterResult struct {
	// Query is the ES bool query to execute.
	Query map[string]any
	// PostFilters contains label regex patterns that ES cannot handle natively
	// (flattened field limitation) and must be applied in the application layer.
	// Key: normalized ES label key, Value: PromQL regex pattern.
	PostFilters map[string]string
}

// buildMetricFilter constructs an ES bool query from metric filter criteria + time range.
// Shared by QueryRaw and QueryFlat to avoid duplicated filter-building logic.
//
// For labelMatch (regex patterns), ES flattened fields do NOT support "regexp" queries.
// Instead, we translate PromQL regex patterns into ES-compatible queries:
//   - "value1|value2|..." → terms query (multi-value exact match)
//   - "literal_with_escaped_dots" → term query (single exact match)
//   - "prefix.*" → prefix query
//   - Complex regex → no ES filter (returned in PostFilters for application-layer filtering)
//
// metricNegations carries the not-equal / not-regex matchers for a query.
// Grouped into a struct so buildMetricFilter keeps a readable signature.
type metricNegations struct {
	Not      map[string]string // != value
	NotMatch map[string]string // !~ pattern
}

func (r *MetricReader) buildMetricFilter(metricName, serviceName string, labels, labelMatch map[string]string, timeRange TimeRange, neg metricNegations) metricFilterResult {
	// Translate PromQL-style labels (underscores, full enum values) to ES storage format
	// (dots, short enum values). Known OTel standard attributes get translated;
	// custom labels pass through unchanged.
	labels, labelMatch = normalizeMetricQueryLabels(labels, labelMatch)

	qb := esq.NewBuilder()

	if metricName != "" {
		qb.Term(FieldName, metricName)
	}
	if serviceName != "" {
		qb.Term(FieldServiceName, serviceName)
	}
	for k, v := range labels {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		qb.Term(field, v)
	}

	// Translate regex patterns to ES-compatible queries for flattened fields.
	// Uses the same .keyword sub-field as exact matches above: metric labels are
	// dynamically mapped text+keyword, and term/terms/prefix on the bare (analyzed)
	// field never matches a multi-token value.
	var postFilters map[string]string
	for k, pattern := range labelMatch {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		translation := TranslatePromQLRegex(pattern)
		clause := BuildESClauseFromRegex(field, translation)
		if clause != nil {
			qb.Raw(clause)
		} else {
			// StrategyUnsupported: collect for post-filtering in application layer.
			if postFilters == nil {
				postFilters = make(map[string]string)
			}
			postFilters[k] = pattern
		}
	}

	// Negated matchers. Exact != becomes must_not term; !~ reuses the same
	// regex translation as =~ and is negated. A pattern the translator cannot
	// express in ES is dropped rather than silently narrowing the result set —
	// post-filtering only supports the positive direction today.
	negLabels, negMatch := normalizeMetricQueryLabels(neg.Not, neg.NotMatch)
	for k, v := range negLabels {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		qb.Raw(esq.MustNotQ(esq.T(field, v)))
	}
	for k, pattern := range negMatch {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		if clause := BuildESClauseFromRegex(field, TranslatePromQLRegex(pattern)); clause != nil {
			qb.Raw(esq.MustNotQ(clause))
		} else {
			r.logger.Warn("metric filter: unsupported !~ pattern, ignoring",
				zap.String("label", k), zap.String("pattern", pattern))
		}
	}

	baseQuery := qb.Build()
	must := []map[string]any{baseQuery}
	timeFilter := r.timeRangeQuery(timeRange)
	if _, isMatchAll := timeFilter["match_all"]; !isMatchAll {
		must = append(must, timeFilter)
	}

	return metricFilterResult{
		Query:       map[string]any{"bool": map[string]any{"must": must}},
		PostFilters: postFilters,
	}
}

// buildRawQueryFilter builds an ES bool query from a MetricRawQuery.
func (r *MetricReader) buildRawQueryFilter(query MetricRawQuery) metricFilterResult {
	return r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
}

// QueryFlat returns all matching metric documents without ES-side grouping.
// Uses a simple ES search (no aggregation) with a configurable MaxDocs cap.
// Grouping by label set happens in Go via the Labels field on each MetricSample.
//
// Designed for histogram_quantile which needs complete bucket_counts data
// across all matching documents in a time range.
func (r *MetricReader) QueryFlat(ctx context.Context, query MetricFlatQuery) (*MetricFlatResult, error) {
	filterResult := r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})

	maxDocs := query.MaxDocs
	if maxDocs <= 0 {
		maxDocs = 10000
	}

	searchReq := &SearchRequest{
		Query: filterResult.Query,
		Size:  maxDocs,
		Sort:  []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
		Source: []string{
			FieldMetricTimeUnixMilli, FieldMetricValue,
			FieldMetricLabels, FieldMetricBucketCounts, FieldMetricExplicitBounds,
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric flat query failed: %w", err)
	}

	samples := make([]MetricSample, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		samples = append(samples, r.hitToSample(hit))
	}

	// Post-filter for unsupported regex patterns that ES flattened fields cannot handle.
	samples = postFilterSamples(samples, filterResult.PostFilters)

	total := int64(len(samples))
	if resp.Hits.Total.Value > 0 && total == int64(len(resp.Hits.Hits)) {
		total = resp.Hits.Total.Value
	}

	return &MetricFlatResult{
		Samples: samples,
		Total:   total,
	}, nil
}

// postFilterSamples applies application-layer regex filtering for patterns
// that cannot be translated to ES queries (StrategyUnsupported on flattened fields).
// postFilters map: key = normalized ES label key, value = PromQL regex pattern.
func postFilterSamples(samples []MetricSample, postFilters map[string]string) []MetricSample {
	if len(postFilters) == 0 {
		return samples
	}

	// Filter samples: keep only those matching ALL regex patterns.
	filtered := samples[:0]
	for _, sample := range samples {
		if matchesPostFilters(sample.Labels, postFilters) {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

// postFilterDataPoints applies application-layer regex filtering on MetricDataPoint slices.
// Used by Query() for instant queries with unsupported regex patterns.
func postFilterDataPoints(data []MetricDataPoint, postFilters map[string]string) []MetricDataPoint {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, dp := range data {
		if matchesPostFilters(dp.Labels, postFilters) {
			filtered = append(filtered, dp)
		}
	}
	return filtered
}

// postFilterSeries applies application-layer regex filtering on MetricSeries slices.
// Used by QueryRange() for grouped aggregation results with unsupported regex patterns.
func postFilterSeries(data []MetricSeries, postFilters map[string]string) []MetricSeries {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, series := range data {
		if matchesPostFilters(series.Labels, postFilters) {
			filtered = append(filtered, series)
		}
	}
	return filtered
}

// postFilterRawSeries applies application-layer regex filtering on MetricRawSeries slices.
// Used by QueryRaw() for raw aggregation results with unsupported regex patterns.
func postFilterRawSeries(data []MetricRawSeries, postFilters map[string]string) []MetricRawSeries {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, series := range data {
		if matchesPostFilters(series.Labels, postFilters) {
			filtered = append(filtered, series)
		}
	}
	return filtered
}

// matchesPostFilters checks if a label set matches ALL given regex post-filters.
func matchesPostFilters(labels map[string]string, postFilters map[string]string) bool {
	for key, pattern := range postFilters {
		val, ok := labels[key]
		if !ok || !PostFilterByRegex(val, pattern) {
			return false
		}
	}
	return true
}

// parseRawResult parses the ES composite+top_hits response into MetricRawSeries.
func (r *MetricReader) parseRawResult(resp *SearchResponse) ([]MetricRawSeries, error) {
	raw, ok := resp.Aggregations["by_series"]
	if !ok {
		return nil, nil
	}

	var composite struct {
		Buckets []struct {
			Key     any `json:"key"` // composite: map[string]any, multi_terms: []any
			Samples struct {
				Hits struct {
					Hits []SearchHit `json:"hits"`
				} `json:"hits"`
			} `json:"samples"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &composite); err != nil {
		return nil, fmt.Errorf("failed to parse QueryRaw result: %w", err)
	}

	result := make([]MetricRawSeries, 0, len(composite.Buckets))
	for _, bucket := range composite.Buckets {
		hits := bucket.Samples.Hits.Hits
		if len(hits) == 0 {
			continue
		}

		samples := make([]MetricSample, 0, len(hits))
		var labels map[string]string
		for _, hit := range hits {
			var doc struct {
				TimeUnixMilli  int64             `json:"timeUnixMilli"`
				Value          float64           `json:"value"`
				Labels         map[string]string `json:"labels"`
				BucketCounts   []int64           `json:"bucket_counts"`
				ExplicitBounds []float64         `json:"explicit_bounds"`
			}
			if err := json.Unmarshal(hit.Source, &doc); err != nil {
				continue
			}
			if labels == nil {
				labels = doc.Labels
			}
			samples = append(samples, MetricSample{
				TimestampMs:  doc.TimeUnixMilli,
				Value:        doc.Value,
				BucketCounts: doc.BucketCounts,
				Bounds:       doc.ExplicitBounds,
				Labels:       doc.Labels,
			})
		}
		if labels == nil {
			labels = make(map[string]string)
		}
		result = append(result, MetricRawSeries{
			Labels:  labels,
			Samples: samples,
		})
	}

	return result, nil
}

// LabelCombinationsQuery is the ES-specific options for label exploration.
type MetricLabelQuery struct {
	MetricName  string
	Labels      map[string]string
	LabelKeys   []string
	ServiceName string
}

// MetricCombinationsResult holds flattened label combinations.
type MetricCombinationsResult struct {
	Combinations []map[string]string
}

// ListLabelCombinations returns unique label value combinations for the
// specified metric. Uses ES terms aggregation on label fields.
func (r *MetricReader) ListLabelCombinations(ctx context.Context, query MetricLabelQuery) (*MetricCombinationsResult, error) {
	esQuery := r.buildMetricQuery(query.MetricName, query.Labels, query.ServiceName)

	searchReq := &SearchRequest{
		Size: 0,
		Query: map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{esQuery},
			},
		},
		Aggregations: map[string]any{"combo_root": r.buildLabelComboAgg(query.LabelKeys)},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("label combinations search failed: %w", err)
	}

	var aggMap map[string]any
	if raw, ok := resp.Aggregations["combo_root"]; ok {
		json.Unmarshal(raw, &aggMap)
	}
	combos := r.flattenLabelCombos(map[string]any{"combo_root": aggMap}, query.LabelKeys)
	return &MetricCombinationsResult{Combinations: combos}, nil
}

func (r *MetricReader) buildLabelComboAgg(keys []string) map[string]any {
	if len(keys) == 0 {
		return nil
	}
	outer := map[string]any{
		"terms": map[string]any{
			"field": "labels." + keys[0],
			"size":  100,
		},
	}
	if len(keys) > 1 {
		outer["aggs"] = map[string]any{
			"next": r.buildLabelComboAgg(keys[1:]),
		}
	}
	return outer
}

func (r *MetricReader) flattenLabelCombos(result map[string]any, keys []string) []map[string]string {
	root, _ := result["combo_root"].(map[string]any)
	if root == nil {
		return nil
	}
	buckets, _ := root["buckets"].([]any)
	if buckets == nil {
		return nil
	}

	var combos []map[string]string
	for _, b := range buckets {
		bucket, _ := b.(map[string]any)
		val := fmt.Sprint(bucket["key"])
		base := map[string]string{keys[0]: val}

		if sub, ok := bucket["next"].(map[string]any); ok && len(keys) > 1 {
			subCombos := r.flattenSubCombos(sub, keys[1:])
			for _, sc := range subCombos {
				for k, v := range base {
					sc[k] = v
				}
				combos = append(combos, sc)
			}
		} else {
			combos = append(combos, base)
		}
	}
	return combos
}

func (r *MetricReader) flattenSubCombos(result map[string]any, keys []string) []map[string]string {
	buckets, _ := result["buckets"].([]any)
	if buckets == nil {
		return nil
	}
	var combos []map[string]string
	for _, b := range buckets {
		bucket, _ := b.(map[string]any)
		val := fmt.Sprint(bucket["key"])
		base := map[string]string{keys[0]: val}

		if sub, ok := bucket["next"].(map[string]any); ok && len(keys) > 1 {
			subCombos := r.flattenSubCombos(sub, keys[1:])
			for _, sc := range subCombos {
				for k, v := range base {
					sc[k] = v
				}
				combos = append(combos, sc)
			}
		} else {
			combos = append(combos, base)
		}
	}
	return combos
}
