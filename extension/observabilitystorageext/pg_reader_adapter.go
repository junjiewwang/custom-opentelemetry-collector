// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/postgresql"
)

// ═══════════════════════════════════════════════════
// PostgreSQL Reader/Admin Adapters
// Converts between PG-internal types and the public interface types.
// ═══════════════════════════════════════════════════

// --- TraceReader Adapter ---

type pgTraceReaderAdapter struct {
	inner *postgresql.TraceReader
}

var _ TraceReader = (*pgTraceReaderAdapter)(nil)

func (a *pgTraceReaderAdapter) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {
	pgQuery := postgresql.TraceQuery{
		ServiceName:   query.ServiceName,
		OperationName: query.OperationName,
		Tags:          query.Tags,
		MinDuration:   query.MinDuration,
		MaxDuration:   query.MaxDuration,
		TimeRange:     postgresql.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:         query.Limit,
		Offset:        query.Offset,
	}
	result, err := a.inner.SearchTraces(ctx, pgQuery)
	if err != nil {
		return nil, err
	}
	return convertPGTraceSearchResult(result), nil
}

func (a *pgTraceReaderAdapter) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	result, err := a.inner.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	return convertPGTrace(result), nil
}

func (a *pgTraceReaderAdapter) GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetServices(ctx, pgTimeRange)
	if err != nil {
		return nil, err
	}
	services := make([]Service, len(result))
	for i, s := range result {
		services[i] = Service{Name: s.Name}
	}
	return services, nil
}

func (a *pgTraceReaderAdapter) GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetOperations(ctx, service, pgTimeRange)
	if err != nil {
		return nil, err
	}
	ops := make([]Operation, len(result))
	for i, op := range result {
		ops[i] = Operation{Name: op.Name, SpanKind: op.SpanKind}
	}
	return ops, nil
}

func (a *pgTraceReaderAdapter) GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetDependencies(ctx, pgTimeRange)
	if err != nil {
		return nil, err
	}
	deps := make([]Dependency, len(result))
	for i, d := range result {
		deps[i] = Dependency{Parent: d.Parent, Child: d.Child, CallCount: d.CallCount}
	}
	return deps, nil
}

// --- MetricReader Adapter ---

type pgMetricReaderAdapter struct {
	inner *postgresql.MetricReader
}

var _ MetricReader = (*pgMetricReaderAdapter)(nil)

func (a *pgMetricReaderAdapter) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	pgQuery := postgresql.MetricQuery{
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		Time:        query.Time,
		ServiceName: query.ServiceName,
	}
	result, err := a.inner.Query(ctx, pgQuery)
	if err != nil {
		return nil, err
	}
	data := make([]MetricDataPoint, len(result.Samples))
	for i, s := range result.Samples {
		data[i] = MetricDataPoint{
			Labels: s.Labels,
			Value:  s.Value,
			Time:   s.Timestamp,
		}
	}
	return &MetricResult{Data: data}, nil
}

func (a *pgMetricReaderAdapter) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	pgQuery := postgresql.MetricRangeQuery{
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		TimeRange:   postgresql.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Step:        query.Step,
		ServiceName: query.ServiceName,
	}
	result, err := a.inner.QueryRange(ctx, pgQuery)
	if err != nil {
		return nil, err
	}
	data := make([]MetricSeries, len(result.Series))
	for i, s := range result.Series {
		values := make([]MetricDataPoint, len(s.DataPoints))
		for j, dp := range s.DataPoints {
			values[j] = MetricDataPoint{Time: dp.Timestamp, Value: dp.Value}
		}
		data[i] = MetricSeries{Labels: s.Labels, Values: values}
	}
	return &MetricRangeResult{Data: data}, nil
}

func (a *pgMetricReaderAdapter) ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListMetricNames(ctx, pgTimeRange)
}

func (a *pgMetricReaderAdapter) ListLabelNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListLabelNames(ctx, pgTimeRange)
}

func (a *pgMetricReaderAdapter) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListLabelValues(ctx, label, pgTimeRange)
}

// --- LogReader Adapter ---

type pgLogReaderAdapter struct {
	inner *postgresql.LogReader
}

var _ LogReader = (*pgLogReaderAdapter)(nil)

