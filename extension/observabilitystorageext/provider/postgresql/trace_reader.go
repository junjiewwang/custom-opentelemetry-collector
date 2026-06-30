// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// TraceQuery aliases the unified storedmodel definition.
type TraceQuery = storedmodel.TraceQuery

// TimeRange aliases the unified storedmodel definition.
type TimeRange = storedmodel.TimeRange

// TraceSearchResult holds the result of a trace search.
type TraceSearchResult struct {
	Traces []TraceSummary
	Total  int64
}

// TraceSummary is a lightweight representation of a trace for search results.
type TraceSummary struct {
	TraceID      string
	RootService  string
	RootOperation string
	StartTime    time.Time
	Duration     float64
	SpanCount    int
	StatusCode   string
	Services     []string
}

// Trace represents a full trace with all its spans.
type Trace struct {
	TraceID string
	Spans   []Span
}

// Span represents a single span within a trace.
type Span struct {
	TraceID        string
	SpanID         string
	ParentSpanID   string
	OperationName  string
	ServiceName    string
	SpanKind       string
	StatusCode     string
	StatusMessage  string
	StartTime      time.Time
	EndTime        time.Time
	DurationMs     float64
	Attributes     map[string]any
	Resource       map[string]any
	Events         []map[string]any
	Links          []map[string]any
}

// Service represents a service name and its span count.
type Service struct {
	Name      string
	SpanCount int64
}

// Operation represents an operation within a service.
type Operation struct {
	Name     string
	SpanKind string
}

// Dependency represents a service-to-service call.
type Dependency struct {
	Parent    string
	Child     string
	CallCount int64
}

// TraceReader queries trace data from PostgreSQL.
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
		logger: logger.Named("pg-trace-reader"),
	}
}

// SearchTraces searches for traces matching the query parameters.
func (r *TraceReader) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {
	// Build WHERE clause dynamically
	var conditions []string
	var args []any
	argIdx := 1

	if !query.TimeRange.Start.IsZero() {
		conditions = append(conditions, fmt.Sprintf("start_time >= $%d", argIdx))
		args = append(args, query.TimeRange.Start)
		argIdx++
	}
	if !query.TimeRange.End.IsZero() {
		conditions = append(conditions, fmt.Sprintf("start_time <= $%d", argIdx))
		args = append(args, query.TimeRange.End)
		argIdx++
	}
	if query.ServiceName != "" {
		conditions = append(conditions, fmt.Sprintf("service_name = $%d", argIdx))
		args = append(args, query.ServiceName)
		argIdx++
	}
	if query.OperationName != "" {
		conditions = append(conditions, fmt.Sprintf("operation_name = $%d", argIdx))
		args = append(args, query.OperationName)
		argIdx++
	}
	if query.MinDuration > 0 {
		conditions = append(conditions, fmt.Sprintf("duration_ms >= $%d", argIdx))
		args = append(args, float64(query.MinDuration)/1e6)
		argIdx++
	}
	if query.MaxDuration > 0 {
		conditions = append(conditions, fmt.Sprintf("duration_ms <= $%d", argIdx))
		args = append(args, float64(query.MaxDuration)/1e6)
		argIdx++
	}
	for k, v := range query.Tags {
		conditions = append(conditions, fmt.Sprintf("attributes @> $%d::jsonb", argIdx))
		tagJSON, _ := json.Marshal(map[string]string{k: v})
		args = append(args, string(tagJSON))
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := query.Offset

	// Count total matching trace IDs
	countSQL := fmt.Sprintf(
		"SELECT COUNT(DISTINCT trace_id) FROM %s %s",
		r.config.Traces.TableName, whereClause,
	)
	var total int64
	if err := r.client.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count traces failed: %w", err)
	}

	// Get distinct trace IDs with summary info
	sql := fmt.Sprintf(`
		SELECT trace_id,
			   MIN(service_name) AS root_service,
			   MIN(operation_name) AS root_operation,
			   MIN(start_time) AS start_time,
			   MAX(duration_ms) AS duration,
			   COUNT(*) AS span_count,
			   MAX(status_code) AS status_code,
			   ARRAY_AGG(DISTINCT service_name) AS services
		FROM %s %s
		GROUP BY trace_id
		ORDER BY MIN(start_time) DESC
		LIMIT %d OFFSET %d
	`, r.config.Traces.TableName, whereClause, limit, offset)

	rows, err := r.client.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search traces failed: %w", err)
	}
	defer rows.Close()

	var traces []TraceSummary
	for rows.Next() {
		var ts TraceSummary
		var services []string
		if err := rows.Scan(
			&ts.TraceID, &ts.RootService, &ts.RootOperation,
			&ts.StartTime, &ts.Duration, &ts.SpanCount, &ts.StatusCode, &services,
		); err != nil {
			return nil, fmt.Errorf("scan trace summary: %w", err)
		}
		ts.Services = services
		traces = append(traces, ts)
	}

	return &TraceSearchResult{Traces: traces, Total: total}, nil
}

