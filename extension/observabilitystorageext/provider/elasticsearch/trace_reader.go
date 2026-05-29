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

// TraceReader implements trace query operations against Elasticsearch.
// It uses a two-pass search strategy:
//   - Step 1: terms aggregation on trace_id to discover matching traces with pagination.
//   - Step 2: bulk fetch all spans for those trace IDs.
type TraceReader struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// NewTraceReader creates a new TraceReader instance.
func NewTraceReader(client *Client, config *Config, logger *zap.Logger) *TraceReader {
	return &TraceReader{
		client: client,
		config: config,
		logger: logger.Named("trace-reader"),
	}
}

// SearchTraces searches for traces matching the query parameters.
func (r *TraceReader) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {
	esQuery := r.buildTraceSearchQuery(query)

	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}

	// Step 1: Find distinct trace_ids matching the filters via aggregation.
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0, // We only want aggregation results.
		Aggregations: map[string]any{
			"traces": map[string]any{
				"terms": map[string]any{
					"field": "trace_id",
					"size":  limit + query.Offset,
					"order": map[string]any{"max_start": "desc"},
				},
				"aggs": map[string]any{
					"max_start": map[string]any{
						"max": map[string]any{"field": "start_time"},
					},
				},
			},
			"total_traces": map[string]any{
				"cardinality": map[string]any{"field": "trace_id"},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("trace search failed: %w", err)
	}

	// Parse aggregation results.
	traceIDs, totalTraces, err := r.parseTraceAggregation(resp, query.Offset, limit)
	if err != nil {
		return nil, err
	}

	if len(traceIDs) == 0 {
		return &TraceSearchResult{Traces: []Trace{}, Total: totalTraces}, nil
	}

	// Step 2: Fetch all spans for the matching trace_ids.
	traces, err := r.fetchTracesByIDs(ctx, traceIDs)
	if err != nil {
		return nil, err
	}

	return &TraceSearchResult{
		Traces: traces,
		Total:  totalTraces,
	}, nil
}

// GetTrace retrieves a single trace by its trace ID.
func (r *TraceReader) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	searchReq := &SearchRequest{
		Query: map[string]any{
			"term": map[string]any{"trace_id": traceID},
		},
		Size: 1000, // Max spans per trace.
		Sort: []map[string]any{
			{"start_time": map[string]any{"order": "asc"}},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get trace failed: %w", err)
	}

	if len(resp.Hits.Hits) == 0 {
		return nil, fmt.Errorf("trace %s not found", traceID)
	}

	spans, err := r.hitsToSpans(resp.Hits.Hits)
	if err != nil {
		return nil, err
	}

	trace := r.assembleTrace(traceID, spans)
	return &trace, nil
}

// GetServices returns all service names within the time range.
func (r *TraceReader) GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"services": map[string]any{
				"terms": map[string]any{
					"field": "service_name",
					"size":  1000,
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get services failed: %w", err)
	}

	return r.parseServicesAggregation(resp, "services")
}

