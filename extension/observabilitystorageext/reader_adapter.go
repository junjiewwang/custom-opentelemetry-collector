// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
)

// ═══════════════════════════════════════════════════
// Reader Adapter — converts between ES-internal types
// and the public interface types defined in types.go.
// This avoids circular imports while keeping a clean
// public API for consumers (e.g., adminext handlers).
// ═══════════════════════════════════════════════════

// traceReaderAdapter adapts the ES TraceReader to the public TraceReader interface.
type traceReaderAdapter struct {
	inner *elasticsearch.TraceReader
}

var _ TraceReader = (*traceReaderAdapter)(nil)

func (a *traceReaderAdapter) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {
	esQuery := elasticsearch.TraceQuery{
		ServiceName:   query.ServiceName,
		OperationName: query.OperationName,
		Tags:          query.Tags,
		MinDuration:   query.MinDuration,
		MaxDuration:   query.MaxDuration,
		TimeRange:     elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:         query.Limit,
		Offset:        query.Offset,
	}
	result, err := a.inner.SearchTraces(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertTraceSearchResult(result), nil
}

func (a *traceReaderAdapter) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	result, err := a.inner.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	t := convertTrace(*result)
	return &t, nil
}

func (a *traceReaderAdapter) GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetServices(ctx, esTimeRange)
	if err != nil {
		return nil, err
	}
	services := make([]Service, len(result))
	for i, s := range result {
		services[i] = Service{Name: s.Name}
	}
	return services, nil
}

func (a *traceReaderAdapter) GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetOperations(ctx, service, esTimeRange)
	if err != nil {
		return nil, err
	}
	ops := make([]Operation, len(result))
	for i, o := range result {
		ops[i] = Operation{Name: o.Name, SpanKind: o.SpanKind}
	}
	return ops, nil
}

func (a *traceReaderAdapter) GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.GetDependencies(ctx, esTimeRange)
	if err != nil {
		return nil, err
	}
	deps := make([]Dependency, len(result))
	for i, d := range result {
		deps[i] = Dependency{Parent: d.Parent, Child: d.Child, CallCount: d.CallCount}
	}
	return deps, nil
}

// logReaderAdapter adapts the ES LogReader to the public LogReader interface.
type logReaderAdapter struct {
	inner *elasticsearch.LogReader
}

var _ LogReader = (*logReaderAdapter)(nil)

func (a *logReaderAdapter) SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error) {
	esQuery := elasticsearch.LogQuery{
		Query:       query.Query,
		ServiceName: query.ServiceName,
		Severity:    query.Severity,
		TraceID:     query.TraceID,
		SpanID:      query.SpanID,
		Attributes:  query.Attributes,
		TimeRange:   elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:       query.Limit,
		Offset:      query.Offset,
	}
	result, err := a.inner.SearchLogs(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertLogSearchResult(result), nil
}

func (a *logReaderAdapter) GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error) {
	result, err := a.inner.GetLogContext(ctx, logID, lines)
	if err != nil {
		return nil, err
	}
	return convertLogContext(result), nil
}

func (a *logReaderAdapter) ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	result, err := a.inner.ListLogFields(ctx, esTimeRange)
	if err != nil {
		return nil, err
	}
	fields := make([]LogField, len(result))
	for i, f := range result {
		fields[i] = LogField{Name: f.Name, Type: f.Type, Count: f.Count}
	}
	return fields, nil
}

func (a *logReaderAdapter) GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error) {
	esQuery := elasticsearch.LogStatsQuery{
		ServiceName: query.ServiceName,
		TimeRange:   elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		GroupBy:     query.GroupBy,
	}
	result, err := a.inner.GetLogStats(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertLogStats(result), nil
}

// metricReaderAdapter adapts the ES MetricReader to the public MetricReader interface.
type metricReaderAdapter struct {
	inner *elasticsearch.MetricReader
}

var _ MetricReader = (*metricReaderAdapter)(nil)

func (a *metricReaderAdapter) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	esQuery := elasticsearch.MetricQuery{
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		ServiceName: query.ServiceName,
		Time:        query.Time,
	}
	result, err := a.inner.Query(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertMetricResult(result), nil
}

func (a *metricReaderAdapter) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	esQuery := elasticsearch.MetricRangeQuery{
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		ServiceName: query.ServiceName,
		TimeRange:   elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Step:        query.Step,
	}
	result, err := a.inner.QueryRange(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertMetricRangeResult(result), nil
}

func (a *metricReaderAdapter) ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListMetricNames(ctx, esTimeRange)
}

func (a *metricReaderAdapter) ListLabelNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListLabelNames(ctx, esTimeRange)
}

func (a *metricReaderAdapter) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	esTimeRange := elasticsearch.TimeRange{Start: timeRange.Start, End: timeRange.End}
	return a.inner.ListLabelValues(ctx, label, esTimeRange)
}

