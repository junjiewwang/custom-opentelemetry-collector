// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// The views below keep MCP responses small and stable. Only fields an AI agent
// needs for diagnosis are exposed instead of serializing internal models, and
// every response states explicitly whether it was truncated.

const maxLogBodyRunes = 512

type observabilityEnvelope struct {
	Signal    string `json:"signal"`
	Tool      string `json:"tool"`
	TenantID  string `json:"tenant_id,omitempty"`
	Count     int    `json:"count"`
	Total     int64  `json:"total,omitempty"`
	Truncated bool   `json:"truncated"`
	Items     any    `json:"items"`
}

type metricPointView struct {
	Metric string            `json:"metric,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	Time   string            `json:"time_unix_milli,omitempty"`
}

type metricTimeValueView struct {
	Time  string  `json:"time_unix_milli"`
	Value float64 `json:"value"`
}

type metricSeriesView struct {
	Metric string                `json:"metric,omitempty"`
	Labels map[string]string     `json:"labels,omitempty"`
	Values []metricTimeValueView `json:"values"`
}

type traceSummaryView struct {
	TraceID         string `json:"trace_id"`
	RootServiceName string `json:"root_service,omitempty"`
	RootSpanName    string `json:"root_span,omitempty"`
	StartTime       string `json:"start_time_unix_nano,omitempty"`
	DurationMs      int64  `json:"duration_ms"`
	SpanCount       int64  `json:"span_count"`
}

type traceSpanView struct {
	SpanID      string `json:"span_id"`
	Name        string `json:"name"`
	ServiceName string `json:"service,omitempty"`
	DurationNs  string `json:"duration_nano,omitempty"`
	Status      string `json:"status,omitempty"`
}

type traceDetailView struct {
	TraceID         string          `json:"trace_id"`
	RootServiceName string          `json:"root_service,omitempty"`
	RootSpanName    string          `json:"root_span,omitempty"`
	SpanCount       int             `json:"span_count"`
	ServiceCount    int             `json:"service_count"`
	Spans           []traceSpanView `json:"spans"`
}

type serviceView struct {
	Name      string `json:"name"`
	SpanCount int64  `json:"span_count,omitempty"`
}

type logRecordView struct {
	ID        string `json:"id"`
	Time      string `json:"time_unix_nano,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Service   string `json:"service,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	Body      string `json:"body"`
	Truncated bool   `json:"body_truncated,omitempty"`
}

func observabilityResult(
	signal, tool, tenantID string,
	items any,
	count int,
	total int64,
	truncated bool,
) (*mcp.CallToolResult, error) {
	payload := observabilityEnvelope{
		Signal:    signal,
		Tool:      tool,
		TenantID:  tenantID,
		Count:     count,
		Total:     total,
		Truncated: truncated,
		Items:     items,
	}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("格式化查询结果失败: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(buf)), nil
}

func truncateStrings(values []string, limit int) ([]string, bool) {
	if limit <= 0 || len(values) <= limit {
		return values, false
	}
	return values[:limit], true
}

func truncateLogBody(body string) (string, bool) {
	runes := []rune(body)
	if len(runes) <= maxLogBodyRunes {
		return body, false
	}
	return string(runes[:maxLogBodyRunes]) + "…", true
}

func metricPointViews(points []observabilitystorageext.MetricDataPoint, limit int) ([]metricPointView, bool) {
	truncated := limit > 0 && len(points) > limit
	if truncated {
		points = points[:limit]
	}
	out := make([]metricPointView, 0, len(points))
	for _, point := range points {
		out = append(out, metricPointView{
			Metric: point.Metric,
			Labels: point.Labels,
			Value:  point.Value,
			Time:   point.TimeUnixMilli,
		})
	}
	return out, truncated
}

func metricSeriesViews(series []observabilitystorageext.MetricSeries, seriesLimit, pointLimit int) ([]metricSeriesView, bool) {
	truncated := seriesLimit > 0 && len(series) > seriesLimit
	if truncated {
		series = series[:seriesLimit]
	}
	out := make([]metricSeriesView, 0, len(series))
	for _, item := range series {
		values := item.Values
		if len(values) == 0 {
			// ES aggregations can emit a placeholder series with no data
			// points (e.g. when a tenant filter excludes every document).
			// Do not present it to the caller as a result.
			continue
		}
		itemTruncated := pointLimit > 0 && len(values) > pointLimit
		if itemTruncated {
			values = values[:pointLimit]
			truncated = true
		}
		points := make([]metricTimeValueView, 0, len(values))
		for _, value := range values {
			points = append(points, metricTimeValueView{Time: value.TimeUnixMilli, Value: value.Value})
		}
		out = append(out, metricSeriesView{Metric: item.Metric, Labels: item.Labels, Values: points})
	}
	return out, truncated
}

func traceSummaryViews(summaries []observabilitystorageext.TraceSummary, limit int) ([]traceSummaryView, bool) {
	truncated := limit > 0 && len(summaries) > limit
	if truncated {
		summaries = summaries[:limit]
	}
	out := make([]traceSummaryView, 0, len(summaries))
	for _, item := range summaries {
		out = append(out, traceSummaryView{
			TraceID:         item.TraceID,
			RootServiceName: item.RootServiceName,
			RootSpanName:    item.RootSpanName,
			StartTime:       item.StartTimeUnixNano,
			DurationMs:      item.DurationMs,
			SpanCount:       item.SpanCount,
		})
	}
	return out, truncated
}

func logRecordViews(logs []observabilitystorageext.LogRecord, limit int) ([]logRecordView, bool) {
	truncated := limit > 0 && len(logs) > limit
	if truncated {
		logs = logs[:limit]
	}
	out := make([]logRecordView, 0, len(logs))
	for _, item := range logs {
		body, bodyTruncated := truncateLogBody(item.Body)
		out = append(out, logRecordView{
			ID:        item.ID,
			Time:      item.TimeUnixNano,
			Severity:  item.SeverityText,
			Service:   item.ServiceName,
			TraceID:   item.TraceID,
			Body:      body,
			Truncated: bodyTruncated,
		})
	}
	return out, truncated || anyLogBodyTruncated(out)
}

func anyLogBodyTruncated(logs []logRecordView) bool {
	for _, item := range logs {
		if item.Truncated {
			return true
		}
	}
	return false
}
