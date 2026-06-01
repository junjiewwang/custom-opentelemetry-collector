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

// LogReader implements log query operations against Elasticsearch.
type LogReader struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// NewLogReader creates a new LogReader instance.
func NewLogReader(client *Client, config *Config, logger *zap.Logger) *LogReader {
	return &LogReader{
		client: client,
		config: config,
		logger: logger.Named("log-reader"),
	}
}

// SearchLogs searches for logs matching the query parameters.
func (r *LogReader) SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error) {
	if query.AppID == "" {
		return nil, errMissingLogAppID
	}

	esQuery := r.buildLogSearchQuery(query)

	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}

	searchReq := &SearchRequest{
		Query: esQuery,
		From:  query.Offset,
		Size:  limit,
		Sort: []map[string]any{
			{"timestamp": map[string]any{"order": "desc"}},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("log search failed: %w", err)
	}

	logs, err := r.hitsToLogRecords(resp.Hits.Hits)
	if err != nil {
		return nil, err
	}

	return &LogSearchResult{
		Logs:  logs,
		Total: resp.Hits.Total.Value,
	}, nil
}

// GetLogContext retrieves surrounding log lines for context viewing.
func (r *LogReader) GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error) {
	if lines <= 0 {
		lines = 10
	}

	// First, fetch the target log by _id to get its timestamp and service_name.
	targetReq := &SearchRequest{
		Query: map[string]any{
			"ids": map[string]any{"values": []string{logID}},
		},
		Size: 1,
	}

	targetResp, err := r.client.Search(ctx, r.indexPattern(), targetReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch target log: %w", err)
	}
	if len(targetResp.Hits.Hits) == 0 {
		return nil, fmt.Errorf("log %s not found", logID)
	}

	targetLogs, err := r.hitsToLogRecords(targetResp.Hits.Hits)
	if err != nil {
		return nil, err
	}
	target := targetLogs[0]

	// Fetch logs before the target (older).
	beforeReq := &SearchRequest{
		Query: map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					{"term": map[string]any{"service_name": target.ServiceName}},
					{"range": map[string]any{
						"timestamp": map[string]any{"lt": formatTimestamp(target.Timestamp)},
					}},
				},
			},
		},
		Size: lines,
		Sort: []map[string]any{
			{"timestamp": map[string]any{"order": "desc"}},
		},
	}

	beforeResp, err := r.client.Search(ctx, r.indexPattern(), beforeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch before context: %w", err)
	}
	beforeLogs, _ := r.hitsToLogRecords(beforeResp.Hits.Hits)
	// Reverse to chronological order.
	for i, j := 0, len(beforeLogs)-1; i < j; i, j = i+1, j-1 {
		beforeLogs[i], beforeLogs[j] = beforeLogs[j], beforeLogs[i]
	}

	// Fetch logs after the target (newer).
	afterReq := &SearchRequest{
		Query: map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					{"term": map[string]any{"service_name": target.ServiceName}},
					{"range": map[string]any{
						"timestamp": map[string]any{"gt": formatTimestamp(target.Timestamp)},
					}},
				},
			},
		},
		Size: lines,
		Sort: []map[string]any{
			{"timestamp": map[string]any{"order": "asc"}},
		},
	}

	afterResp, err := r.client.Search(ctx, r.indexPattern(), afterReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch after context: %w", err)
	}
	afterLogs, _ := r.hitsToLogRecords(afterResp.Hits.Hits)

	return &LogContext{
		Before: beforeLogs,
		Target: target,
		After:  afterLogs,
	}, nil
}