// GetTrace retrieves a single trace by its trace ID.
func (r *TraceReader) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	sql := fmt.Sprintf(`
		SELECT trace_id, span_id, parent_span_id, operation_name, service_name,
			   span_kind, status_code, status_message, start_time, end_time,
			   duration_ms, attributes, resource, events, links
		FROM %s
		WHERE trace_id = $1
		ORDER BY start_time ASC
	`, r.config.Traces.TableName)

	rows, err := r.client.Query(ctx, sql, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace failed: %w", err)
	}
	defer rows.Close()

	trace := &Trace{TraceID: traceID}
	for rows.Next() {
		var span Span
		var attrsJSON, resourceJSON, eventsJSON, linksJSON []byte
		if err := rows.Scan(
			&span.TraceID, &span.SpanID, &span.ParentSpanID, &span.OperationName,
			&span.ServiceName, &span.SpanKind, &span.StatusCode, &span.StatusMessage,
			&span.StartTime, &span.EndTime, &span.DurationMs,
			&attrsJSON, &resourceJSON, &eventsJSON, &linksJSON,
		); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		_ = json.Unmarshal(attrsJSON, &span.Attributes)
		_ = json.Unmarshal(resourceJSON, &span.Resource)
		_ = json.Unmarshal(eventsJSON, &span.Events)
		_ = json.Unmarshal(linksJSON, &span.Links)
		trace.Spans = append(trace.Spans, span)
	}

	if len(trace.Spans) == 0 {
		return nil, fmt.Errorf("trace not found: %s", traceID)
	}
	return trace, nil
}

// GetTraceSpans returns all spans for a trace as StoredSpan.
func (r *TraceReader) GetTraceSpans(ctx context.Context, traceID string) ([]storedmodel.StoredSpan, error) {
	sql := fmt.Sprintf(`
		SELECT trace_id, span_id, parent_span_id, operation_name, service_name,
			   span_kind, status_code, status_message, start_time, end_time,
			   duration_ms, attributes, resource, events, links
		FROM %s
		WHERE trace_id = $1
		ORDER BY start_time ASC
	`, r.config.Traces.TableName)

	rows, err := r.client.Query(ctx, sql, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace spans: %w", err)
	}
	defer rows.Close()

	var spans []storedmodel.StoredSpan
	for rows.Next() {
		ss, err := r.scanStoredSpan(rows)
		if err != nil {
			return nil, err
		}
		spans = append(spans, ss)
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("trace not found: %s", traceID)
	}
	return spans, nil
}

// SearchSpans searches spans matching the query and returns StoredSpan format.
func (r *TraceReader) SearchSpans(ctx context.Context, query TraceQuery) ([]storedmodel.StoredSpan, []string, error) {
	// Simplified: delegate to SearchTraces (ID list) then GetTraceSpans for each
	result, err := r.SearchTraces(ctx, query)
	if err != nil || result == nil || len(result.Traces) == 0 {
		return nil, nil, err
	}

	var allSpans []storedmodel.StoredSpan
	var traceIDs []string
	for _, t := range result.Traces {
		traceIDs = append(traceIDs, t.TraceID)
		spans, err := r.GetTraceSpans(ctx, t.TraceID)
		if err != nil {
			r.logger.Warn("Failed to fetch spans for trace", zap.String("trace_id", t.TraceID), zap.Error(err))
			continue
		}
		allSpans = append(allSpans, spans...)
	}
	return allSpans, traceIDs, nil
}

