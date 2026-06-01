/**
 * Log Query API 类型定义
 *
 * 对应后端 observabilitystorageext Log 数据模型。
 */

// ============================================================================
// Log 数据模型
// ============================================================================

/** 单条日志记录 */
export interface LogRecord {
  id: string;
  timestamp: string;           // ISO 8601
  observed_time?: string;      // ISO 8601
  trace_id?: string;
  span_id?: string;
  severity: string;            // e.g. "ERROR", "WARN", "INFO", "DEBUG"
  severity_number: number;
  body: string;
  service_name: string;
  app_id?: string;
  attributes?: Record<string, unknown>;
  resource?: Record<string, unknown>;
}

/** 日志搜索结果 */
export interface LogSearchResult {
  logs: LogRecord[];
  total: number;
}

/** 日志上下文（前后行） */
export interface LogContext {
  before: LogRecord[];
  target: LogRecord;
  after: LogRecord[];
}

/** 日志可用字段 */
export interface LogField {
  name: string;
  type: string;   // "keyword", "text", "number"
  count: number;
}

/** 日志统计结果 */
export interface LogStats {
  total_count: number;
  severity_counts?: Record<string, number>;
  service_counts?: Record<string, number>;
  time_histogram?: TimeBucket[];
}

/** 时间桶（用于直方图） */
export interface TimeBucket {
  time: string;    // ISO 8601
  count: number;
}

// ============================================================================
// 查询参数
// ============================================================================

/** 日志搜索参数 */
export interface LogSearchParams {
  query?: string;
  service?: string;
  severity?: string;      // 逗号分隔: "ERROR,WARN"
  traceId?: string;
  spanId?: string;
  attributes?: string;    // key:value,key:value
  start?: number;         // Unix ms
  end?: number;           // Unix ms
  limit?: number;
  offset?: number;
}

/** 日志统计参数 */
export interface LogStatsParams {
  service?: string;
  start?: number;
  end?: number;
  groupBy?: string;
}

// ============================================================================
// 前端展示用派生类型
// ============================================================================

/** 严重级别颜色映射 */
export const SEVERITY_COLORS: Record<string, string> = {
  FATAL: '#dc2626',
  ERROR: '#ef4444',
  WARN: '#f59e0b',
  INFO: '#3b82f6',
  DEBUG: '#6b7280',
  TRACE: '#9ca3af',
};

/** 严重级别排序权重（越高越严重） */
export const SEVERITY_WEIGHTS: Record<string, number> = {
  FATAL: 6,
  ERROR: 5,
  WARN: 4,
  INFO: 3,
  DEBUG: 2,
  TRACE: 1,
};