// ListLogFields returns available log fields for filtering.
func (r *LogReader) ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error) {
	// Use field_caps API approach via aggregation on known fields.
	// We query a sample of recent documents and extract field names from attributes.
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"severities": map[string]any{
				"terms": map[string]any{
					"field": "severity",
					"size":  20,
				},
			},
			"services": map[string]any{
				"terms": map[string]any{
					"field": "service_name",
					"size":  500,
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list log fields failed: %w", err)
	}

	fields := []LogField{
		{Name: "timestamp", Type: "date", Count: resp.Hits.Total.Value},
		{Name: "severity", Type: "keyword", Count: resp.Hits.Total.Value},
		{Name: "service_name", Type: "keyword", Count: resp.Hits.Total.Value},
		{Name: "body", Type: "text", Count: resp.Hits.Total.Value},
		{Name: "trace_id", Type: "keyword"},
		{Name: "span_id", Type: "keyword"},
		{Name: "app_id", Type: "keyword"},
	}

	// Add severity values as context.
	if raw, ok := resp.Aggregations["severities"]; ok {
		var agg struct {
			Buckets []struct {
				Key      string `json:"key"`
				DocCount int64  `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			for _, b := range agg.Buckets {
				fields = append(fields, LogField{
					Name:  fmt.Sprintf("severity=%s", b.Key),
					Type:  "value",
					Count: b.DocCount,
				})
			}
		}
	}

	return fields, nil
}

// GetLogStats returns log statistics (counts, severity distribution, etc.).
func (r *LogReader) GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error) {
	if query.AppID == "" {
		return nil, errMissingLogAppID
	}

	var must []map[string]any
	must = append(must, r.timeRangeFilter(query.TimeRange))

	if query.ServiceName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"service_name": query.ServiceName},
		})
	}

	searchReq := &SearchRequest{
		Query: map[string]any{
			"bool": map[string]any{"must": must},
		},
		Size: 0,
		Aggregations: map[string]any{
			"by_severity": map[string]any{
				"terms": map[string]any{
					"field": "severity",
					"size":  20,
				},
			},
			"by_service": map[string]any{
				"terms": map[string]any{
					"field": "service_name",
					"size":  500,
				},
			},
			"time_histogram": map[string]any{
				"date_histogram": map[string]any{
					"field":          "timestamp",
					"fixed_interval": r.calculateInterval(query.TimeRange),
					"min_doc_count":  0,
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("log stats query failed: %w", err)
	}

	stats := &LogStats{
		TotalCount:     resp.Hits.Total.Value,
		SeverityCounts: make(map[string]int64),
		ServiceCounts:  make(map[string]int64),
	}

	// Parse severity aggregation.
	if raw, ok := resp.Aggregations["by_severity"]; ok {
		var agg struct {
			Buckets []struct {
				Key      string `json:"key"`
				DocCount int64  `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			for _, b := range agg.Buckets {
				stats.SeverityCounts[b.Key] = b.DocCount
			}
		}
	}

	// Parse service aggregation.
	if raw, ok := resp.Aggregations["by_service"]; ok {
		var agg struct {
			Buckets []struct {
				Key      string `json:"key"`
				DocCount int64  `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			for _, b := range agg.Buckets {
				stats.ServiceCounts[b.Key] = b.DocCount
			}
		}
	}

	// Parse time histogram.
	if raw, ok := resp.Aggregations["time_histogram"]; ok {
		var agg struct {
			Buckets []struct {
				KeyAsString string `json:"key_as_string"`
				Key         int64  `json:"key"`
				DocCount    int64  `json:"doc_count"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &agg); err == nil {
			stats.TimeHistogram = make([]TimeBucket, 0, len(agg.Buckets))
			for _, b := range agg.Buckets {
				stats.TimeHistogram = append(stats.TimeHistogram, TimeBucket{
					Time:  time.UnixMilli(b.Key),
					Count: b.DocCount,
				})
			}
		}
	}

	return stats, nil
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for logs.
// When appID is provided, returns an app-scoped pattern; otherwise falls back to global wildcard.
func (r *LogReader) indexPattern(appID ...string) string {
	if len(appID) > 0 && appID[0] != "" {
		return r.config.Logs.IndexPrefix + "-" + appID[0] + "-*"
	}
	return r.config.Logs.IndexPrefix + "-*"
}

// errMissingLogAppID is returned when AppID is not provided in a log query.
var errMissingLogAppID = fmt.Errorf("app_id is required for log queries (app-level data isolation)")

// buildLogSearchQuery constructs the ES query from LogQuery parameters.
func (r *LogReader) buildLogSearchQuery(query LogQuery) map[string]any {
	var must []map[string]any

	// Time range filter.
	must = append(must, r.timeRangeFilter(query.TimeRange))

	// Full-text search on body.
	if query.Query != "" {
		must = append(must, map[string]any{
			"match": map[string]any{
				"body": map[string]any{
					"query":    query.Query,
					"operator": "and",
				},
			},
		})
	}

	// Service name filter.
	if query.ServiceName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"service_name": query.ServiceName},
		})
	}

	// Severity filter.
	if len(query.Severity) > 0 {
		must = append(must, map[string]any{
			"terms": map[string]any{"severity": query.Severity},
		})
	}

	// Trace context filters.
	if query.TraceID != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"trace_id": query.TraceID},
		})
	}
	if query.SpanID != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"span_id": query.SpanID},
		})
	}

	// Attribute filters.
	for k, v := range query.Attributes {
		must = append(must, map[string]any{
			"term": map[string]any{fmt.Sprintf("attributes.%s", k): v},
		})
	}

	return map[string]any{
		"bool": map[string]any{"must": must},
	}
}

