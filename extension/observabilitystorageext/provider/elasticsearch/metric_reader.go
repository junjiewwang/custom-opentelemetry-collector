// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// MetricReader implements metric query operations against Elasticsearch.
// Metrics are stored as per-datapoint documents with fields:
//
//	@timestamp, metric_name, metric_type, service_name, value, labels, resource
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
						"@timestamp": map[string]any{"lte": formatTimestamp(query.Time)},
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
					"field": "labels",
					"size":  1000,
				},
				"aggs": map[string]any{
					"latest": map[string]any{
						"top_hits": map[string]any{
							"size":    1,
							"sort":    []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}},
							"_source": []string{"@timestamp", "value", "labels"},
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
			{"@timestamp": map[string]any{"order": "desc"}},
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
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	esQuery := r.buildMetricQuery(query.MetricName, query.Labels, query.ServiceName)

	// Add time range filter.
	must := []map[string]any{esQuery}
	if !query.TimeRange.Start.IsZero() || !query.TimeRange.End.IsZero() {
		timeFilter := map[string]any{}
		if !query.TimeRange.Start.IsZero() {
			timeFilter["gte"] = formatTimestamp(query.TimeRange.Start)
		}
		if !query.TimeRange.End.IsZero() {
			timeFilter["lte"] = formatTimestamp(query.TimeRange.End)
		}
		must = append(must, map[string]any{
			"range": map[string]any{"@timestamp": timeFilter},
		})
	}

	// Calculate interval for date_histogram.
	interval := r.calculateInterval(query.TimeRange, query.Step)

	searchReq := &SearchRequest{
		Query: map[string]any{
			"bool": map[string]any{"must": must},
		},
		Size: 0,
		Aggregations: map[string]any{
			"time_series": map[string]any{
				"date_histogram": map[string]any{
					"field":          "@timestamp",
					"fixed_interval": interval,
					"min_doc_count":  0,
				},
				"aggs": map[string]any{
					"avg_value": map[string]any{
						"avg": map[string]any{"field": "value"},
					},
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric range query failed: %w", err)
	}

	result := &MetricRangeResult{}

	if raw, ok := resp.Aggregations["time_series"]; ok {
		var agg struct {
			Buckets []struct {
				Key      int64   `json:"key"`
				AvgValue struct {
					Value *float64 `json:"value"`
				} `json:"avg_value"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			series := MetricSeries{
				Labels: query.Labels,
				Values: make([]MetricDataPoint, 0, len(agg.Buckets)),
			}
			for _, b := range agg.Buckets {
				if b.AvgValue.Value != nil {
					series.Values = append(series.Values, MetricDataPoint{
						Labels: query.Labels,
						Value:  *b.AvgValue.Value,
						Time:   time.UnixMilli(b.Key),
					})
				}
			}
			result.Data = append(result.Data, series)
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
					"field": "metric_name",
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

	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse metric_names aggregation: %w", err)
	}

	names := make([]string, 0, len(agg.Buckets))
	for _, b := range agg.Buckets {
		names = append(names, b.Key)
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
		Source: []string{"labels"},
		Sort: []map[string]any{
			{"@timestamp": map[string]any{"order": "desc"}},
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
	fieldPath := fmt.Sprintf("labels.%s", label)

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

	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse label_values aggregation: %w", err)
	}

	values := make([]string, 0, len(agg.Buckets))
	for _, b := range agg.Buckets {
		values = append(values, b.Key)
	}
	return values, nil
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for metrics.
// When appID is provided, returns an app-scoped pattern; otherwise falls back to global wildcard.
func (r *MetricReader) indexPattern(appID ...string) string {
	if len(appID) > 0 && appID[0] != "" {
		return r.config.Metrics.IndexPrefix + "-" + appID[0] + "-*"
	}
	return r.config.Metrics.IndexPrefix + "-*"
}

// errMissingMetricAppID is returned when AppID is not provided in a metric query.
var errMissingMetricAppID = fmt.Errorf("app_id is required for metric queries (app-level data isolation)")

// buildMetricQuery constructs the ES query for metric search.
func (r *MetricReader) buildMetricQuery(metricName string, labels map[string]string, serviceName string) map[string]any {
	var must []map[string]any

	if metricName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"metric_name": metricName},
		})
	}

	if serviceName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"service_name": serviceName},
		})
	}

	for k, v := range labels {
		must = append(must, map[string]any{
			"term": map[string]any{fmt.Sprintf("labels.%s", k): v},
		})
	}

	if len(must) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"bool": map[string]any{"must": must},
	}
}

// timeRangeQuery returns a simple time range query for metrics.
func (r *MetricReader) timeRangeQuery(tr TimeRange) map[string]any {
	filter := map[string]any{}
	if !tr.Start.IsZero() {
		filter["gte"] = formatTimestamp(tr.Start)
	}
	if !tr.End.IsZero() {
		filter["lte"] = formatTimestamp(tr.End)
	}
	if len(filter) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"range": map[string]any{"@timestamp": filter},
	}
}

// calculateInterval determines the appropriate histogram interval.
func (r *MetricReader) calculateInterval(tr TimeRange, step time.Duration) string {
	if step > 0 {
		return fmt.Sprintf("%ds", int(step.Seconds()))
	}
	if tr.Start.IsZero() || tr.End.IsZero() {
		return "1m"
	}
	duration := tr.End.Sub(tr.Start)
	switch {
	case duration <= 1*time.Hour:
		return "15s"
	case duration <= 6*time.Hour:
		return "1m"
	case duration <= 24*time.Hour:
		return "5m"
	case duration <= 7*24*time.Hour:
		return "30m"
	default:
		return "1h"
	}
}

// hitToDataPoint converts an ES search hit to a MetricDataPoint.
func (r *MetricReader) hitToDataPoint(hit SearchHit) MetricDataPoint {
	var doc struct {
		Timestamp string            `json:"@timestamp"`
		Value     float64           `json:"value"`
		Labels    map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricDataPoint{}
	}

	ts, _ := time.Parse(esTimestampFormat, doc.Timestamp)
	return MetricDataPoint{
		Labels: doc.Labels,
		Value:  doc.Value,
		Time:   ts,
	}
}
