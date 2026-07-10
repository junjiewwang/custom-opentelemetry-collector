// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.uber.org/zap"
)

// MetricReader implements metric query operations against Elasticsearch.
// Metrics are stored as per-datapoint documents with fields:
//
//	timeUnixMilli, name, type, serviceName, value, labels, resource
type MetricReader struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// NewMetricReader creates a new MetricReader instance.
func NewMetricReader(client *Client, config *Config, logger *zap.Logger) *MetricReader {
	return &MetricReader{
		client: client,
		config: config,
		logger: logger.Named("metric-reader"),
	}
}

// Query executes an instant metric query, returning the latest value(s) before the given time.
// AppID is optional: when empty, queries all app indices (admin mode).
func (r *MetricReader) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	esQuery := r.buildMetricQuery(query.MetricName, query.Labels, query.ServiceName)

	// For instant query, we look for the latest data point at or before query.Time.
	if !query.Time.IsZero() {
		esQuery = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					esQuery,
				{"range": map[string]any{
					FieldMetricTimeUnixMilli: map[string]any{"lte": query.Time.UnixMilli()},
				}},
				},
			},
		}
	}

	// Use top_hits aggregation grouped by label set to get latest value per series.
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Aggregations: map[string]any{
			"by_labels": map[string]any{
				"terms": map[string]any{
					"field": FieldMetricLabels,
					"size":  1000,
				},
				"aggs": map[string]any{
					"latest": map[string]any{
						"top_hits": map[string]any{
							"size":    1,
							"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}}},
							"_source": []string{FieldMetricTimeUnixMilli, FieldMetricValue, FieldMetricLabels},
						},
					},
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric query failed: %w", err)
	}

	// Fallback: if aggregation doesn't work well with object fields,
	// use a direct query approach.
	if resp.Aggregations == nil || len(resp.Aggregations) == 0 {
		return r.queryDirect(ctx, query.AppID, esQuery)
	}

	result := &MetricResult{}
	if raw, ok := resp.Aggregations["by_labels"]; ok {
		var agg struct {
			Buckets []struct {
				Latest struct {
					Hits struct {
						Hits []SearchHit `json:"hits"`
					} `json:"hits"`
				} `json:"latest"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			for _, bucket := range agg.Buckets {
				for _, hit := range bucket.Latest.Hits.Hits {
					dp := r.hitToDataPoint(hit)
					result.Data = append(result.Data, dp)
				}
			}
		}
	}

	return result, nil
}

// queryDirect performs a direct search as fallback when aggregation on object fields fails.
func (r *MetricReader) queryDirect(ctx context.Context, appID string, query map[string]any) (*MetricResult, error) {
	searchReq := &SearchRequest{
		Query: query,
		Size:  100,
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(appID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric direct query failed: %w", err)
	}

	result := &MetricResult{Data: make([]MetricDataPoint, 0, len(resp.Hits.Hits))}
	for _, hit := range resp.Hits.Hits {
		result.Data = append(result.Data, r.hitToDataPoint(hit))
	}
	return result, nil
}

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
	esQuery := r.buildQueryFilter(query)
	if _, isMatchAll := esQuery["match_all"]; esQuery != nil && isMatchAll {
		esQuery = map[string]any{"match_all": map[string]any{}}
	}

	// 3. Calculate interval for date_histogram.
	interval := r.calculateInterval(query.TimeRange, query.Step)

	// 4. Determine min_doc_count based on fill strategy.
	minDocCount := 0
	if query.Fill == "none" {
		minDocCount = 1
	}

	// 5. Build ES aggregations (with or without groupBy).
	aggs := r.buildAggregation(query.GroupBy, interval, aggFunc, minDocCount, query.SeriesLimit)

	searchReq := &SearchRequest{
		Query:        esQuery,
		Size:         0,
		Aggregations: aggs,
	}

	resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
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

	// 7. Apply fill strategy (post-processing).
	fillFn := GetFillStrategy(query.Fill)
	for i := range result.Data {
		result.Data[i].Values = fillFn(result.Data[i].Values)
	}

	// 8. Normalize labels (ensure non-nil).
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

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
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

// ListLabelNames returns all label names across metrics within the time range.
func (r *MetricReader) ListLabelNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	// ES doesn't natively support listing dynamic field names in objects.
	// We sample recent documents and extract keys from the "labels" field.
	searchReq := &SearchRequest{
		Query:  r.timeRangeQuery(timeRange),
		Size:   100,
		Source: []string{FieldMetricLabels},
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
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

// ListLabelValues returns values for a specific label within the time range.
func (r *MetricReader) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	fieldPath := fmt.Sprintf(FieldMetricLabels+".%s", label)

	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
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

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
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
		qb.Term(fmt.Sprintf(FieldMetricLabels+".%s", k), v)
	}

	return qb.Build()
}

// buildQueryFilter builds the complete ES bool query for a MetricRangeQuery,
// including metric name, service, labels, labelMatch (regex), and time range.
func (r *MetricReader) buildQueryFilter(query MetricRangeQuery) map[string]any {
	qb := esq.NewBuilder()

	if query.MetricName != "" {
		qb.Term(FieldName, query.MetricName)
	}
	if query.ServiceName != "" {
		qb.Term(FieldServiceName, query.ServiceName)
	}
	for k, v := range query.Labels {
		qb.Term(fmt.Sprintf(FieldMetricLabels+".%s", k), v)
	}

	// labelMatch: regex filtering (WHERE tag =~ /regex/)
	for k, pattern := range query.LabelMatch {
		fieldPath := fmt.Sprintf(FieldMetricLabels+".%s", k)
		qb.Raw(map[string]any{
			"regexp": map[string]any{
				fieldPath: map[string]any{
					"value": pattern,
				},
			},
		})
	}

	baseQuery := qb.Build()

	// Add time range filter.
	must := []map[string]any{baseQuery}
	timeFilter := r.timeRangeQuery(query.TimeRange)
	if _, isMatchAll := timeFilter["match_all"]; !isMatchAll {
		must = append(must, timeFilter)
	}

	return map[string]any{"bool": map[string]any{"must": must}}
}

// buildAggregation constructs the ES aggregation for metric range queries.
// Uses simple date_histogram when groupBy is empty, or composite+date_histogram when grouping.
func (r *MetricReader) buildAggregation(groupBy []string, interval string, aggFunc *AggregationFunc, minDocCount int, seriesLimit int) map[string]any {
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
		sources = append(sources, map[string]any{
			label: map[string]any{
				"terms": map[string]any{
					"field":          fmt.Sprintf("%s.%s", FieldMetricLabels, label),
					"missing_bucket": true,
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
		TimeUnixMilli int64             `json:"timeUnixMilli"`
		Value         float64           `json:"value"`
		Labels        map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricDataPoint{}
	}

	return MetricDataPoint{
		Labels: doc.Labels,
		Value:  doc.Value,
		Time:   time.UnixMilli(doc.TimeUnixMilli),
	}
}