func (a *pgLogReaderAdapter) SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error) {
	pgQuery := postgresql.LogQuery{
		Query:       query.Query,
		ServiceName: query.ServiceName,
		Severity:    query.Severity,
		TraceID:     query.TraceID,
		TimeRange:   postgresql.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:       query.Limit,
		Offset:      query.Offset,
	}
	result, err := a.inner.SearchLogs(ctx, pgQuery)
	if err != nil {
		return nil, err
	}
	logs := make([]LogRecord, len(result.Logs))
	for i, l := range result.Logs {
		logs[i] = LogRecord{
			ID:             fmt.Sprintf("%d", l.ID),
			Timestamp:      l.Timestamp,
			Severity:       l.SeverityText,
			SeverityNumber: int32(l.SeverityNumber),
			Body:           l.Body,
			ServiceName:    l.ServiceName,
			TraceID:        l.TraceID,
			SpanID:         l.SpanID,
			Attributes:     l.Attributes,
			Resource:       l.Resource,
		}
	}
	return &LogSearchResult{Logs: logs, Total: result.Total}, nil
}

func (a *pgLogReaderAdapter) GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error) {
	result, err := a.inner.GetLogContext(ctx, logID, lines)
	if err != nil {
		return nil, err
	}
	before := make([]LogRecord, len(result.Before))
	for i, l := range result.Before {
		before[i] = convertPGLogRecord(l)
	}
	after := make([]LogRecord, len(result.After))
	for i, l := range result.After {
		after[i] = convertPGLogRecord(l)
	}
	return &LogContext{Before: before, After: after}, nil
}

func (a *pgLogReaderAdapter) ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error) {
	pgTimeRange := postgresql.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.ListLogFields(ctx, pgTimeRange)
	if err != nil {
		return nil, err
	}
	fields := make([]LogField, len(result))
	for i, f := range result {
		fields[i] = LogField{Name: f.Name, Type: f.Type, Count: f.Count}
	}
	return fields, nil
}

func (a *pgLogReaderAdapter) GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error) {
	pgQuery := postgresql.LogStatsQuery{
		ServiceName: query.ServiceName,
		TimeRange:   postgresql.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
	}
	result, err := a.inner.GetLogStats(ctx, pgQuery)
	if err != nil {
		return nil, err
	}
	return &LogStats{
		TotalCount:     result.TotalCount,
		SeverityCounts: result.SeverityBreakdown,
		ServiceCounts:  result.ServiceBreakdown,
	}, nil
}

// --- StorageAdmin Adapter ---

type pgStorageAdminAdapter struct {
	inner  *postgresql.Admin
	config *Config
}

var _ StorageAdmin = (*pgStorageAdminAdapter)(nil)

func (a *pgStorageAdminAdapter) GetStatus(ctx context.Context) (*StorageStatus, error) {
	status, err := a.inner.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	return &StorageStatus{
		Provider: "postgresql",
		Healthy:  status["status"] != "red",
		Details:  status,
	}, nil
}

func (a *pgStorageAdminAdapter) InitSchema(ctx context.Context) error {
	return a.inner.InitSchema(ctx)
}

func (a *pgStorageAdminAdapter) GetRetention(ctx context.Context) (map[SignalType]RetentionPolicy, error) {
	return map[SignalType]RetentionPolicy{
		SignalTrace:  {Duration: a.config.PostgreSQL.Traces.Retention},
		SignalMetric: {Duration: a.config.PostgreSQL.Metrics.Retention},
		SignalLog:    {Duration: a.config.PostgreSQL.Logs.Retention},
	}, nil
}

func (a *pgStorageAdminAdapter) SetRetention(ctx context.Context, signal SignalType, policy RetentionPolicy) error {
	tableName, err := a.tableNameForSignal(signal)
	if err != nil {
		return err
	}
	return a.inner.SetRetention(ctx, tableName, policy.Duration)
}

func (a *pgStorageAdminAdapter) Purge(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
	tableName, err := a.tableNameForSignal(signal)
	if err != nil {
		return nil, err
	}
	tsField := a.timestampFieldForSignal(signal)
	deleted, err := a.inner.Purge(ctx, tableName, tsField, before)
	if err != nil {
		return nil, err
	}
	return &PurgeResult{DeletedCount: deleted}, nil
}

