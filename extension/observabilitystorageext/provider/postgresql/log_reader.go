// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// LogQuery represents parameters for searching logs.
type LogQuery struct {
	Query       string // Full-text search query
	ServiceName string
	Severity    []string
	TraceID     string
	AppID       string
	TimeRange   TimeRange
	Limit       int
	Offset      int
}

// LogSearchResult holds the result of a log search.
type LogSearchResult struct {
	Logs  []LogRecord
	Total int64
}

// LogRecord represents a single log entry.
type LogRecord struct {
	ID             int64
	Timestamp      time.Time
	ObservedTime   *time.Time
	SeverityNumber int
	SeverityText   string
	Body           string
	ServiceName    string
	AppID          string
	TraceID        string
	SpanID         string
	Attributes     map[string]any
	Resource       map[string]any
}

// LogContext holds surrounding log lines for context viewing.
type LogContext struct {
	Before []LogRecord
	After  []LogRecord
}

// LogField represents an available log field for filtering.
type LogField struct {
	Name  string
	Type  string
	Count int64
}

// LogStatsQuery represents parameters for log statistics.
type LogStatsQuery struct {
	ServiceName string
	TimeRange   TimeRange
	AppID       string
}

// LogStats holds log statistics data.
type LogStats struct {
	TotalCount        int64
	SeverityBreakdown map[string]int64
	ServiceBreakdown  map[string]int64
}

// LogReader queries log data from PostgreSQL.
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
		logger: logger.Named("pg-log-reader"),
	}
}

// SearchLogs searches for logs matching the query parameters.
func (r *LogReader) SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error) {
	var conditions []string
	var args []any
	argIdx := 1

	if !query.TimeRange.Start.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, query.TimeRange.Start)
		argIdx++
	}
	if !query.TimeRange.End.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, query.TimeRange.End)
		argIdx++
	}
	if query.ServiceName != "" {
		conditions = append(conditions, fmt.Sprintf("service_name = $%d", argIdx))
		args = append(args, query.ServiceName)
		argIdx++
	}
	if query.TraceID != "" {
		conditions = append(conditions, fmt.Sprintf("trace_id = $%d", argIdx))
		args = append(args, query.TraceID)
		argIdx++
	}
	if query.AppID != "" {
		conditions = append(conditions, fmt.Sprintf("app_id = $%d", argIdx))
		args = append(args, query.AppID)
		argIdx++
	}
	if len(query.Severity) > 0 {
		conditions = append(conditions, fmt.Sprintf("severity_text = ANY($%d)", argIdx))
		args = append(args, query.Severity)
		argIdx++
	}
	if query.Query != "" {
		// Full-text search using tsvector
		conditions = append(conditions, fmt.Sprintf("body_tsv @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, query.Query)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}

	// Count total
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s %s", r.config.Logs.TableName, whereClause)
	var total int64
	if err := r.client.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count logs failed: %w", err)
	}

	// Fetch logs
	sql := fmt.Sprintf(`
		SELECT id, timestamp, observed_time, severity_number, severity_text,
			   body, service_name, app_id, trace_id, span_id,
			   attributes, resource
		FROM %s %s
		ORDER BY timestamp DESC
		LIMIT %d OFFSET %d
	`, r.config.Logs.TableName, whereClause, limit, query.Offset)

	rows, err := r.client.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search logs failed: %w", err)
	}
	defer rows.Close()

	var logs []LogRecord
	for rows.Next() {
		var lr LogRecord
		var attrsJSON, resourceJSON []byte
		if err := rows.Scan(
			&lr.ID, &lr.Timestamp, &lr.ObservedTime, &lr.SeverityNumber,
			&lr.SeverityText, &lr.Body, &lr.ServiceName, &lr.AppID,
			&lr.TraceID, &lr.SpanID, &attrsJSON, &resourceJSON,
		); err != nil {
			continue
		}
		_ = json.Unmarshal(attrsJSON, &lr.Attributes)
		_ = json.Unmarshal(resourceJSON, &lr.Resource)
		logs = append(logs, lr)
	}

	return &LogSearchResult{Logs: logs, Total: total}, nil
}