// GetOperations returns operations for a given service.
func (r *TraceReader) GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error) {
	query := map[string]any{
		"bool": map[string]any{
			"must": []map[string]any{
				{"term": map[string]any{"service_name": service}},
				r.timeRangeFilter(timeRange),
			},
		},
	}

	searchReq := &SearchRequest{
		Query: query,
		Size:  0,
		Aggregations: map[string]any{
			"operations": map[string]any{
				"terms": map[string]any{
					"field": "operation_name",
					"size":  1000,
				},
				"aggs": map[string]any{
					"span_kinds": map[string]any{
						"terms": map[string]any{
							"field": "span_kind",
							"size":  10,
						},
					},
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get operations failed: %w", err)
	}

	return r.parseOperationsAggregation(resp)
}

// GetDependencies returns service-to-service dependencies for the service map.
// It uses service co-occurrence within traces as an approximation since ES
// doesn't support joins natively.
func (r *TraceReader) GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	return r.calculateDependencies(ctx, timeRange)
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for traces.
func (r *TraceReader) indexPattern() string {
	return r.config.Traces.IndexPrefix + "-*"
}

// buildTraceSearchQuery constructs the ES query from TraceQuery parameters.
func (r *TraceReader) buildTraceSearchQuery(query TraceQuery) map[string]any {
	var must []map[string]any

	// Time range filter.
	must = append(must, r.timeRangeFilter(query.TimeRange))

	// Service name filter.
	if query.ServiceName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"service_name": query.ServiceName},
		})
	}

	// Operation name filter.
	if query.OperationName != "" {
		must = append(must, map[string]any{
			"term": map[string]any{"operation_name": query.OperationName},
		})
	}

	// Duration filter.
	if query.MinDuration > 0 {
		must = append(must, map[string]any{
			"range": map[string]any{
				"duration_us": map[string]any{"gte": query.MinDuration.Microseconds()},
			},
		})
	}
	if query.MaxDuration > 0 {
		must = append(must, map[string]any{
			"range": map[string]any{
				"duration_us": map[string]any{"lte": query.MaxDuration.Microseconds()},
			},
		})
	}

	// Tag filters (match in attributes or resource).
	for k, v := range query.Tags {
		must = append(must, map[string]any{
			"bool": map[string]any{
				"should": []map[string]any{
					{"term": map[string]any{fmt.Sprintf("attributes.%s", k): v}},
					{"term": map[string]any{fmt.Sprintf("resource.%s", k): v}},
				},
				"minimum_should_match": 1,
			},
		})
	}

	return map[string]any{
		"bool": map[string]any{"must": must},
	}
}

// timeRangeQuery returns a simple time range query.
func (r *TraceReader) timeRangeQuery(tr TimeRange) map[string]any {
	return map[string]any{
		"range": map[string]any{
			"start_time": map[string]any{
				"gte": formatTimestamp(tr.Start),
				"lte": formatTimestamp(tr.End),
			},
		},
	}
}

// timeRangeFilter returns a time range filter clause for use in bool.must.
func (r *TraceReader) timeRangeFilter(tr TimeRange) map[string]any {
	filter := map[string]any{}
	if !tr.Start.IsZero() {
		filter["gte"] = formatTimestamp(tr.Start)
	}
	if !tr.End.IsZero() {
		filter["lte"] = formatTimestamp(tr.End)
	}
	if len(filter) == 0 {
		// No time range constraint — match all.
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"range": map[string]any{"start_time": filter},
	}
}

// parseTraceAggregation extracts trace IDs and total count from the aggregation response.
func (r *TraceReader) parseTraceAggregation(resp *SearchResponse, offset, limit int) ([]string, int64, error) {
	raw, ok := resp.Aggregations["traces"]
	if !ok {
		return nil, 0, nil
	}

	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, 0, fmt.Errorf("failed to parse trace aggregation: %w", err)
	}

	// Parse total distinct traces.
	var totalTraces int64
	if rawTotal, ok := resp.Aggregations["total_traces"]; ok {
		var cardAgg struct {
			Value int64 `json:"value"`
		}
		if err := json.Unmarshal(rawTotal, &cardAgg); err == nil {
			totalTraces = cardAgg.Value
		}
	}

	// Apply offset/limit to the trace_id list.
	traceIDs := make([]string, 0)
	for i, bucket := range agg.Buckets {
		if i < offset {
			continue
		}
		if len(traceIDs) >= limit {
			break
		}
		traceIDs = append(traceIDs, bucket.Key)
	}

	return traceIDs, totalTraces, nil
}

