// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

const (
	maxMetricPointsPerSeries = 200
	maxTraceDetailSpans      = 50
)

// ========== Parameter helpers ==========

func intArg(args map[string]any, name string, def int) int {
	switch value := args[name].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return def
}

func stringMapArg(args map[string]any, name string) map[string]string {
	raw, ok := args[name].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		if str, ok := value.(string); ok {
			out[key] = str
		}
	}
	return out
}

func stringSliceArg(args map[string]any, name string) []string {
	switch raw := args[name].(type) {
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return raw
	case string:
		if raw != "" {
			return []string{raw}
		}
	}
	return nil
}

func parseRFC3339(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("时间参数 %q 不是 RFC3339 格式", value)
	}
	return parsed.UTC(), nil
}

func (w *mcpServerWrapper) observabilityTimeRange(ctx context.Context, args map[string]any) (observabilitystorageext.TimeRange, error) {
	settings := w.observabilitySettings(ctx)
	now := time.Now().UTC()
	start, err := parseRFC3339(stringArg(args, "start"), now.Add(-time.Hour))
	if err != nil {
		return observabilitystorageext.TimeRange{}, err
	}
	end, err := parseRFC3339(stringArg(args, "end"), now)
	if err != nil {
		return observabilitystorageext.TimeRange{}, err
	}
	if !end.After(start) {
		return observabilitystorageext.TimeRange{}, errors.New("end 必须晚于 start")
	}
	if end.Sub(start) > settings.MaxTimeRange {
		return observabilitystorageext.TimeRange{}, fmt.Errorf(
			"查询时间窗不能超过 %s", settings.MaxTimeRangeText)
	}
	return observabilitystorageext.TimeRange{Start: start, End: end}, nil
}

// observabilityQueryContext keeps the authenticated tenant in the context
// (WithIdentity already mirrored it into tenantctx) and bounds query latency.
func (w *mcpServerWrapper) observabilityQueryContext(
	ctx context.Context,
) (context.Context, context.CancelFunc, string) {
	identity, _ := tenantauth.IdentityFromContext(ctx)
	queryCtx, cancel := context.WithTimeout(ctx, w.observabilitySettings(ctx).QueryTimeout)
	return queryCtx, cancel, identity.TenantID
}

func observabilityQueryError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError("查询失败: " + err.Error()), nil
}

// ========== Registration ==========

func (w *mcpServerWrapper) registerObservabilityTools() {
	cfg := w.ext.config.Observability
	if !cfg.Enabled {
		return
	}
	if cfg.Signals.Metric && w.ext.metricReader != nil {
		w.registerMetricQueryTool()
		w.registerMetricQueryRangeTool()
		w.registerMetricListNamesTool()
		w.registerMetricLabelValuesTool()
	}
	if cfg.Signals.Trace && w.ext.traceReader != nil {
		w.registerTraceSearchTool()
		w.registerTraceGetTool()
		w.registerTraceServicesTool()
	}
	if cfg.Signals.Log && w.ext.logReader != nil {
		w.registerLogSearchTool()
		w.registerLogContextTool()
	}
}

func (w *mcpServerWrapper) registerMetricQueryTool() {
	tool := mcp.NewTool("metric_query",
		mcp.WithDescription("查询指标在某一时刻的瞬时值。按结构化参数查询，不接受 PromQL 表达式；租户由服务端根据认证身份决定。"),
		mcp.WithString("metric", mcp.Required(), mcp.Description("指标名，例如 http_server_duration_milliseconds_count。")),
		mcp.WithString("service", mcp.Description("服务名，精确匹配。")),
		mcp.WithString("app_id", mcp.Description("应用 ID；提供时会校验其是否属于调用者租户。")),
		mcp.WithObject("labels", mcp.Description("标签精确匹配，JSON 对象，例如 {\"http.method\":\"GET\"}。")),
		mcp.WithString("time", mcp.Description("查询时刻，RFC3339；不填则为当前时间。")),
	)
	w.addTool(tool, w.handleMetricQuery)
}