// GetLogContext retrieves surrounding log lines for context viewing.
func (r *LogReader) GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error) {
	if lines <= 0 {
		lines = 5
	}

	// Get the target log's timestamp and service for context
	var targetTS time.Time
	var targetService string
	err := r.client.QueryRow(ctx,
		fmt.Sprintf("SELECT timestamp, service_name FROM %s WHERE id = $1", r.config.Logs.TableName),
		logID,
	).Scan(&targetTS, &targetService)
	if err != nil {
		return nil, fmt.Errorf("log not found: %w", err)
	}

	// Get logs before
	beforeSQL := fmt.Sprintf(`
		SELECT id, timestamp, observed_time, severity_number, severity_text,
			   body, service_name, app_id, trace_id, span_id, attributes, resource
		FROM %s
		WHERE service_name = $1 AND timestamp < $2
		ORDER BY timestamp DESC
		LIMIT %d
	`, r.config.Logs.TableName, lines)

	beforeRows, err := r.client.Query(ctx, beforeSQL, targetService, targetTS)
	if err != nil {
		return nil, fmt.Errorf("get log context (before) failed: %w", err)
	}
	defer beforeRows.Close()

	var before []LogRecord
	for beforeRows.Next() {
		lr := r.scanLogRecord(beforeRows)
		if lr != nil {
			before = append(before, *lr)
		}
	}

	// Get logs after
	afterSQL := fmt.Sprintf(`
		SELECT id, timestamp, observed_time, severity_number, severity_text,
			   body, service_name, app_id, trace_id, span_id, attributes, resource
		FROM %s
		WHERE service_name = $1 AND timestamp > $2
		ORDER BY timestamp ASC
		LIMIT %d
	`, r.config.Logs.TableName, lines)

	afterRows, err := r.client.Query(ctx, afterSQL, targetService, targetTS)
	if err != nil {
		return nil, fmt.Errorf("get log context (after) failed: %w", err)
	}
	defer afterRows.Close()

	var after []LogRecord
	for afterRows.Next() {
		lr := r.scanLogRecord(afterRows)
		if lr != nil {
			after = append(after, *lr)
		}
	}

	return &LogContext{Before: before, After: after}, nil
}

// ListLogFields returns available log fields for filtering.
func (r *LogReader) ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error) {
	// Get service names as fields
	sql := fmt.Sprintf(`
		SELECT service_name, COUNT(*) AS cnt
		FROM %s
		WHERE timestamp >= $1 AND timestamp <= $2
		GROUP BY service_name
		ORDER BY cnt DESC
		LIMIT 100
	`, r.config.Logs.TableName)

	rows, err := r.client.Query(ctx, sql, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("list log fields failed: %w", err)
	}
	defer rows.Close()

	var fields []LogField
	for rows.Next() {
		var f LogField
		if err := rows.Scan(&f.Name, &f.Count); err != nil {
			continue
		}
		f.Type = "service"
		fields = append(fields, f)
	}

	return fields, nil
}

// GetLogStats returns log statistics (counts, severity distribution, etc.).
func (r *LogReader) GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error) {
	var conditions []string
	var args []any
	argIdx := 1

	if !query.TimeRange.Start.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, query.TimeRange.Start)
		argIdx++
	}
	if !query.TimeRange.End.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, query.TimeRange.End)
		argIdx++
	}
	if query.ServiceName != "" {
		conditions = append(conditions, fmt.Sprintf("service_name = $%d", argIdx))
		args = append(args, query.ServiceName)
		argIdx++
	}
	if query.AppID != "" {
		conditions = append(conditions, fmt.Sprintf("app_id = $%d", argIdx))
		args = append(args, query.AppID)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Total count
	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s %s", r.config.Logs.TableName, whereClause)
	if err := r.client.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("log stats count failed: %w", err)
	}

	// Severity breakdown
	sevSQL := fmt.Sprintf(`
		SELECT COALESCE(severity_text, 'UNSPECIFIED'), COUNT(*)
		FROM %s %s
		GROUP BY severity_text
	`, r.config.Logs.TableName, whereClause)
	sevRows, err := r.client.Query(ctx, sevSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("log stats severity failed: %w", err)
	}
	defer sevRows.Close()

	severityBreakdown := make(map[string]int64)
	for sevRows.Next() {
		var sev string
		var cnt int64
		if err := sevRows.Scan(&sev, &cnt); err == nil {
			severityBreakdown[sev] = cnt
		}
	}

	// Service breakdown
	svcSQL := fmt.Sprintf(`
		SELECT service_name, COUNT(*)
		FROM %s %s
		GROUP BY service_name
		ORDER BY COUNT(*) DESC
		LIMIT 20
	`, r.config.Logs.TableName, whereClause)
	svcRows, err := r.client.Query(ctx, svcSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("log stats service failed: %w", err)
	}
	defer svcRows.Close()

	serviceBreakdown := make(map[string]int64)
	for svcRows.Next() {
		var svc string
		var cnt int64
		if err := svcRows.Scan(&svc, &cnt); err == nil {
			serviceBreakdown[svc] = cnt
		}
	}

	return &LogStats{
		TotalCount:        total,
		SeverityBreakdown: severityBreakdown,
		ServiceBreakdown:  serviceBreakdown,
	}, nil
}

// scanLogRecord is a helper to scan a log record from a row.
func (r *LogReader) scanLogRecord(rows interface{ Scan(...any) error }) *LogRecord {
	var lr LogRecord
	var attrsJSON, resourceJSON []byte
	if err := rows.Scan(
		&lr.ID, &lr.Timestamp, &lr.ObservedTime, &lr.SeverityNumber,
		&lr.SeverityText, &lr.Body, &lr.ServiceName, &lr.AppID,
		&lr.TraceID, &lr.SpanID, &attrsJSON, &resourceJSON,
	); err != nil {
		return nil
	}
	_ = json.Unmarshal(attrsJSON, &lr.Attributes)
	_ = json.Unmarshal(resourceJSON, &lr.Resource)
	return &lr
}