// timeRangeQuery returns a simple time range query for logs.
func (r *LogReader) timeRangeQuery(tr TimeRange) map[string]any {
	return map[string]any{
		"range": map[string]any{
			"timestamp": map[string]any{
				"gte": formatTimestamp(tr.Start),
				"lte": formatTimestamp(tr.End),
			},
		},
	}
}

// timeRangeFilter returns a time range filter clause for use in bool.must.
func (r *LogReader) timeRangeFilter(tr TimeRange) map[string]any {
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
		"range": map[string]any{"timestamp": filter},
	}
}

// calculateInterval determines the date_histogram interval based on the time range.
func (r *LogReader) calculateInterval(tr TimeRange) string {
	if tr.Start.IsZero() || tr.End.IsZero() {
		return "1h"
	}
	duration := tr.End.Sub(tr.Start)
	switch {
	case duration <= 1*time.Hour:
		return "1m"
	case duration <= 6*time.Hour:
		return "5m"
	case duration <= 24*time.Hour:
		return "15m"
	case duration <= 7*24*time.Hour:
		return "1h"
	case duration <= 30*24*time.Hour:
		return "6h"
	default:
		return "1d"
	}
}

// hitsToLogRecords converts ES search hits into LogRecord objects.
func (r *LogReader) hitsToLogRecords(hits []SearchHit) ([]LogRecord, error) {
	records := make([]LogRecord, 0, len(hits))
	for _, hit := range hits {
		var doc logDocument
		if err := json.Unmarshal(hit.Source, &doc); err != nil {
			r.logger.Warn("Failed to unmarshal log document", zap.String("id", hit.ID), zap.Error(err))
			continue
		}
		record := doc.toLogRecord()
		record.ID = hit.ID
		records = append(records, record)
	}
	return records, nil
}

// ==================== Log Document Model ====================

// logDocument represents the ES document structure for a log record (read-side).
type logDocument struct {
	Timestamp      string         `json:"timestamp"`
	ObservedTime   string         `json:"observed_time,omitempty"`
	Severity       string         `json:"severity"`
	SeverityNumber int32          `json:"severity_number"`
	Body           string         `json:"body,omitempty"`
	ServiceName    string         `json:"service_name"`
	TraceID        string         `json:"trace_id,omitempty"`
	SpanID         string         `json:"span_id,omitempty"`
	AppID          string         `json:"app_id,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	Resource       map[string]any `json:"resource,omitempty"`
}

// toLogRecord converts an ES log document to the local LogRecord type.
func (d *logDocument) toLogRecord() LogRecord {
	ts, _ := time.Parse(esTimestampFormat, d.Timestamp)
	observedTs, _ := time.Parse(esTimestampFormat, d.ObservedTime)

	return LogRecord{
		Timestamp:      ts,
		ObservedTime:   observedTs,
		Severity:       d.Severity,
		SeverityNumber: d.SeverityNumber,
		Body:           d.Body,
		ServiceName:    d.ServiceName,
		TraceID:        d.TraceID,
		SpanID:         d.SpanID,
		AppID:          d.AppID,
		Attributes:     d.Attributes,
		Resource:       d.Resource,
	}
}