func (w *mcpServerWrapper) registerMetricQueryRangeTool() {
	tool := mcp.NewTool("metric_query_range",
		mcp.WithDescription("查询指标在一段时间内的时间序列。时间窗受配置上限约束；不接受 PromQL 表达式。"),
		mcp.WithString("metric", mcp.Required(), mcp.Description("指标名。")),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
		mcp.WithString("step", mcp.Description("采样间隔，Go duration 字符串，例如 1m；默认 1m。")),
		mcp.WithString("aggregation", mcp.Description("聚合方式：avg / sum / min / max / count；默认 avg。")),
		mcp.WithArray("group_by", mcp.Description("分组标签名列表。")),
		mcp.WithNumber("series_limit", mcp.Description("返回的最大序列数，受配置上限约束。")),
		mcp.WithString("service", mcp.Description("服务名。")),
		mcp.WithString("app_id", mcp.Description("应用 ID；提供时会校验租户归属。")),
		mcp.WithObject("labels", mcp.Description("标签精确匹配，JSON 对象。")),
	)
	w.addTool(tool, w.handleMetricQueryRange)
}

func (w *mcpServerWrapper) registerMetricListNamesTool() {
	tool := mcp.NewTool("metric_list_names",
		mcp.WithDescription("列出时间范围内可用的指标名，供选择 metric_query 的 metric 参数。"),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
	)
	w.addTool(tool, w.handleMetricListNames)
}

func (w *mcpServerWrapper) registerMetricLabelValuesTool() {
	tool := mcp.NewTool("metric_label_values",
		mcp.WithDescription("列出某个标签在时间范围内的取值，可限定指标名。"),
		mcp.WithString("label", mcp.Required(), mcp.Description("标签名，例如 http.method。")),
		mcp.WithString("metric", mcp.Description("限定到某个指标名；不填则跨指标。")),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
	)
	w.addTool(tool, w.handleMetricLabelValues)
}

func (w *mcpServerWrapper) registerTraceSearchTool() {
	tool := mcp.NewTool("trace_search",
		mcp.WithDescription("按服务、操作、标签和耗时范围搜索链路，返回轻量摘要（含 trace_id，可交给 trace_get）。不接受 TraceQL 表达式。"),
		mcp.WithString("service", mcp.Description("服务名。")),
		mcp.WithString("operation", mcp.Description("操作 / span 名称。")),
		mcp.WithString("app_id", mcp.Description("应用 ID；提供时会校验租户归属。")),
		mcp.WithObject("tags", mcp.Description("标签精确匹配，JSON 对象。")),
		mcp.WithString("min_duration", mcp.Description("最小时长，Go duration 字符串，例如 100ms。")),
		mcp.WithString("max_duration", mcp.Description("最大时长，Go duration 字符串。")),
		mcp.WithString("status", mcp.Description("span 状态：ok / error / unset。")),
		mcp.WithBoolean("is_root", mcp.Description("是否只查根 span。")),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
		mcp.WithNumber("limit", mcp.Description("返回条数，受配置上限约束。")),
	)
	w.addTool(tool, w.handleTraceSearch)
}

func (w *mcpServerWrapper) registerTraceGetTool() {
	tool := mcp.NewTool("trace_get",
		mcp.WithDescription("按 trace_id 获取链路详情与精简的 span 列表。"),
		mcp.WithString("trace_id", mcp.Required(), mcp.Description("链路 ID，可从 trace_search 获取。")),
	)
	w.addTool(tool, w.handleTraceGet)
}

func (w *mcpServerWrapper) registerTraceServicesTool() {
	tool := mcp.NewTool("trace_services",
		mcp.WithDescription("列出时间范围内出现过的服务名，供 trace_search 选择 service 参数。"),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
	)
	w.addTool(tool, w.handleTraceServices)
}

func (w *mcpServerWrapper) registerLogSearchTool() {
	tool := mcp.NewTool("log_search",
		mcp.WithDescription("按关键字、服务、严重级别与 trace_id 搜索日志。不接受 LogQL 表达式；租户由服务端决定。"),
		mcp.WithString("query", mcp.Description("日志正文关键字（自由文本，非表达式）。")),
		mcp.WithString("service", mcp.Description("服务名。")),
		mcp.WithString("app_id", mcp.Description("应用 ID；提供时会校验租户归属。")),
		mcp.WithString("trace_id", mcp.Description("按链路 ID 过滤。")),
		mcp.WithArray("severity", mcp.Description("严重级别列表，例如 [\"ERROR\",\"WARN\"]。")),
		mcp.WithString("direction", mcp.Description("排序方向：forward / backward。")),
		mcp.WithString("start", mcp.Description("开始时间，RFC3339；默认最近 1 小时。")),
		mcp.WithString("end", mcp.Description("结束时间，RFC3339；默认当前时间。")),
		mcp.WithNumber("limit", mcp.Description("返回条数，受配置上限约束。")),
	)
	w.addTool(tool, w.handleLogSearch)
}