// ==================== Type Conversion Helpers ====================

func convertTraceSearchResult(src *elasticsearch.TraceSearchResult) *TraceSearchResult {
	traces := make([]Trace, len(src.Traces))
	for i, t := range src.Traces {
		traces[i] = convertTrace(t)
	}
	return &TraceSearchResult{Traces: traces, Total: src.Total}
}

func convertTrace(src elasticsearch.Trace) Trace {
	spans := make([]Span, len(src.Spans))
	for i, s := range src.Spans {
		spans[i] = convertSpan(s)
	}
	return Trace{TraceID: src.TraceID, Spans: spans, Duration: src.Duration}
}

func convertSpan(src elasticsearch.Span) Span {
	span := Span{
		TraceID:       src.TraceID,
		SpanID:        src.SpanID,
		ParentSpanID:  src.ParentSpanID,
		OperationName: src.OperationName,
		ServiceName:   src.ServiceName,
		SpanKind:      src.SpanKind,
		StatusCode:    src.StatusCode,
		StatusMessage: src.StatusMessage,
		StartTime:     src.StartTime,
		EndTime:       src.EndTime,
		DurationUS:    src.DurationUS,
		Attributes:    src.Attributes,
		Resource:      src.Resource,
	}
	if len(src.Events) > 0 {
		span.Events = make([]SpanEvent, len(src.Events))
		for i, e := range src.Events {
			span.Events[i] = SpanEvent{Name: e.Name, Timestamp: e.Timestamp, Attributes: e.Attributes}
		}
	}
	if len(src.Links) > 0 {
		span.Links = make([]SpanLink, len(src.Links))
		for i, l := range src.Links {
			span.Links[i] = SpanLink{TraceID: l.TraceID, SpanID: l.SpanID}
		}
	}
	return span
}

func convertLogSearchResult(src *elasticsearch.LogSearchResult) *LogSearchResult {
	logs := make([]LogRecord, len(src.Logs))
	for i, l := range src.Logs {
		logs[i] = convertLogRecord(l)
	}
	return &LogSearchResult{Logs: logs, Total: src.Total}
}

func convertLogRecord(src elasticsearch.LogRecord) LogRecord {
	return LogRecord{
		ID:             src.ID,
		Timestamp:      src.Timestamp,
		ObservedTime:   src.ObservedTime,
		TraceID:        src.TraceID,
		SpanID:         src.SpanID,
		Severity:       src.Severity,
		SeverityNumber: src.SeverityNumber,
		Body:           src.Body,
		ServiceName:    src.ServiceName,
		AppID:          src.AppID,
		Attributes:     src.Attributes,
		Resource:       src.Resource,
	}
}

func convertLogContext(src *elasticsearch.LogContext) *LogContext {
	before := make([]LogRecord, len(src.Before))
	for i, l := range src.Before {
		before[i] = convertLogRecord(l)
	}
	after := make([]LogRecord, len(src.After))
	for i, l := range src.After {
		after[i] = convertLogRecord(l)
	}
	return &LogContext{
		Before: before,
		Target: convertLogRecord(src.Target),
		After:  after,
	}
}

func convertLogStats(src *elasticsearch.LogStats) *LogStats {
	timeBuckets := make([]TimeBucket, len(src.TimeHistogram))
	for i, b := range src.TimeHistogram {
		timeBuckets[i] = TimeBucket{Time: b.Time, Count: b.Count}
	}
	return &LogStats{
		TotalCount:     src.TotalCount,
		SeverityCounts: src.SeverityCounts,
		ServiceCounts:  src.ServiceCounts,
		TimeHistogram:  timeBuckets,
	}
}

func convertMetricResult(src *elasticsearch.MetricResult) *MetricResult {
	data := make([]MetricDataPoint, len(src.Data))
	for i, d := range src.Data {
		data[i] = MetricDataPoint{Labels: d.Labels, Value: d.Value, Time: d.Time}
	}
	return &MetricResult{Data: data}
}

func convertMetricRangeResult(src *elasticsearch.MetricRangeResult) *MetricRangeResult {
	series := make([]MetricSeries, len(src.Data))
	for i, s := range src.Data {
		values := make([]MetricDataPoint, len(s.Values))
		for j, v := range s.Values {
			values[j] = MetricDataPoint{Labels: v.Labels, Value: v.Value, Time: v.Time}
		}
		series[i] = MetricSeries{Labels: s.Labels, Values: values}
	}
	return &MetricRangeResult{Data: series}
}

// ==================== Storage Admin Adapter ====================

// storageAdminAdapter adapts the ES Admin to the public StorageAdmin interface.
type storageAdminAdapter struct {
	inner  *elasticsearch.Admin
	config *Config
}

