/**
 * OTel Standard Metric Types
 *
 * Aligned with the V2 backend metric API response format.
 * The backend stores OTel metrics and returns structured results.
 */

// ============================================================================
// Metric API Response Types
// ============================================================================

/** Metric instant query result */
export interface MetricResult {
  data: MetricDataPoint[];
}

/** Metric range query result */
export interface MetricRangeResult {
  data: MetricSeries[];
}

/** Single metric value at a point in time */
export interface MetricDataPoint {
  metric?: string;
  labels: Record<string, string>;
  value: number;
  /** Millisecond Unix timestamp as string */
  timeUnixMilli: string;
}

/** Metric time series */
export interface MetricSeries {
  metric?: string;
  labels: Record<string, string> | null;
  values: MetricTimeValue[];
}

/** Single time-value pair in a metric series */
export interface MetricTimeValue {
  /** Millisecond Unix timestamp as string */
  timeUnixMilli: string;
  value: number;
}

// ============================================================================
// Query Parameters
// ============================================================================

/** Metric instant query parameters */
export interface MetricQueryParams {
  metric: string;
  service?: string;
  labels?: string;    // key:value,key:value
  time?: number;      // Unix ms
}

/** Metric range query parameters — aligned with InfluxQL Query Builder */
export interface MetricRangeQueryParams {
  metric: string;
  service?: string;
  labels?: string;        // key:value,key:value (exact match)
  labelMatch?: string;    // key:/regex/ (regex match)
  start: number;          // Unix ms
  end: number;            // Unix ms
  step: string;           // e.g. "15s", "1m", "5m"
  aggregation?: string;   // avg|sum|max|min|count|last|first|p50|p90|p95|p99, default "avg"
  groupBy?: string;       // comma-separated label keys, e.g. "service_name,method"
  fill?: string;          // null|none|0|previous|linear, default "null"
  seriesLimit?: number;   // max series count, default 100
}

/** Aggregation function options */
export const AGGREGATION_OPTIONS = [
  { value: 'avg', label: 'avg (mean)' },
  { value: 'sum', label: 'sum' },
  { value: 'max', label: 'max' },
  { value: 'min', label: 'min' },
  { value: 'count', label: 'count' },
  { value: 'last', label: 'last' },
  { value: 'first', label: 'first' },
  { value: 'p50', label: 'p50 (median)' },
  { value: 'p90', label: 'p90' },
  { value: 'p95', label: 'p95' },
  { value: 'p99', label: 'p99' },
] as const;

/** Fill strategy options */
export const FILL_OPTIONS = [
  { value: 'null', label: 'null (gaps)' },
  { value: 'none', label: 'none (skip)' },
  { value: '0', label: 'zero' },
  { value: 'previous', label: 'previous' },
  { value: 'linear', label: 'linear' },
] as const;

// ============================================================================
// Frontend Display Types
// ============================================================================

/** Time range quick preset option */
export interface TimeRangePreset {
  label: string;
  value: string;
  seconds: number;
}

/** Chart series data (derived from MetricSeries for rendering) */
export interface ChartSeries {
  name: string;
  labels: Record<string, string>;
  data: { time: number; value: number }[];
}

/** Metric panel definition (for RED dashboard etc.) */
export interface MetricPanel {
  id: string;
  title: string;
  description: string;
  /** Metric name to query */
  metric: string;
  /** Service filter */
  service?: string;
  /** Additional label filters */
  labels?: Record<string, string>;
  /** Chart type */
  chartType?: 'line' | 'area';
  /** Y axis unit */
  unit?: 'percent' | 'seconds' | 'bytes' | 'ops' | 'none';
}