func (w *mcpServerWrapper) registerLogContextTool() {
	tool := mcp.NewTool("log_context",
		mcp.WithDescription("按日志 ID 获取目标日志前后的上下文行，用于定位问题现场。"),
		mcp.WithString("log_id", mcp.Required(), mcp.Description("日志 ID，可从 log_search 获取。")),
		mcp.WithNumber("lines", mcp.Description("前后各取多少行，受配置上限约束。")),
	)
	w.addTool(tool, w.handleLogContext)
}

// ========== Metric handlers ==========

func (w *mcpServerWrapper) handleMetricQuery(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.metricReader == nil {
		return mcp.NewToolResultError("metric 查询能力未启用或 Reader 不可用"), nil
	}

	args := request.GetArguments()
	metric := stringArg(args, "metric")
	if metric == "" {
		return mcp.NewToolResultError("参数 metric 不能为空"), nil
	}
	at, err := parseRFC3339(stringArg(args, "time"), time.Now().UTC())
	if err != nil {
		return observabilityQueryError(err)
	}

	result, err := w.ext.metricReader.Query(queryCtx, observabilitystorageext.MetricQuery{
		TenantID:    tenantID,
		AppID:       stringArg(args, "app_id"),
		MetricName:  metric,
		ServiceName: stringArg(args, "service"),
		Labels:      stringMapArg(args, "labels"),
		Time:        at,
	})
	if err != nil {
		return observabilityQueryError(err)
	}
	limit := w.observabilitySettings(ctx).Limits.MetricSeries
	items, truncated := metricPointViews(result.Data, limit)
	return observabilityResult("metric", "metric_query", tenantID, items, len(items), int64(len(result.Data)), truncated)
}

func (w *mcpServerWrapper) handleMetricQueryRange(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.metricReader == nil {
		return mcp.NewToolResultError("metric 查询能力未启用或 Reader 不可用"), nil
	}

	args := request.GetArguments()
	metric := stringArg(args, "metric")
	if metric == "" {
		return mcp.NewToolResultError("参数 metric 不能为空"), nil
	}
	timeRange, err := w.observabilityTimeRange(ctx, args)
	if err != nil {
		return observabilityQueryError(err)
	}
	step, err := time.ParseDuration(stringArgDefault(stringArg(args, "step"), "1m"))
	if err != nil || step <= 0 {
		return mcp.NewToolResultError("参数 step 必须是正的 Go duration，例如 1m"), nil
	}
	seriesLimit := intArg(args, "series_limit", w.observabilitySettings(ctx).Limits.MetricSeries)
	if seriesLimit <= 0 || seriesLimit > w.observabilitySettings(ctx).Limits.MetricSeries {
		seriesLimit = w.observabilitySettings(ctx).Limits.MetricSeries
	}

	result, err := w.ext.metricReader.QueryRange(queryCtx, observabilitystorageext.MetricRangeQuery{
		TenantID:    tenantID,
		AppID:       stringArg(args, "app_id"),
		MetricName:  metric,
		ServiceName: stringArg(args, "service"),
		Labels:      stringMapArg(args, "labels"),
		TimeRange:   timeRange,
		Aggregation: stringArgDefault(stringArg(args, "aggregation"), "avg"),
		Step:        step,
		GroupBy:     stringSliceArg(args, "group_by"),
		SeriesLimit: seriesLimit,
		Limit:       seriesLimit * maxMetricPointsPerSeries,
	})
	if err != nil {
		return observabilityQueryError(err)
	}
	items, truncated := metricSeriesViews(result.Data, seriesLimit, maxMetricPointsPerSeries)
	return observabilityResult("metric", "metric_query_range", tenantID, items, len(items), int64(len(result.Data)), truncated)
}

