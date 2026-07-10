// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// Reader Adapter — converts between ES-internal types
// and the public OTel-standard types defined in types.go.
// This avoids circular imports while keeping a clean
// public API for consumers (e.g., adminext handlers).
// ═══════════════════════════════════════════════════

// traceReaderAdapter adapts the ES TraceReader to the public TraceReader interface.
type traceReaderAdapter struct {
	inner *elasticsearch.TraceReader
}

var _ TraceReader = (*traceReaderAdapter)(nil)

func (a *traceReaderAdapter) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	spans, err := a.inner.GetTraceSpans(ctx, traceID)
	if err != nil {
		return nil, err
	}
	return buildTraceFromStoredSpans(spans), nil
}

func (a *traceReaderAdapter) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {
	q := storedmodel.TraceQuery{
		AppID:         query.AppID,
		ServiceName:   query.ServiceName,
		OperationName: query.OperationName,
		Tags:          query.Tags,
		MinDuration:   query.MinDuration,
		MaxDuration:   query.MaxDuration,
		TimeRange:     storedmodel.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:         query.Limit,
		Offset:        query.Offset,
	}
	spans, _, err := a.inner.SearchSpans(ctx, q)
	if err != nil {
		return nil, err
	}

	// Group spans by trace_id
	traceMap := make(map[string][]StoredSpan)
	for _, ss := range spans {
		traceMap[ss.TraceID] = append(traceMap[ss.TraceID], ss)
	}

	// Keep original order
	var seen []string
	traces := make([]Trace, 0, len(traceMap))
	for _, ss := range spans {
		tid := ss.TraceID
		if sp, ok := traceMap[tid]; ok {
			seen = append(seen, tid)
			traces = append(traces, *buildTraceFromStoredSpans(sp))
			delete(traceMap, tid)
		}
	}

	return &TraceSearchResult{Traces: traces, Total: int64(len(seen))}, nil
}

// buildTraceFromStoredSpans builds a public Trace from StoredSpan slice.
func buildTraceFromStoredSpans(spans []StoredSpan) *Trace {
	publicSpans := make([]Span, len(spans))
	for i, ss := range spans {
		publicSpans[i] = StoredSpanToPublic(ss)
	}

	trace := &Trace{
		TraceID:   spans[0].TraceID,
		Spans:     publicSpans,
		SpanCount: len(publicSpans),
	}

	// Compute derived fields
	serviceSet := make(map[string]struct{})
	for _, s := range publicSpans {
		if s.ServiceName != "" {
			serviceSet[s.ServiceName] = struct{}{}
		}
		if s.ParentSpanID == "" {
			trace.RootServiceName = s.ServiceName
			trace.RootSpanName = s.Name
		}
	}
	trace.ServiceCount = len(serviceSet)
	trace.DurationNano = traceDuration(spans)

	return trace
}

// traceDuration computes the total trace duration from StoredSpan range.
func traceDuration(spans []StoredSpan) string {
	var minStart, maxEnd int64
	for _, ss := range spans {
		if minStart == 0 || ss.StartUnixNano < minStart {
			minStart = ss.StartUnixNano
		}
		if ss.EndUnixNano > maxEnd {
			maxEnd = ss.EndUnixNano
		}
	}
	if minStart > 0 && maxEnd > 0 {
		return strconv.FormatInt(maxEnd-minStart, 10)
	}
	return "0"
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
		ops[i] = Operation{Name: o.Name, SpanKind: NormalizeSpanKind(o.SpanKind)}
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
		AppID:       query.AppID,
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
		AppID:       query.AppID,
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
		AppID:       query.AppID,
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
		AppID:       query.AppID,
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		LabelMatch:  query.LabelMatch,
		ServiceName: query.ServiceName,
		TimeRange:   elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Aggregation: query.Aggregation,
		Step:        query.Step,
		GroupBy:     query.GroupBy,
		Fill:        query.Fill,
		Limit:       query.Limit,
		SeriesLimit: query.SeriesLimit,
	}
	result, err := a.inner.QueryRange(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	return convertMetricRangeResult(result), nil
}