// scanStoredSpan scans a PG row into a StoredSpan.
func (r *TraceReader) scanStoredSpan(rows interface{ Scan(...interface{}) error }) (storedmodel.StoredSpan, error) {
	var ss storedmodel.StoredSpan
	var operationName, spanKind, statusCode, statusMsg string
	var startTime, endTime time.Time
	var durationMs float64
	var attrsJSON, resourceJSON, eventsJSON, linksJSON []byte

	if err := rows.Scan(
		&ss.TraceID, &ss.SpanID, &ss.ParentSpanID, &operationName,
		&ss.ServiceName, &spanKind, &statusCode, &statusMsg,
		&startTime, &endTime, &durationMs,
		&attrsJSON, &resourceJSON, &eventsJSON, &linksJSON,
	); err != nil {
		return ss, fmt.Errorf("scan span: %w", err)
	}

	ss.Name = operationName
	ss.Kind = spanKind
	ss.Status.Code = statusCode
	ss.Status.Message = statusMsg
	ss.StartUnixNano = startTime.UnixNano()
	ss.EndUnixNano = endTime.UnixNano()
	ss.DurationNano = int64(durationMs * 1e6)

	_ = json.Unmarshal(attrsJSON, &ss.Attributes)
	_ = json.Unmarshal(resourceJSON, &ss.Resource)
	_ = json.Unmarshal(eventsJSON, &ss.Events)
	_ = json.Unmarshal(linksJSON, &ss.Links)

	return ss, nil
}

// GetServices returns all service names within the time range.
func (r *TraceReader) GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error) {
	sql := fmt.Sprintf(`
		SELECT service_name, COUNT(*) AS span_count
		FROM %s
		WHERE start_time >= $1 AND start_time <= $2
		GROUP BY service_name
		ORDER BY span_count DESC
	`, r.config.Traces.TableName)

	rows, err := r.client.Query(ctx, sql, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("get services failed: %w", err)
	}
	defer rows.Close()

	var services []Service
	for rows.Next() {
		var s Service
		if err := rows.Scan(&s.Name, &s.SpanCount); err != nil {
			continue
		}
		services = append(services, s)
	}
	return services, nil
}

// GetOperations returns operations for a given service.
func (r *TraceReader) GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error) {
	sql := fmt.Sprintf(`
		SELECT DISTINCT operation_name, span_kind
		FROM %s
		WHERE service_name = $1 AND start_time >= $2 AND start_time <= $3
		ORDER BY operation_name
	`, r.config.Traces.TableName)

	rows, err := r.client.Query(ctx, sql, service, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("get operations failed: %w", err)
	}
	defer rows.Close()

	var operations []Operation
	for rows.Next() {
		var op Operation
		if err := rows.Scan(&op.Name, &op.SpanKind); err != nil {
			continue
		}
		operations = append(operations, op)
	}
	return operations, nil
}

// GetDependencies returns service-to-service dependencies.
func (r *TraceReader) GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	sql := fmt.Sprintf(`
		SELECT parent.service_name AS parent_svc,
			   child.service_name AS child_svc,
			   COUNT(*) AS call_count
		FROM %s child
		JOIN %s parent ON child.parent_span_id = parent.span_id
			AND child.trace_id = parent.trace_id
		WHERE child.start_time >= $1 AND child.start_time <= $2
			AND parent.service_name != child.service_name
		GROUP BY parent.service_name, child.service_name
		ORDER BY call_count DESC
	`, r.config.Traces.TableName, r.config.Traces.TableName)

	rows, err := r.client.Query(ctx, sql, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("get dependencies failed: %w", err)
	}
	defer rows.Close()

	var deps []Dependency
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.Parent, &d.Child, &d.CallCount); err != nil {
			continue
		}
		deps = append(deps, d)
	}
	return deps, nil
}