func (w *mcpServerWrapper) handleMetricListNames(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.metricReader == nil {
		return mcp.NewToolResultError("metric 查询能力未启用或 Reader 不可用"), nil
	}
	timeRange, err := w.observabilityTimeRange(ctx, request.GetArguments())
	if err != nil {
		return observabilityQueryError(err)
	}
	names, err := w.ext.metricReader.ListMetricNames(queryCtx, timeRange)
	if err != nil {
		return observabilityQueryError(err)
	}
	items, truncated := truncateStrings(names, w.observabilitySettings(ctx).Limits.MetricSeries)
	return observabilityResult("metric", "metric_list_names", tenantID, items, len(items), int64(len(names)), truncated)
}

func (w *mcpServerWrapper) handleMetricLabelValues(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.metricReader == nil {
		return mcp.NewToolResultError("metric 查询能力未启用或 Reader 不可用"), nil
	}
	args := request.GetArguments()
	label := stringArg(args, "label")
	if label == "" {
		return mcp.NewToolResultError("参数 label 不能为空"), nil
	}
	timeRange, err := w.observabilityTimeRange(ctx, args)
	if err != nil {
		return observabilityQueryError(err)
	}
	values, err := w.ext.metricReader.ListLabelValuesForMetric(queryCtx, label, stringArg(args, "metric"), timeRange)
	if err != nil {
		return observabilityQueryError(err)
	}
	items, truncated := truncateStrings(values, w.observabilitySettings(ctx).Limits.MetricSeries)
	return observabilityResult("metric", "metric_label_values", tenantID, items, len(items), int64(len(values)), truncated)
}

// ========== Trace handlers ==========

func (w *mcpServerWrapper) handleTraceSearch(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.traceReader == nil {
		return mcp.NewToolResultError("trace 查询能力未启用或 Reader 不可用"), nil
	}
	args := request.GetArguments()
	timeRange, err := w.observabilityTimeRange(ctx, args)
	if err != nil {
		return observabilityQueryError(err)
	}
	query := observabilitystorageext.TraceQuery{
		TenantID:      tenantID,
		AppID:         stringArg(args, "app_id"),
		ServiceName:   stringArg(args, "service"),
		OperationName: stringArg(args, "operation"),
		Tags:          stringMapArg(args, "tags"),
		TimeRange:     timeRange,
		Status:        stringArg(args, "status"),
		IsRoot:        boolArg(args, "is_root"),
		Limit:         w.observabilityLimit(args, "limit", w.observabilitySettings(ctx).Limits.Traces),
	}
	if minDuration := stringArg(args, "min_duration"); minDuration != "" {
		parsed, err := time.ParseDuration(minDuration)
		if err != nil {
			return mcp.NewToolResultError("参数 min_duration 必须是 Go duration，例如 100ms"), nil
		}
		query.MinDuration = parsed
	}
	if maxDuration := stringArg(args, "max_duration"); maxDuration != "" {
		parsed, err := time.ParseDuration(maxDuration)
		if err != nil {
			return mcp.NewToolResultError("参数 max_duration 必须是 Go duration，例如 5s"), nil
		}
		query.MaxDuration = parsed
	}

	result, err := w.ext.traceReader.SearchTraceSummaries(queryCtx, query, 0)
	if err != nil {
		return observabilityQueryError(err)
	}
	items, truncated := traceSummaryViews(result.Summaries, query.Limit)
	return observabilityResult("trace", "trace_search", tenantID, items, len(items), result.Total, truncated)
}

func (w *mcpServerWrapper) handleTraceGet(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.traceReader == nil {
		return mcp.NewToolResultError("trace 查询能力未启用或 Reader 不可用"), nil
	}
	traceID := stringArg(request.GetArguments(), "trace_id")
	if traceID == "" {
		return mcp.NewToolResultError("参数 trace_id 不能为空"), nil
	}
	trace, err := w.ext.traceReader.GetTrace(queryCtx, traceID)
	if err != nil {
		return observabilityQueryError(err)
	}
	if trace == nil {
		return mcp.NewToolResultError("未找到该链路"), nil
	}

	spans := trace.Spans
	truncated := len(spans) > maxTraceDetailSpans
	if truncated {
		spans = spans[:maxTraceDetailSpans]
	}
	views := make([]traceSpanView, 0, len(spans))
	for _, span := range spans {
		views = append(views, traceSpanView{
			SpanID:      span.SpanID,
			Name:        span.Name,
			ServiceName: span.ServiceName,
			DurationNs:  span.DurationNano,
			Status:      string(span.Status.Code),
		})
	}
	item := traceDetailView{
		TraceID:         trace.TraceID,
		RootServiceName: trace.RootServiceName,
		RootSpanName:    trace.RootSpanName,
		SpanCount:       trace.SpanCount,
		ServiceCount:    trace.ServiceCount,
		Spans:           views,
	}
	return observabilityResult("trace", "trace_get", tenantID, []traceDetailView{item}, 1, int64(trace.SpanCount), truncated)
}

