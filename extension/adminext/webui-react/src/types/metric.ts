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
  /** Nanosecond Unix timestamp as string */
  timeUnixNano: string;
}

/** Metric time series */
export interface MetricSeries {
  metric?: string;
  labels: Record<string, string>;
  values: MetricTimeValue[];
}

/** Single time-value pair in a metric series */
export interface MetricTimeValue {
  /** Nanosecond Unix timestamp as string */
  timeUnixNano: string;
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

/** Metric range query parameters */
export interface MetricRangeQueryParams {
  metric: string;
  service?: string;
  labels?: string;    // key:value,key:value
  start: number;      // Unix ms
  end: number;        // Unix ms
  step: string;       // e.g. "15s", "1m", "5m"
}

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