var _ StorageAdmin = (*storageAdminAdapter)(nil)

func (a *storageAdminAdapter) GetStatus(ctx context.Context) (*StorageStatus, error) {
	info, err := a.inner.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	healthy := true
	if status, ok := info["status"].(string); ok && status == "red" {
		healthy = false
	}
	version := ""
	if v, ok := info["cluster_name"].(string); ok {
		version = v
	}
	return &StorageStatus{
		Provider: "elasticsearch",
		Healthy:  healthy,
		Version:  version,
		Details:  info,
	}, nil
}

func (a *storageAdminAdapter) InitSchema(ctx context.Context) error {
	return a.inner.InitSchema(ctx)
}

func (a *storageAdminAdapter) GetRetention(_ context.Context) (map[SignalType]RetentionPolicy, error) {
	result := make(map[SignalType]RetentionPolicy)
	if a.config != nil && a.config.Elasticsearch.Traces.Retention > 0 {
		result[SignalTrace] = RetentionPolicy{Duration: a.config.Elasticsearch.Traces.Retention}
	}
	if a.config != nil && a.config.Elasticsearch.Metrics.Retention > 0 {
		result[SignalMetric] = RetentionPolicy{Duration: a.config.Elasticsearch.Metrics.Retention}
	}
	if a.config != nil && a.config.Elasticsearch.Logs.Retention > 0 {
		result[SignalLog] = RetentionPolicy{Duration: a.config.Elasticsearch.Logs.Retention}
	}
	return result, nil
}

func (a *storageAdminAdapter) SetRetention(ctx context.Context, signal SignalType, policy RetentionPolicy) error {
	indexPrefix, err := a.indexPrefixForSignal(signal)
	if err != nil {
		return err
	}
	return a.inner.SetRetention(ctx, indexPrefix, policy.Duration)
}

func (a *storageAdminAdapter) Purge(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
	indexPrefix, err := a.indexPrefixForSignal(signal)
	if err != nil {
		return nil, err
	}
	timestampField := a.timestampFieldForSignal(signal)

	deleted, err := a.inner.Purge(ctx, indexPrefix, timestampField, before)
	if err != nil {
		return nil, err
	}
	return &PurgeResult{
		DeletedCount: deleted,
		Message:      fmt.Sprintf("Purged %d documents from %s-* before %s", deleted, indexPrefix, before.Format(time.RFC3339)),
	}, nil
}

func (a *storageAdminAdapter) PurgeByApp(ctx context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error) {
	indexPrefix, err := a.indexPrefixForSignal(signal)
	if err != nil {
		return nil, err
	}
	timestampField := a.timestampFieldForSignal(signal)

	deleted, err := a.inner.PurgeByApp(ctx, indexPrefix, timestampField, appID, before)
	if err != nil {
		return nil, err
	}
	return &PurgeResult{
		DeletedCount: deleted,
		Message:      fmt.Sprintf("Purged %d documents for app %s from %s-* before %s", deleted, appID, indexPrefix, before.Format(time.RFC3339)),
	}, nil
}

// indexPrefixForSignal returns the ES index prefix for the given signal type.
func (a *storageAdminAdapter) indexPrefixForSignal(signal SignalType) (string, error) {
	if a.config == nil || a.config.Elasticsearch == nil {
		return "", fmt.Errorf("elasticsearch config is not available")
	}
	switch signal {
	case SignalTrace:
		return a.config.Elasticsearch.Traces.IndexPrefix, nil
	case SignalMetric:
		return a.config.Elasticsearch.Metrics.IndexPrefix, nil
	case SignalLog:
		return a.config.Elasticsearch.Logs.IndexPrefix, nil
	default:
		return "", fmt.Errorf("unknown signal type: %s", signal)
	}
}

// timestampFieldForSignal returns the timestamp field name used in ES documents for the given signal.
func (a *storageAdminAdapter) timestampFieldForSignal(signal SignalType) string {
	switch signal {
	case SignalTrace:
		return "start_time"
	case SignalMetric:
		return "@timestamp"
	case SignalLog:
		return "timestamp"
	default:
		return "@timestamp"
	}
}

func (a *storageAdminAdapter) GetDiskUsage(ctx context.Context) (*DiskUsage, error) {
	stats, err := a.inner.GetIndicesStats(ctx)
	if err != nil {
		return nil, err
	}

	usage := &DiskUsage{
		BySignal: make(map[SignalType]int64),
	}

	// Parse the indices stats response
	if all, ok := stats["_all"].(map[string]any); ok {
		if total, ok := all["total"].(map[string]any); ok {
			if store, ok := total["store"].(map[string]any); ok {
				if sizeBytes, ok := store["size_in_bytes"].(float64); ok {
					usage.UsedBytes = int64(sizeBytes)
				}
			}
		}
	}

	return usage, nil
}
