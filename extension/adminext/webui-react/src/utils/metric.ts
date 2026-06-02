/**
 * Metric 数据工具函数
 *
 * 从 OTel V2 API 响应数据中提取和转换前端展示用数据。
 */

import type {
  MetricRangeResult,
  ChartSeries,
  TimeRangePreset,
  MetricPanel,
} from '@/types/metric';

/**
 * 将 MetricRangeResult 转换为 ECharts 图表系列数据。
 */
export function seriesToChartSeries(result: MetricRangeResult): ChartSeries[] {
  if (!result.data || result.data.length === 0) return [];

  return result.data.map((series) => {
    const name = formatMetricLabels(series.labels);
    return {
      name,
      labels: series.labels,
      data: series.values.map((tv) => ({
        time: Number(tv.timeUnixNano) / 1_000_000, // 纳秒转毫秒
        value: tv.value,
      })),
    };
  });
}

/**
 * 格式化 Metric labels 为人类可读字符串。
 */
export function formatMetricLabels(labels: Record<string, string>): string {
  const filtered = Object.entries(labels).filter(([k]) => k !== '__name__' && k !== 'metric');
  if (filtered.length === 0) {
    return labels['__name__'] ?? labels['metric'] ?? 'unknown';
  }
  const metricName = labels['__name__'] ?? labels['metric'] ?? '';
  const labelStr = filtered.map(([k, v]) => `${k}="${v}"`).join(', ');
  return metricName ? `${metricName}{${labelStr}}` : `{${labelStr}}`;
}

/**
 * 根据时间范围计算合适的 step。
 */
export function calculateStep(rangeSeconds: number): string {
  if (rangeSeconds <= 3600) return '15s';        // <=1h: 15s step
  if (rangeSeconds <= 6 * 3600) return '60s';    // <=6h: 1m step
  if (rangeSeconds <= 24 * 3600) return '300s';  // <=24h: 5m step
  if (rangeSeconds <= 7 * 86400) return '1800s'; // <=7d: 30m step
  return '3600s';                                 // >7d: 1h step
}

/**
 * 格式化 Y 轴数值。
 */
export function formatYAxisValue(value: number, unit?: string): string {
  switch (unit) {
    case 'percent':
      return `${value.toFixed(1)}%`;
    case 'seconds':
      if (value < 0.001) return `${(value * 1_000_000).toFixed(0)}μs`;
      if (value < 1) return `${(value * 1000).toFixed(1)}ms`;
      return `${value.toFixed(2)}s`;
    case 'bytes':
      if (value < 1024) return `${value.toFixed(0)}B`;
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)}KB`;
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)}MB`;
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)}GB`;
    case 'ops':
      if (value < 1) return value.toFixed(3);
      if (value > 1000) return `${(value / 1000).toFixed(1)}k`;
      return value.toFixed(1);
    default:
      if (Number.isInteger(value)) return String(value);
      return value.toFixed(2);
  }
}

/**
 * 时间范围预设选项。
 */
export const TIME_RANGE_PRESETS: TimeRangePreset[] = [
  { label: '15m', value: '15m', seconds: 15 * 60 },
  { label: '30m', value: '30m', seconds: 30 * 60 },
  { label: '1h', value: '1h', seconds: 3600 },
  { label: '3h', value: '3h', seconds: 3 * 3600 },
  { label: '6h', value: '6h', seconds: 6 * 3600 },
  { label: '12h', value: '12h', seconds: 12 * 3600 },
  { label: '24h', value: '24h', seconds: 24 * 3600 },
  { label: '2d', value: '2d', seconds: 2 * 86400 },
  { label: '7d', value: '7d', seconds: 7 * 86400 },
];

/**
 * 预设的 RED 指标面板（Rate / Error / Duration）。
 *
 * 使用 OTel metric names + service filter。
 */
export const RED_PANELS: MetricPanel[] = [
  {
    id: 'request_rate',
    title: 'Request Rate',
    description: '每秒请求数 (QPS)',
    metric: 'http_server_request_duration_seconds_count',
    chartType: 'area',
    unit: 'ops',
  },
  {
    id: 'error_rate',
    title: 'Error Rate',
    description: '错误率百分比',
    metric: 'http_server_request_error_count',
    chartType: 'area',
    unit: 'percent',
  },
  {
    id: 'latency_p50',
    title: 'Latency P50',
    description: '50th percentile 延迟',
    metric: 'http_server_request_duration_seconds_p50',
    chartType: 'line',
    unit: 'seconds',
  },
  {
    id: 'latency_p95',
    title: 'Latency P95',
    description: '95th percentile 延迟',
    metric: 'http_server_request_duration_seconds_p95',
    chartType: 'line',
    unit: 'seconds',
  },
  {
    id: 'latency_p99',
    title: 'Latency P99',
    description: '99th percentile 延迟',
    metric: 'http_server_request_duration_seconds_p99',
    chartType: 'line',
    unit: 'seconds',
  },
  {
    id: 'request_by_status',
    title: 'Requests by Status Code',
    description: '按 HTTP 状态码分组的请求速率',
    metric: 'http_server_request_duration_seconds_count',
    labels: { http_response_status_code: '*' },
    chartType: 'area',
    unit: 'ops',
  },
];

/**
 * 图表颜色调色板。
 */
export const CHART_COLORS = [
  '#3b82f6', '#ef4444', '#22c55e', '#f59e0b', '#8b5cf6',
  '#ec4899', '#06b6d4', '#84cc16', '#f97316', '#6366f1',
  '#14b8a6', '#e11d48', '#0891b2', '#65a30d', '#7c3aed',
];
