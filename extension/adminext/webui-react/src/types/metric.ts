/**
 * Prometheus HTTP API 响应类型定义
 *
 * 参考: https://prometheus.io/docs/prometheus/latest/querying/api/
 */

// ============================================================================
// 通用响应包装
// ============================================================================

/** Prometheus API 标准响应 */
export interface PrometheusResponse<T> {
  status: 'success' | 'error';
  data: T;
  errorType?: string;
  error?: string;
  warnings?: string[];
}

// ============================================================================
// Query 结果数据模型
// ============================================================================

/** Query 结果包装 */
export interface PrometheusQueryResult {
  resultType: 'matrix' | 'vector' | 'scalar' | 'string';
  result: PrometheusMatrixResult[] | PrometheusVectorResult[] | PrometheusScalarResult;
}

/** Range Query 结果（矩阵类型） */
export interface PrometheusMatrixResult {
  metric: Record<string, string>;
  values: [number, string][]; // [timestamp, value][]
}

/** Instant Query 结果（向量类型） */
export interface PrometheusVectorResult {
  metric: Record<string, string>;
  value: [number, string]; // [timestamp, value]
}

/** 标量结果 */
export type PrometheusScalarResult = [number, string]; // [timestamp, value]

// ============================================================================
// Labels / Series / Metadata
// ============================================================================

/** Label 名称列表响应 */
export type PrometheusLabelsResponse = PrometheusResponse<string[]>;

/** Label 值列表响应 */
export type PrometheusLabelValuesResponse = PrometheusResponse<string[]>;

/** Series 响应 */
export type PrometheusSeriesResponse = PrometheusResponse<Record<string, string>[]>;

/** Metric metadata */
export interface PrometheusMetadataEntry {
  type: 'counter' | 'gauge' | 'histogram' | 'summary' | 'unknown';
  help: string;
  unit: string;
}

/** Metadata 响应 */
export type PrometheusMetadataResponse = PrometheusResponse<Record<string, PrometheusMetadataEntry[]>>;

// ============================================================================
// 前端查询参数
// ============================================================================

/** Metric 查询参数 */
export interface MetricQueryParams {
  query: string;
  start: number;   // Unix 时间戳（秒）
  end: number;     // Unix 时间戳（秒）
  step: string;    // 例如 "15s", "1m", "5m"
}

/** 时间范围快捷选项 */
export interface TimeRangePreset {
  label: string;
  value: string;
  seconds: number;
}

/** 预设面板定义 */
export interface MetricPanel {
  id: string;
  title: string;
  description: string;
  query: string;
  /** 查询中需要替换的变量 */
  variables?: { name: string; label: string; labelValue?: string }[];
  /** 图表类型 */
  chartType?: 'line' | 'area';
  /** Y 轴单位 */
  unit?: 'percent' | 'seconds' | 'bytes' | 'ops' | 'none';
}

// ============================================================================
// 前端展示用派生类型
// ============================================================================

/** 图表系列数据（从 PrometheusMatrixResult 派生） */
export interface ChartSeries {
  name: string;
  labels: Record<string, string>;
  data: { time: number; value: number }[];
}