func (a *metricReaderAdapter) QueryRaw(ctx context.Context, query MetricRawQuery) ([]MetricRawSeries, error) {
	esQuery := elasticsearch.MetricRawQuery{
		AppID:       query.AppID,
		MetricName:  query.MetricName,
		Labels:      query.Labels,
		LabelMatch:  query.LabelMatch,
		ServiceName: query.ServiceName,
		TimeRange:   elasticsearch.TimeRange{Start: query.TimeRange.Start, End: query.TimeRange.End},
		Limit:       query.Limit,
	}
	rawSeries, err := a.inner.QueryRaw(ctx, esQuery)
	if err != nil {
		return nil, err
	}
	result := make([]MetricRawSeries, len(rawSeries))
	for i, s := range rawSeries {
		samples := make([]MetricSample, len(s.Samples))
		for j, sm := range s.Samples {
			samples[j] = MetricSample{
				TimestampMs: sm.TimestampMs,
				Value:       sm.Value,
			}
		}
		result[i] = MetricRawSeries{
			Labels:  s.Labels,
			Samples: samples,
		}
	}
	return result, nil
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

// ═══════════════════════════════════════════════════
// Type Conversion Helpers — ES internal → OTel standard
// ═══════════════════════════════════════════════════
// convertSpan / convertTrace / convertTraceSearchResult removed — replaced by
// StoredSpanToPublic() and buildTraceFromStoredSpans() using the canonical format.

// computeDurationNano is retained for the PG reader adapter.
func computeDurationNano(start, end time.Time, durationUS int64) string {
	if !start.IsZero() && !end.IsZero() {
		nanos := end.Sub(start).Nanoseconds()
		if nanos > 0 {
			return strconv.FormatInt(nanos, 10)
		}
	}
	if durationUS > 0 {
		return strconv.FormatInt(durationUS*1000, 10)
	}
	return "0"
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
		ID:                   src.ID,
		TimeUnixNano:         TimeToUnixNano(src.Timestamp),
		ObservedTimeUnixNano: TimeToUnixNano(src.ObservedTime),
		TraceID:              src.TraceID,
		SpanID:               src.SpanID,
		SeverityNumber:       src.SeverityNumber,
		SeverityText:         src.Severity,
		Body:                 src.Body,
		Attributes:           MapToKeyValues(src.Attributes),
		Resource:             MapToKeyValues(src.Resource),
		ServiceName:          src.ServiceName,
		AppID:                src.AppID,
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
		timeBuckets[i] = TimeBucket{
			TimeUnixNano: TimeToUnixNano(b.Time),
			Count:        b.Count,
		}
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
		data[i] = MetricDataPoint{
			Labels:        d.Labels,
			Value:         d.Value,
			TimeUnixMilli: TimeToUnixMilli(d.Time),
		}
	}
	return &MetricResult{Data: data}
}

func convertMetricRangeResult(src *elasticsearch.MetricRangeResult) *MetricRangeResult {
	series := make([]MetricSeries, len(src.Data))
	for i, s := range src.Data {
		// Filter out NilValue sentinel points (empty buckets for fill=null).
		// Fill strategies (0/previous/linear) will have already replaced these.
		values := make([]MetricTimeValue, 0, len(s.Values))
		for _, v := range s.Values {
			if isNilValue(v.Value) {
				continue
			}
			values = append(values, MetricTimeValue{
				TimeUnixMilli: TimeToUnixMilli(v.Time),
				Value:         v.Value,
			})
		}
		series[i] = MetricSeries{Labels: s.Labels, Values: values}
	}
	return &MetricRangeResult{Data: series}
}

// isNilValue returns true if the value is the sentinel for empty/nil metric points.
func isNilValue(v float64) bool {
	return v != v // NaN check: NaN != NaN is always true
}

// ═══════════════════════════════════════════════════
// Storage Admin Adapter
// ═══════════════════════════════════════════════════

// storageAdminAdapter adapts the ES Admin to the public StorageAdmin interface.
type storageAdminAdapter struct {
	inner          *elasticsearch.Admin
	config         *Config
	retentionStore lifecycle.RetentionStore
}

