/**
 * Jaeger Query API 响应类型定义
 *
 * 基于 Jaeger HTTP JSON API 格式。
 * 参考: https://www.jaegertracing.io/docs/apis/#http-json
 */

// ============================================================================
// Jaeger 通用
// ============================================================================

/** Jaeger API 标准响应包装 */
export interface JaegerResponse<T> {
  data: T;
  total: number;
  limit: number;
  offset: number;
  errors: JaegerError[] | null;
}

export interface JaegerError {
  code: number;
  msg: string;
}

// ============================================================================
// Trace 数据模型
// ============================================================================

/** Jaeger Trace */
export interface JaegerTrace {
  traceID: string;
  spans: JaegerSpan[];
  processes: Record<string, JaegerProcess>;
  warnings: string[] | null;
}

/** Jaeger Span */
export interface JaegerSpan {
  traceID: string;
  spanID: string;
  operationName: string;
  references: JaegerReference[];
  startTime: number; // 微秒
  duration: number;  // 微秒
  tags: JaegerKeyValue[];
  logs: JaegerLog[];
  processID: string;
  warnings: string[] | null;
}

/** Jaeger Reference（父子关系） */
export interface JaegerReference {
  refType: 'CHILD_OF' | 'FOLLOWS_FROM';
  traceID: string;
  spanID: string;
}

/** Jaeger Process（服务信息） */
export interface JaegerProcess {
  serviceName: string;
  tags: JaegerKeyValue[];
}

/** Jaeger 键值对 */
export interface JaegerKeyValue {
  key: string;
  type: string;
  value: string | number | boolean;
}

/** Jaeger Log */
export interface JaegerLog {
  timestamp: number;
  fields: JaegerKeyValue[];
}

// ============================================================================
// Operation 数据模型
// ============================================================================

/** Jaeger Operation */
export interface JaegerOperation {
  name: string;
  spanKind: string;
}

// ============================================================================
// 搜索参数
// ============================================================================

/** Trace 搜索参数 */
export interface TraceSearchParams {
  service: string;
  operation?: string;
  tags?: string;        // JSON 格式: {"key":"value"}
  limit?: number;
  start?: number;       // 微秒时间戳
  end?: number;         // 微秒时间戳
  minDuration?: string; // 例如 "1.2s", "100ms", "500us"
  maxDuration?: string;
  lookback?: string;    // 例如 "1h", "2d"
}

// ============================================================================
// 前端展示用派生类型
// ============================================================================

/** Trace 列表展示项（从 JaegerTrace 派生） */
export interface TraceListItem {
  traceID: string;
  rootServiceName: string;
  rootOperationName: string;
  startTime: number;   // 微秒
  duration: number;    // 微秒
  spanCount: number;
  serviceCount: number;
  hasError: boolean;
  services: { name: string; count: number }[];
}

/** Span 树节点（带层级信息，用于时间轴渲染） */
export interface SpanTreeNode {
  span: JaegerSpan;
  process: JaegerProcess;
  children: SpanTreeNode[];
  depth: number;
  /** 相对于 Trace 起始时间的偏移量（微秒） */
  relativeStartTime: number;
  /** 在总 Trace 时间中的占比 */
  percentOfTrace: number;
}

// ============================================================================
// Service Dependencies（用于 Service Map）
// ============================================================================

/** Jaeger 服务依赖关系 */
export interface JaegerDependencyLink {
  parent: string;
  child: string;
  callCount: number;
}