// fetchTracesByIDs fetches all spans for the given trace IDs and assembles them into Trace objects.
func (r *TraceReader) fetchTracesByIDs(ctx context.Context, traceIDs []string) ([]Trace, error) {
	searchReq := &SearchRequest{
		Query: map[string]any{
			"terms": map[string]any{"trace_id": traceIDs},
		},
		Size: len(traceIDs) * 100, // Assume average 100 spans per trace.
		Sort: []map[string]any{
			{"trace_id": map[string]any{"order": "asc"}},
			{"start_time": map[string]any{"order": "asc"}},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("fetch traces by IDs failed: %w", err)
	}

	spans, err := r.hitsToSpans(resp.Hits.Hits)
	if err != nil {
		return nil, err
	}

	// Group spans by trace_id.
	traceMap := make(map[string][]Span)
	for _, span := range spans {
		traceMap[span.TraceID] = append(traceMap[span.TraceID], span)
	}

	// Assemble traces in the order requested.
	traces := make([]Trace, 0, len(traceIDs))
	for _, tid := range traceIDs {
		if spanList, ok := traceMap[tid]; ok && len(spanList) > 0 {
			traces = append(traces, r.assembleTrace(tid, spanList))
		}
	}
	return traces, nil
}

// hitsToSpans converts ES search hits into Span objects.
func (r *TraceReader) hitsToSpans(hits []SearchHit) ([]Span, error) {
	spans := make([]Span, 0, len(hits))
	for _, hit := range hits {
		var doc spanDocument
		if err := json.Unmarshal(hit.Source, &doc); err != nil {
			r.logger.Warn("Failed to unmarshal span document", zap.String("id", hit.ID), zap.Error(err))
			continue
		}
		spans = append(spans, doc.toSpan())
	}
	return spans, nil
}

// assembleTrace creates a Trace from a list of spans.
func (r *TraceReader) assembleTrace(traceID string, spans []Span) Trace {
	var minStart, maxEnd time.Time
	for _, s := range spans {
		if minStart.IsZero() || s.StartTime.Before(minStart) {
			minStart = s.StartTime
		}
		if s.EndTime.After(maxEnd) {
			maxEnd = s.EndTime
		}
	}

	duration := int64(0)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		duration = maxEnd.Sub(minStart).Microseconds()
	}

	return Trace{
		TraceID:  traceID,
		Spans:    spans,
		Duration: duration,
	}
}

// parseServicesAggregation parses a terms aggregation to extract Service names.
func (r *TraceReader) parseServicesAggregation(resp *SearchResponse, aggName string) ([]Service, error) {
	raw, ok := resp.Aggregations[aggName]
	if !ok {
		return nil, nil
	}

	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse %s aggregation: %w", aggName, err)
	}

	services := make([]Service, 0, len(agg.Buckets))
	for _, bucket := range agg.Buckets {
		services = append(services, Service{Name: bucket.Key})
	}
	return services, nil
}

