// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// StoredLogRecord aliases the canonical log storage type.
type StoredLogRecord = storedmodel.StoredLogRecord

// LogReader implements log query operations against Elasticsearch.
type LogReader struct {
	searcher Searcher
	config *Config
	logger *zap.Logger
}

// NewLogReader creates a new LogReader instance.
func NewLogReader(searcher Searcher, config *Config, logger *zap.Logger) *LogReader {
	return &LogReader{
		searcher: searcher,
		config: config,
		logger: logger.Named("log-reader"),
	}
}

// SearchLogs searches for logs matching the query parameters.
// AppID is optional: when empty, queries all app indices (admin mode).
func (r *LogReader) SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error) {
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
			{FieldLogTimeUnixNano: map[string]any{"order": "desc"}},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
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

// SearchLogMetric executes a log metric aggregation query.
// Builds a nested terms + histogram aggregation, grouped by the specified labels.
func (r *LogReader) SearchLogMetric(ctx context.Context, query LogMetricQuery) (*LogMetricResult, error) {
	// Build the base log query (filters, time range, etc.)
	esQuery := r.buildLogSearchQuery(query.LogQuery)

	// Default values
	topN := query.TopN
	if topN <= 0 {
		topN = 10
	}
	intervalNanos := query.IntervalNanos
	if intervalNanos <= 0 {
		intervalNanos = 300_000_000_000 // 5min default
	}

	// Build nested aggregations: bottom-to-top
	// Innermost: histogram
	innerAgg := map[string]any{
		"over_time": map[string]any{
			"histogram": map[string]any{
				"field":         FieldLogTimeUnixNano,
				"interval":      float64(intervalNanos),
				"min_doc_count": 0,
			},
		},
	}

	// Nested terms aggregations for each group-by label, from innermost to outermost
	outerAgg := innerAgg
	for i := len(query.GroupByLabels) - 1; i >= 0; i-- {
		esField := r.resolveLogLabelESField(query.GroupByLabels[i])
		outerAgg = map[string]any{
			"by_" + query.GroupByLabels[i]: map[string]any{
				"terms": map[string]any{
					"field": esField,
					"size":  topN,
				},
				"aggs": outerAgg,
			},
		}
	}

	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Sort:  nil, // not needed for aggregations
	}
	if len(query.GroupByLabels) > 0 {
		searchReq.Aggregations = outerAgg
	} else {
		searchReq.Aggregations = innerAgg
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("log metric search failed: %w", err)
	}

	// Parse aggregation results into series
	series := r.parseMetricAggResult(resp.Aggregations, query.GroupByLabels)
	return &LogMetricResult{Series: series}, nil
}

// parseMetricAggResult recursively parses nested ES aggregation results into flat series.
func (r *LogReader) parseMetricAggResult(raw map[string]json.RawMessage, groupBy []string) []LogMetricSeries {
	return r.parseNestedAgg(raw, groupBy, nil)
}

func (r *LogReader) parseNestedAgg(raw map[string]json.RawMessage, remainingLabels []string, labelAcc map[string]string) []LogMetricSeries {
	if labelAcc == nil {
		labelAcc = make(map[string]string)
	}

	if len(remainingLabels) == 0 {
		// Reached the histogram layer
		return r.parseHistogramLayer(raw, labelAcc)
	}

	labelName := remainingLabels[0]
	aggKey := "by_" + labelName
	aggRaw, ok := raw[aggKey]
	if !ok {
		return nil
	}

	var termsAgg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(aggRaw, &termsAgg); err != nil {
		return nil
	}

	// Parse nested aggregations within each term bucket.
	// We need to decode the raw JSON to get the child aggs.
	var fullBucket struct {
		Buckets []json.RawMessage `json:"buckets"`
	}
	if err := json.Unmarshal(aggRaw, &fullBucket); err != nil {
		return nil
	}

	var allSeries []LogMetricSeries
	for i, b := range termsAgg.Buckets {
		childAcc := copyMap(labelAcc)
		childAcc[labelName] = b.Key

		// Parse the child aggregations from the raw bucket
		var bucketWithAggs struct {
			Aggregations map[string]json.RawMessage `json:"over_time,omitempty"`
			// Nested key format
			Children map[string]json.RawMessage
		}
		if err := json.Unmarshal(fullBucket.Buckets[i], &bucketWithAggs); err != nil {
			continue
		}

		childAggs := bucketWithAggs.Aggregations
		if childAggs == nil {
			childAggs = bucketWithAggs.Children
		}
		if childAggs == nil {
			// The child aggs might be at the top level (flattened)
			json.Unmarshal(fullBucket.Buckets[i], &childAggs)
		}

		subSeries := r.parseNestedAgg(childAggs, remainingLabels[1:], childAcc)
		allSeries = append(allSeries, subSeries...)
	}

	return allSeries
}