func (w *mcpServerWrapper) handleTraceServices(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.traceReader == nil {
		return mcp.NewToolResultError("trace 查询能力未启用或 Reader 不可用"), nil
	}
	timeRange, err := w.observabilityTimeRange(ctx, request.GetArguments())
	if err != nil {
		return observabilityQueryError(err)
	}
	services, err := w.ext.traceReader.GetServices(queryCtx, timeRange)
	if err != nil {
		return observabilityQueryError(err)
	}
	limit := w.observabilitySettings(ctx).Limits.MetricSeries
	truncated := len(services) > limit
	if truncated {
		services = services[:limit]
	}
	items := make([]serviceView, 0, len(services))
	for _, service := range services {
		items = append(items, serviceView{Name: service.Name, SpanCount: service.SpanCount})
	}
	return observabilityResult("trace", "trace_services", tenantID, items, len(items), int64(len(services)), truncated)
}

// ========== Log handlers ==========

func (w *mcpServerWrapper) handleLogSearch(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.logReader == nil {
		return mcp.NewToolResultError("log 查询能力未启用或 Reader 不可用"), nil
	}
	args := request.GetArguments()
	timeRange, err := w.observabilityTimeRange(ctx, args)
	if err != nil {
		return observabilityQueryError(err)
	}
	result, err := w.ext.logReader.SearchLogs(queryCtx, observabilitystorageext.LogQuery{
		TenantID:    tenantID,
		AppID:       stringArg(args, "app_id"),
		Query:       stringArg(args, "query"),
		ServiceName: stringArg(args, "service"),
		TraceID:     stringArg(args, "trace_id"),
		Severity:    stringSliceArg(args, "severity"),
		Direction:   stringArg(args, "direction"),
		TimeRange:   timeRange,
		Limit:       w.observabilityLimit(args, "limit", w.observabilitySettings(ctx).Limits.Logs),
	})
	if err != nil {
		return observabilityQueryError(err)
	}
	items, truncated := logRecordViews(result.Logs, w.observabilitySettings(ctx).Limits.Logs)
	return observabilityResult("log", "log_search", tenantID, items, len(items), result.Total, truncated)
}

func (w *mcpServerWrapper) handleLogContext(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	queryCtx, cancel, tenantID := w.observabilityQueryContext(ctx)
	defer cancel()
	if w.ext.logReader == nil {
		return mcp.NewToolResultError("log 查询能力未启用或 Reader 不可用"), nil
	}
	args := request.GetArguments()
	logID := stringArg(args, "log_id")
	if logID == "" {
		return mcp.NewToolResultError("参数 log_id 不能为空"), nil
	}
	lines := w.observabilityLimit(args, "lines", w.observabilitySettings(ctx).Limits.LogContextLines)
	contextResult, err := w.ext.logReader.GetLogContext(queryCtx, logID, lines)
	if err != nil {
		return observabilityQueryError(err)
	}
	if contextResult == nil {
		return mcp.NewToolResultError("未找到该日志"), nil
	}

	before, beforeTruncated := logRecordViews(contextResult.Before, lines)
	after, afterTruncated := logRecordViews(contextResult.After, lines)
	target, targetTruncated := logRecordViews([]observabilitystorageext.LogRecord{contextResult.Target}, 1)
	item := map[string]any{
		"target": target,
		"before": before,
		"after":  after,
	}
	truncated := beforeTruncated || afterTruncated || targetTruncated
	return observabilityResult("log", "log_context", tenantID, []any{item}, 1, 0, truncated)
}

// ========== Small helpers ==========

func (w *mcpServerWrapper) observabilityLimit(args map[string]any, name string, max int) int {
	value := intArg(args, name, max)
	if value <= 0 || value > max {
		return max
	}
	return value
}

func stringArgDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