// parseOperationsAggregation parses operations + span_kind aggregation.
func (r *TraceReader) parseOperationsAggregation(resp *SearchResponse) ([]Operation, error) {
	raw, ok := resp.Aggregations["operations"]
	if !ok {
		return nil, nil
	}

	var agg struct {
		Buckets []struct {
			Key       string `json:"key"`
			SpanKinds struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"span_kinds"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse operations aggregation: %w", err)
	}

	operations := make([]Operation, 0)
	for _, bucket := range agg.Buckets {
		if len(bucket.SpanKinds.Buckets) > 0 {
			for _, kindBucket := range bucket.SpanKinds.Buckets {
				operations = append(operations, Operation{
					Name:     bucket.Key,
					SpanKind: kindBucket.Key,
				})
			}
		} else {
			operations = append(operations, Operation{Name: bucket.Key})
		}
	}
	return operations, nil
}

// calculateDependencies uses service co-occurrence within traces to compute dependencies.
func (r *TraceReader) calculateDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"by_trace": map[string]any{
				"terms": map[string]any{
					"field": "trace_id",
					"size":  5000,
				},
				"aggs": map[string]any{
					"services": map[string]any{
						"terms": map[string]any{
							"field": "service_name",
							"size":  100,
						},
					},
				},
			},
		},
	}

	resp, err := r.client.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("dependency search failed: %w", err)
	}

	raw, ok := resp.Aggregations["by_trace"]
	if !ok {
		return nil, nil
	}

	var traceAgg struct {
		Buckets []struct {
			Key      string `json:"key"`
			Services struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"services"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &traceAgg); err != nil {
		return nil, fmt.Errorf("failed to parse dependency aggregation: %w", err)
	}

	// Build dependency counts from service co-occurrence within traces.
	depCounts := make(map[string]int64) // "parent->child" → count
	for _, traceBucket := range traceAgg.Buckets {
		services := make([]string, 0, len(traceBucket.Services.Buckets))
		for _, svcBucket := range traceBucket.Services.Buckets {
			services = append(services, svcBucket.Key)
		}
		// For each pair of services in the same trace, record a dependency.
		for i := 0; i < len(services); i++ {
			for j := i + 1; j < len(services); j++ {
				key := services[i] + "->" + services[j]
				depCounts[key]++
			}
		}
	}

	// Convert to Dependency slice.
	deps := make([]Dependency, 0, len(depCounts))
	for key, count := range depCounts {
		// Parse "parent->child".
		for i := 0; i < len(key)-1; i++ {
			if key[i] == '-' && key[i+1] == '>' {
				deps = append(deps, Dependency{
					Parent:    key[:i],
					Child:     key[i+2:],
					CallCount: count,
				})
				break
			}
		}
	}
	return deps, nil
}

// ==================== Span Document Model ====================

// spanDocument represents the ES document structure for a span (read-side).
type spanDocument struct {
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	OperationName string         `json:"operation_name"`
	ServiceName   string         `json:"service_name"`
	SpanKind      string         `json:"span_kind"`
	StatusCode    string         `json:"status_code"`
	StatusMessage string         `json:"status_message,omitempty"`
	StartTime     string         `json:"start_time"`
	EndTime       string         `json:"end_time"`
	DurationUS    int64          `json:"duration_us"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Resource      map[string]any `json:"resource,omitempty"`
	Events        []spanEventDoc `json:"events,omitempty"`
	Links         []spanLinkDoc  `json:"links,omitempty"`
}

type spanEventDoc struct {
	Name       string         `json:"name"`
	Timestamp  string         `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type spanLinkDoc struct {
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

// toSpan converts an ES span document to the local Span type.
func (d *spanDocument) toSpan() Span {
	startTime, _ := time.Parse(esTimestampFormat, d.StartTime)
	endTime, _ := time.Parse(esTimestampFormat, d.EndTime)

	span := Span{
		TraceID:       d.TraceID,
		SpanID:        d.SpanID,
		ParentSpanID:  d.ParentSpanID,
		OperationName: d.OperationName,
		ServiceName:   d.ServiceName,
		SpanKind:      d.SpanKind,
		StatusCode:    d.StatusCode,
		StatusMessage: d.StatusMessage,
		StartTime:     startTime,
		EndTime:       endTime,
		DurationUS:    d.DurationUS,
		Attributes:    d.Attributes,
		Resource:      d.Resource,
	}

	// Convert events.
	if len(d.Events) > 0 {
		span.Events = make([]SpanEvent, 0, len(d.Events))
		for _, e := range d.Events {
			ts, _ := time.Parse(esTimestampFormat, e.Timestamp)
			span.Events = append(span.Events, SpanEvent{
				Name:       e.Name,
				Timestamp:  ts,
				Attributes: e.Attributes,
			})
		}
	}

	// Convert links.
	if len(d.Links) > 0 {
		span.Links = make([]SpanLink, 0, len(d.Links))
		for _, l := range d.Links {
			span.Links = append(span.Links, SpanLink{
				TraceID: l.TraceID,
				SpanID:  l.SpanID,
			})
		}
	}

	return span
}