func (a *pgStorageAdminAdapter) PurgeByApp(ctx context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error) {
	tableName, err := a.tableNameForSignal(signal)
	if err != nil {
		return nil, err
	}
	tsField := a.timestampFieldForSignal(signal)
	deleted, err := a.inner.PurgeByApp(ctx, tableName, tsField, appID, before)
	if err != nil {
		return nil, err
	}
	return &PurgeResult{DeletedCount: deleted}, nil
}

func (a *pgStorageAdminAdapter) GetDiskUsage(ctx context.Context) (*DiskUsage, error) {
	stats, err := a.inner.GetIndicesStats(ctx)
	if err != nil {
		return nil, err
	}
	totalSize, _ := stats["total_size"].(int64)

	bySignal := make(map[SignalType]int64)
	if traceStats, ok := stats["trace"].(map[string]any); ok {
		if size, ok := traceStats["size"].(int64); ok {
			bySignal[SignalTrace] = size
		}
	}
	if metricStats, ok := stats["metric"].(map[string]any); ok {
		if size, ok := metricStats["size"].(int64); ok {
			bySignal[SignalMetric] = size
		}
	}
	if logStats, ok := stats["log"].(map[string]any); ok {
		if size, ok := logStats["size"].(int64); ok {
			bySignal[SignalLog] = size
		}
	}

	return &DiskUsage{
		TotalBytes: totalSize,
		UsedBytes:  totalSize,
		BySignal:   bySignal,
	}, nil
}

func (a *pgStorageAdminAdapter) tableNameForSignal(signal SignalType) (string, error) {
	switch signal {
	case SignalTrace:
		return a.config.PostgreSQL.Traces.TableName, nil
	case SignalMetric:
		return a.config.PostgreSQL.Metrics.TableName, nil
	case SignalLog:
		return a.config.PostgreSQL.Logs.TableName, nil
	default:
		return "", fmt.Errorf("unknown signal type: %s", signal)
	}
}

func (a *pgStorageAdminAdapter) timestampFieldForSignal(signal SignalType) string {
	switch signal {
	case SignalTrace:
		return "start_time"
	case SignalMetric:
		return "timestamp"
	case SignalLog:
		return "timestamp"
	default:
		return "timestamp"
	}
}

// --- Conversion helpers ---

func convertPGTraceSearchResult(result *postgresql.TraceSearchResult) *TraceSearchResult {
	traces := make([]Trace, len(result.Traces))
	for i, t := range result.Traces {
		traces[i] = Trace{
			TraceID:  t.TraceID,
			Duration: int64(t.Duration * 1000), // ms → µs
			Spans: []Span{
				{
					TraceID:       t.TraceID,
					ServiceName:   t.RootService,
					OperationName: t.RootOperation,
					StartTime:     t.StartTime,
					StatusCode:    t.StatusCode,
					DurationUS:    int64(t.Duration * 1000),
				},
			},
		}
	}
	return &TraceSearchResult{Traces: traces, Total: result.Total}
}

func convertPGTrace(result *postgresql.Trace) *Trace {
	spans := make([]Span, len(result.Spans))
	for i, s := range result.Spans {
		spans[i] = Span{
			TraceID:       s.TraceID,
			SpanID:        s.SpanID,
			ParentSpanID:  s.ParentSpanID,
			OperationName: s.OperationName,
			ServiceName:   s.ServiceName,
			SpanKind:      s.SpanKind,
			StatusCode:    s.StatusCode,
			StatusMessage: s.StatusMessage,
			StartTime:     s.StartTime,
			EndTime:       s.EndTime,
			DurationUS:    int64(s.DurationMs * 1000), // ms → µs
			Attributes:    s.Attributes,
			Resource:      s.Resource,
		}
	}
	return &Trace{TraceID: result.TraceID, Spans: spans}
}

func convertPGLogRecord(l postgresql.LogRecord) LogRecord {
	return LogRecord{
		ID:             fmt.Sprintf("%d", l.ID),
		Timestamp:      l.Timestamp,
		Severity:       l.SeverityText,
		SeverityNumber: int32(l.SeverityNumber),
		Body:           l.Body,
		ServiceName:    l.ServiceName,
		TraceID:        l.TraceID,
		SpanID:         l.SpanID,
		Attributes:     l.Attributes,
		Resource:       l.Resource,
	}
}