func (r *LogReader) parseHistogramLayer(raw map[string]json.RawMessage, labels map[string]string) []LogMetricSeries {
	// When called from the top-level (no group-by), raw contains {"over_time": {...}}.
	// When called from an inner terms bucket, the "over_time" key has already been
	// consumed by bucketWithAggs, and raw directly contains {"buckets": [...]}.
	histRaw, ok := raw["over_time"]
	if !ok {
		if _, hasBuckets := raw["buckets"]; hasBuckets {
			reEncoded, _ := json.Marshal(raw)
			histRaw = json.RawMessage(reEncoded)
		} else {
			return nil
		}
	}

	var histAgg struct {
		Buckets []struct {
			Key      int64 `json:"key"`
			DocCount int64 `json:"doc_count"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(histRaw, &histAgg); err != nil {
		return nil
	}

	values := make([]LogMetricValue, 0, len(histAgg.Buckets))
	for _, b := range histAgg.Buckets {
		if b.DocCount == 0 && len(values) == 0 {
			continue // skip leading empty buckets
		}
		values = append(values, LogMetricValue{
			TimestampNano: b.Key,
			Value:         float64(b.DocCount),
		})
	}

	if len(values) == 0 {
		return nil
	}

	seriesLabels := copyMap(labels)
	return []LogMetricSeries{{Labels: seriesLabels, Values: values}}
}

func copyMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
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

	targetResp, err := r.searcher.Search(ctx, r.indexPattern(), targetReq)
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
					{"term": map[string]any{FieldServiceName: target.ServiceName}},
					{"range": map[string]any{
						FieldLogTimeUnixNano: map[string]any{"lt": target.Timestamp.UnixNano()},
					}},
				},
			},
		},
		Size: lines,
		Sort: []map[string]any{
			{FieldLogTimeUnixNano: map[string]any{"order": "desc"}},
		},
	}

	beforeResp, err := r.searcher.Search(ctx, r.indexPattern(), beforeReq)
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
					{"term": map[string]any{FieldServiceName: target.ServiceName}},
					{"range": map[string]any{
						FieldLogTimeUnixNano: map[string]any{"gt": target.Timestamp.UnixNano()},
					}},
				},
			},
		},
		Size: lines,
		Sort: []map[string]any{
			{FieldLogTimeUnixNano: map[string]any{"order": "asc"}},
		},
	}

	afterResp, err := r.searcher.Search(ctx, r.indexPattern(), afterReq)
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
					"field": FieldLogSeverityText,
					"size":  20,
				},
			},
			"services": map[string]any{
				"terms": map[string]any{
					"field": FieldServiceName,
					"size":  500,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list log fields failed: %w", err)
	}

	fields := []LogField{
		{Name: FieldLogTimeUnixNano, Type: "date", Count: resp.Hits.Total.Value},
		{Name: FieldLogSeverityText, Type: "keyword", Count: resp.Hits.Total.Value},
		{Name: FieldServiceName, Type: "keyword", Count: resp.Hits.Total.Value},
		{Name: FieldLogBody, Type: "text", Count: resp.Hits.Total.Value},
		{Name: FieldTraceID, Type: "keyword"},
		{Name: FieldSpanID, Type: "keyword"},
		{Name: FieldAppID, Type: "keyword"},
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
// AppID is optional: when empty, queries all app indices (admin mode).
func (r *LogReader) GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error) {
	var must []map[string]any
	must = append(must, r.timeRangeFilter(query.TimeRange))

	if query.ServiceName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{FieldServiceName: query.ServiceName},
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
					"field": FieldServiceName,
					"size":  500,
				},
			},
		"time_histogram": map[string]any{
			"histogram": map[string]any{
				"field":         FieldLogTimeUnixNano,
				"interval":      float64(r.calculateNanoInterval(query.TimeRange)),
				"min_doc_count": 0,
			},
		},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
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
	id := ""
	if len(appID) > 0 {
		id = appID[0]
	}
	return esq.IndexPattern(r.config.Logs.IndexPrefix, id)
}

// buildLogSearchQuery constructs the ES query from LogQuery parameters.
func (r *LogReader) buildLogSearchQuery(lq LogQuery) map[string]any {
	qb := esq.NewBuilder().
		Raw(esq.TimeRangeFilter(FieldLogTimeUnixNano, lq.TimeRange))

	if lq.Query != "" {
		qb.Match(FieldLogBody, lq.Query, map[string]any{"operator": "and"})
	}
	if lq.ServiceName != "" {
		qb.Term(FieldServiceName, lq.ServiceName)
	}
	if len(lq.Severity) > 0 {
		qb.Terms(FieldLogSeverityText, lq.Severity)
	}
	if lq.TraceID != "" {
		qb.Term(FieldTraceID, lq.TraceID)
	}
	if lq.SpanID != "" {
		qb.Term(FieldSpanID, lq.SpanID)
	}
	for k, v := range lq.Attributes {
		clauses := resolveTagTermClauses(k, v)
		if len(clauses) == 1 {
			qb.Raw(clauses[0])
		} else {
			qb.Should(1, clauses...)
		}
	}

	// ── Loki-specific stream selector labels ──
	for k, v := range lq.Labels {
		f := r.resolveLogLabelESField(k)
		qb.Term(f, v)
	}
	for k, v := range lq.LabelMatch {
		f := r.resolveLogLabelESField(k)
		_ = f
		clauses := resolveTagTermClauses(k, v)
		if len(clauses) == 1 {
			qb.Raw(clauses[0])
		} else {
			qb.Should(1, clauses...)
		}
	}

	return qb.Build()
}

// timeRangeQuery returns a simple time range query for logs.
func (r *LogReader) timeRangeQuery(tr TimeRange) map[string]any {
	return esq.TimeRangeQuery(FieldLogTimeUnixNano, tr)
}

// timeRangeFilter returns a time range filter clause for use in bool.must.
func (r *LogReader) timeRangeFilter(tr TimeRange) map[string]any {
	return esq.TimeRangeFilter(FieldLogTimeUnixNano, tr)
}

// calculateInterval determines the date_histogram interval based on the time range.
// NOTE: This returns a duration string (e.g. "15s") designed for ES date type fields.
// For long type fields (like timeUnixNano), use calculateNanoInterval instead.
func (r *LogReader) calculateInterval(tr TimeRange) string {
	duration := time.Duration(0)
	if !tr.Start.IsZero() && !tr.End.IsZero() {
		duration = tr.End.Sub(tr.Start)
	}

	interval, clamped := esq.SafeInterval(esq.BucketParams{
		Duration:   duration,
		Step:       0, // auto-calculate
		MaxBuckets: esq.DefaultMaxBuckets,
	})

	if clamped {
		r.logger.Warn("log stats interval clamped to avoid too_many_buckets",
			zap.String("clamped_interval", interval),
			zap.Duration("duration", duration),
			zap.Int("max_buckets", esq.DefaultMaxBuckets),
		)
	}

	return interval
}

// calculateNanoInterval computes the histogram interval in nanoseconds.
// Used for histogram aggregation on long type fields (e.g. timeUnixNano).
// Delegates to SafeInterval for bucket-count safety, then converts to nanoseconds.
func (r *LogReader) calculateNanoInterval(tr TimeRange) int64 {
	intervalStr := r.calculateInterval(tr)
	d, err := time.ParseDuration(intervalStr)
	if err != nil {
		return 300_000_000_000 // fallback: 5 minutes in nanoseconds
	}
	return int64(d)
}

// hitsToLogRecords converts ES search hits into LogRecord objects.
func (r *LogReader) hitsToLogRecords(hits []SearchHit) ([]LogRecord, error) {
	records := make([]LogRecord, 0, len(hits))
	for _, hit := range hits {
		var rec StoredLogRecord
		if err := json.Unmarshal(hit.Source, &rec); err != nil {
			r.logger.Warn("Failed to unmarshal log document", zap.String("id", hit.ID), zap.Error(err))
			continue
		}
		rec = compatLogRecord(rec, hit.Source)
		localRec := LogRecord{
			Timestamp:      time.Unix(0, rec.TimeUnixNano),
			ObservedTime:   time.Unix(0, rec.ObservedTimeUnixNano),
			Severity:       rec.SeverityText,
			SeverityNumber: rec.SeverityNumber,
			Body:           rec.Body,
			ServiceName:    rec.ServiceName,
			TraceID:        rec.TraceID,
			SpanID:         rec.SpanID,
			AppID:          rec.AppID,
			Attributes:     rec.Attributes,
			Resource:       rec.Resource,
			ID:             hit.ID,
		}
		records = append(records, localRec)
	}
	return records, nil
}

// compatLogRecord fills fields from old index format.
func compatLogRecord(rec StoredLogRecord, raw json.RawMessage) StoredLogRecord {
	if rec.SeverityText == "" {
		var legacy struct {
			Severity string `json:"severity"`
		}
		if json.Unmarshal(raw, &legacy) == nil && legacy.Severity != "" {
			rec.SeverityText = legacy.Severity
		}
	}
	return rec
}

// ListLogLabels returns distinct log label names for the given time range.
// Labels are returned in Loki-compatible format (underscore_case, not ES camelCase).
// This is consumed by Grafana's labels picker and the logs-drilldown plugin.
func (r *LogReader) ListLogLabels(ctx context.Context, timeRange TimeRange, appID string) ([]string, error) {
	// Return Loki-compatible label names, not ES field names.
	// Grafana plugins (loki-datasource, logs-drilldown) expect these names
	// when constructing LogQL queries and populating label pickers.
	labels := []string{
		"service_name",   // ES: serviceName
		"severity",       // ES: severityText
		"level",          // ES: severityText (Loki standard)
		"detected_level", // ES: severityText (Loki standard)
		"appId",          // ES: appId (unchanged)
		"traceID",        // ES: traceId
		"spanID",         // ES: spanId
	}
	return labels, nil
}

// ListLogLabelValues returns distinct values for a label within the time range.
func (r *LogReader) ListLogLabelValues(ctx context.Context, label string, timeRange TimeRange, appID string) ([]string, error) {
	field := r.resolveLogLabelESField(label)
	if field == "" {
		return nil, nil
	}
	return r.listFieldValues(ctx, field, timeRange, appID)
}

// resolveLogLabelESField maps a Loki-style label name to the ES field.
// Loki standard labels (level, detected_level) are mapped to severityText
// since OTel data model stores log severity in that single field.
var logLabelFieldMap = map[string]string{
	"service_name":   FieldServiceName,
	"appId":          FieldAppID,
	"severity":       FieldLogSeverityText,
	"level":          FieldLogSeverityText, // Loki standard — log level
	"detected_level": FieldLogSeverityText, // Loki standard — detected log level
	"traceID":        FieldTraceID,
	"spanID":         FieldSpanID,
}

func (r *LogReader) resolveLogLabelESField(label string) string {
	if f, ok := logLabelFieldMap[label]; ok {
		return f
	}
	// Dynamic labels are stored under resource.* or attributes.*
	return FieldResource + "." + label
}

func (r *LogReader) listFieldKeys(ctx context.Context, timeRange TimeRange, appID string, fields ...string) ([]string, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
	}
	resp, err := r.searcher.Search(ctx, r.indexPattern(appID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list log labels failed: %w", err)
	}
	_ = resp // unused for now
	return append([]string(nil), fields...), nil
}

func (r *LogReader) listFieldValues(ctx context.Context, field string, timeRange TimeRange, appID string) ([]string, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"values": map[string]any{
				"terms": map[string]any{
					"field": field,
					"size":  1000,
				},
			},
		},
	}
	resp, err := r.searcher.Search(ctx, r.indexPattern(appID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list log label values failed (field=%s): %w", field, err)
	}
	raw, ok := resp.Aggregations["values"]
	if !ok {
		return nil, nil
	}
	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("parse label values: %w", err)
	}
	result := make([]string, 0, len(agg.Buckets))
	for _, b := range agg.Buckets {
		if b.Key != "" {
			result = append(result, b.Key)
		}
	}
	return result, nil
}

// Update buildLogSearchQuery to also handle Loki-specific labels.
func init() {
	// Note: buildLogSearchQuery is patched below to include Labels/LabelMatch handling.
}

// ==================== Log Document Model ====================

// ==================== Log Document Model ====================
// logDocument and toLogRecord() replaced by storedmodel.StoredLogRecord.
// For backward compat with old index data, see compatLogRecord().
