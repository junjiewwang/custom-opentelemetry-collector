/**
 * Trace 数据工具函数
 *
 * 从 Jaeger 原始数据提取前端展示需要的派生信息。
 */

import type {
  JaegerTrace,
  JaegerSpan,
  TraceListItem,
  SpanTreeNode,
} from '@/types/trace';

/**
 * 从 JaegerTrace 提取列表展示项。
 */
export function traceToListItem(trace: JaegerTrace): TraceListItem {
  const { spans, processes } = trace;

  // 找到 root span（无 parent reference 的 span）
  const rootSpan = findRootSpan(spans);

  // 计算 trace 的总时长
  let minStart = Infinity;
  let maxEnd = 0;
  for (const span of spans) {
    minStart = Math.min(minStart, span.startTime);
    maxEnd = Math.max(maxEnd, span.startTime + span.duration);
  }
  const totalDuration = maxEnd - minStart;

  // 统计每个 service 的 span 数量
  const serviceMap = new Map<string, number>();
  let hasError = false;

  for (const span of spans) {
    const proc = processes[span.processID];
    if (proc) {
      const svcName = proc.serviceName;
      serviceMap.set(svcName, (serviceMap.get(svcName) ?? 0) + 1);
    }
    // 检查是否有错误
    if (!hasError && span.tags.some(t => t.key === 'error' && t.value === true)) {
      hasError = true;
    }
    if (!hasError && span.tags.some(t => t.key === 'otel.status_code' && t.value === 'ERROR')) {
      hasError = true;
    }
  }

  const services = Array.from(serviceMap.entries())
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count);

  const rootProcess = rootSpan ? processes[rootSpan.processID] : undefined;

  return {
    traceID: trace.traceID,
    rootServiceName: rootProcess?.serviceName ?? 'unknown',
    rootOperationName: rootSpan?.operationName ?? 'unknown',
    startTime: minStart,
    duration: totalDuration,
    spanCount: spans.length,
    serviceCount: serviceMap.size,
    hasError,
    services,
  };
}

/**
 * 找到 root span（没有 CHILD_OF 引用的 span）。
 */
function findRootSpan(spans: JaegerSpan[]): JaegerSpan | undefined {
  return spans.find(span =>
    span.references.length === 0 ||
    span.references.every(ref => ref.refType !== 'CHILD_OF'),
  );
}

/**
 * 将 JaegerTrace 的 span 列表构建成树形结构。
 */
export function buildSpanTree(trace: JaegerTrace): SpanTreeNode[] {
  const { spans, processes } = trace;
  if (spans.length === 0) return [];

  // 计算 trace 的总时长和起始时间
  let traceStartTime = Infinity;
  let traceEndTime = 0;
  for (const span of spans) {
    traceStartTime = Math.min(traceStartTime, span.startTime);
    traceEndTime = Math.max(traceEndTime, span.startTime + span.duration);
  }
  const traceDuration = traceEndTime - traceStartTime;

  // 构建 spanID → span 的映射
  const spanMap = new Map<string, JaegerSpan>();
  for (const span of spans) {
    spanMap.set(span.spanID, span);
  }

  // 构建 parentID → children 的映射
  const childrenMap = new Map<string, string[]>();
  const rootSpanIDs: string[] = [];

  for (const span of spans) {
    const parentRef = span.references.find(r => r.refType === 'CHILD_OF');
    if (parentRef && spanMap.has(parentRef.spanID)) {
      const children = childrenMap.get(parentRef.spanID) ?? [];
      children.push(span.spanID);
      childrenMap.set(parentRef.spanID, children);
    } else {
      rootSpanIDs.push(span.spanID);
    }
  }

  // 递归构建树节点
  function buildNode(spanID: string, depth: number): SpanTreeNode | null {
    const span = spanMap.get(spanID);
    if (!span) return null;

    const process = processes[span.processID] ?? { serviceName: 'unknown', tags: [] };
    const childIDs = childrenMap.get(spanID) ?? [];

    // 子节点按开始时间排序
    const children = childIDs
      .map(id => buildNode(id, depth + 1))
      .filter((n): n is SpanTreeNode => n !== null)
      .sort((a, b) => a.span.startTime - b.span.startTime);

    return {
      span,
      process,
      children,
      depth,
      relativeStartTime: span.startTime - traceStartTime,
      percentOfTrace: traceDuration > 0 ? (span.duration / traceDuration) * 100 : 0,
    };
  }

  // 构建所有根节点
  return rootSpanIDs
    .map(id => buildNode(id, 0))
    .filter((n): n is SpanTreeNode => n !== null)
    .sort((a, b) => a.span.startTime - b.span.startTime);
}

/**
 * 格式化微秒时长为人类可读字符串。
 */
export function formatDuration(microseconds: number): string {
  if (microseconds < 1000) {
    return `${microseconds}μs`;
  }
  if (microseconds < 1_000_000) {
    return `${(microseconds / 1000).toFixed(2)}ms`;
  }
  return `${(microseconds / 1_000_000).toFixed(2)}s`;
}

/**
 * 格式化微秒时间戳为可读的日期时间字符串。
 */
export function formatTimestamp(microseconds: number): string {
  const date = new Date(microseconds / 1000); // 微秒转毫秒
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

/**
 * 为 Service 名称生成一个稳定的颜色。
 */
const SERVICE_COLORS = [
  '#17B8BE', '#F8DCA1', '#B7885E', '#FFCB99', '#F89570',
  '#829AE3', '#E79FD5', '#1E96BE', '#89DAC1', '#B3AD9E',
  '#12939A', '#DDB27C', '#88572C', '#FF9833', '#EF5D28',
  '#162A65', '#DA70BF', '#125C77', '#4DC19C', '#776E57',
];

export function getServiceColor(serviceName: string): string {
  let hash = 0;
  for (let i = 0; i < serviceName.length; i++) {
    hash = ((hash << 5) - hash + serviceName.charCodeAt(i)) | 0;
  }
  return SERVICE_COLORS[Math.abs(hash) % SERVICE_COLORS.length]!;
}