// configKey returns the RetentionStore key for the signal's retention config.
func (a *storageAdminAdapter) configKey(signal SignalType) string {
	return "platform_default/" + string(signal)
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

func (a *storageAdminAdapter) GetRetention(ctx context.Context) (map[SignalType]RetentionPolicy, error) {
	result := make(map[SignalType]RetentionPolicy)
	signals := []SignalType{SignalTrace, SignalMetric, SignalLog}

	for _, signal := range signals {
		var duration time.Duration
		// 1. Priority: read from RetentionStore (app config, source of truth)
		if a.retentionStore != nil {
			if d, err := a.retentionStore.GetForApp(ctx, a.configKey(signal), lifecycle.SignalType(signal)); err == nil && d != nil && *d > 0 {
				duration = *d
			}
		}
		// 2. Fallback: platform default from config.yaml (not RetentionStore)
		if duration == 0 {
			duration = a.configDefault(signal)
		}
		if duration > 0 {
			result[signal] = RetentionPolicy{Duration: RetentionDuration(duration)}
		}
	}
	return result, nil
}

// configDefault returns the platform-level default retention for a signal from config.yaml.
func (a *storageAdminAdapter) configDefault(signal SignalType) time.Duration {
	if a.config == nil || a.config.Elasticsearch == nil {
		return 0
	}
	switch signal {
	case SignalTrace:
		return a.config.Elasticsearch.Traces.Retention
	case SignalMetric:
		return a.config.Elasticsearch.Metrics.Retention
	case SignalLog:
		return a.config.Elasticsearch.Logs.Retention
	}
	return 0
}

func (a *storageAdminAdapter) SetRetention(ctx context.Context, signal SignalType, policy RetentionPolicy) error {
	indexPrefix, err := a.indexPrefixForSignal(signal)
	if err != nil {
		return err
	}
	// 1. Write to ES ILM policy (execution layer)
	if err := a.inner.SetRetention(ctx, indexPrefix, time.Duration(policy.Duration)); err != nil {
		return err
	}
	// 2. Write to RetentionStore (app config, source of truth)
	if a.retentionStore != nil {
		if err := a.retentionStore.SetForApp(ctx, a.configKey(signal), lifecycle.SignalType(signal), time.Duration(policy.Duration)); err != nil {
			return err
		}
	}
	// NOTE: a.config.X.Retention is the platform default from config.yaml, NOT updated here.
	return nil
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

// classifyIndexSignal maps an ES index name to its signal type using configured index prefixes.
func (a *storageAdminAdapter) classifyIndexSignal(indexName string) SignalType {
	if a.config == nil || a.config.Elasticsearch == nil {
		return ""
	}
	switch {
	case strings.HasPrefix(indexName, a.config.Elasticsearch.Traces.IndexPrefix):
		return SignalTrace
	case strings.HasPrefix(indexName, a.config.Elasticsearch.Metrics.IndexPrefix):
		return SignalMetric
	case strings.HasPrefix(indexName, a.config.Elasticsearch.Logs.IndexPrefix):
		return SignalLog
	default:
		return ""
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
		ByApp:    make(map[string]int64),
	}

	// Parse used bytes from indices stats
	if all, ok := stats["_all"].(map[string]any); ok {
		if total, ok := all["total"].(map[string]any); ok {
			if store, ok := total["store"].(map[string]any); ok {
				if sizeBytes, ok := store["size_in_bytes"].(float64); ok {
					usage.UsedBytes = int64(sizeBytes)
				}
			}
		}
	}

	// Parse per-signal breakdown
	if indices, ok := stats["indices"].(map[string]any); ok {
		for name, data := range indices {
			if idxMap, ok := data.(map[string]any); ok {
				var size int64
				if total, ok := idxMap["total"].(map[string]any); ok {
					if store, ok := total["store"].(map[string]any); ok {
						if s, ok := store["size_in_bytes"].(float64); ok {
							size = int64(s)
						}
					}
				}
				if size > 0 {
					signal := a.classifyIndexSignal(name)
					if signal != "" {
						usage.BySignal[signal] += size
					}
				}
			}
		}
	}

	// Populate total and available bytes from ES nodes filesystem stats.
	// If nodes stats are unavailable (e.g., restricted permissions), TotalBytes
	// falls back to UsedBytes so the progress bar is at least visible.
	totalBytes, availableBytes, nodeErr := a.inner.GetNodesDiskStats(ctx)
	if nodeErr != nil {
		usage.TotalBytes = usage.UsedBytes
	} else {
		usage.TotalBytes = totalBytes
		usage.AvailableBytes = availableBytes
	}

	return usage, nil
}
