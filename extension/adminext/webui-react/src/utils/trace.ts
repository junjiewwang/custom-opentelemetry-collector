/**
 * Trace Data Utilities — OTel Standard
 *
 * Converts OTel standard trace data into frontend display structures.
 */

import type {
  OTelTrace,
  OTelSpan,
  TraceListItem,
  SpanTreeNode,
  KeyValue,
  AnyValue,
} from '@/types/trace';

// ============================================================================
// Nanosecond Helpers
// ============================================================================

/** Convert nanosecond string to milliseconds number */
export function nanoToMs(nanoStr: string): number {
  if (!nanoStr) return 0;
  const nano = Number(nanoStr);
  return nano / 1_000_000;
}

/** Convert nanosecond string to microseconds number */
export function nanoToUs(nanoStr: string): number {
  if (!nanoStr) return 0;
  const nano = Number(nanoStr);
  return nano / 1_000;
}

// ============================================================================
// KeyValue Helpers
// ============================================================================

/** Extract the display value from an AnyValue */
export function anyValueToDisplay(v: AnyValue): string | number | boolean {
  if (v.stringValue !== undefined) return v.stringValue;
  if (v.intValue !== undefined) return v.intValue;
  if (v.doubleValue !== undefined) return v.doubleValue;
  if (v.boolValue !== undefined) return v.boolValue;
  if (v.bytesValue !== undefined) return v.bytesValue;
  if (v.arrayValue) return JSON.stringify(v.arrayValue.values.map(anyValueToDisplay));
  if (v.kvlistValue) return JSON.stringify(
    Object.fromEntries(v.kvlistValue.values.map(kv => [kv.key, anyValueToDisplay(kv.value)])),
  );
  return '';
}

/** Convert KeyValue[] to a flat Record for easy display */
export function keyValuesToRecord(kvs?: KeyValue[]): Record<string, string | number | boolean> {
  if (!kvs || kvs.length === 0) return {};
  const result: Record<string, string | number | boolean> = {};
  for (const kv of kvs) {
    result[kv.key] = anyValueToDisplay(kv.value);
  }
  return result;
}

/** Find a specific key's value in a KeyValue array */
export function findKeyValue(kvs: KeyValue[] | undefined, key: string): string | number | boolean | undefined {
  if (!kvs) return undefined;
  const kv = kvs.find(k => k.key === key);
  if (!kv) return undefined;
  return anyValueToDisplay(kv.value);
}

// ============================================================================
// Trace → List Item Conversion
// ============================================================================

/**
 * Convert an OTelTrace to a list display item.
 */
export function traceToListItem(trace: OTelTrace): TraceListItem {
  const { spans } = trace;

  // Find root span (no parentSpanId)
  const rootSpan = spans.find(s => !s.parentSpanId);

  // Determine start time from the earliest span
  let minStartNano = Infinity;
  for (const span of spans) {
    const startNano = Number(span.startTimeUnixNano);
    if (startNano < minStartNano) minStartNano = startNano;
  }

  // Check for errors
  let hasError = false;
  const serviceMap = new Map<string, number>();

  for (const span of spans) {
    // Count services
    if (span.serviceName) {
      serviceMap.set(span.serviceName, (serviceMap.get(span.serviceName) ?? 0) + 1);
    }
    // Check error status
    if (!hasError && span.status?.code === 'STATUS_CODE_ERROR') {
      hasError = true;
    }
    // Also check error attribute
    if (!hasError && span.attributes) {
      const errorAttr = span.attributes.find(a => a.key === 'error');
      if (errorAttr?.value?.boolValue === true) hasError = true;
    }
  }

  const services = Array.from(serviceMap.entries())
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count);

  return {
    traceId: trace.traceId,
    rootServiceName: trace.rootServiceName ?? rootSpan?.serviceName ?? 'unknown',
    rootSpanName: trace.rootSpanName ?? rootSpan?.name ?? 'unknown',
    startTimeMs: nanoToMs(minStartNano === Infinity ? '0' : String(minStartNano)),
    durationUs: nanoToUs(trace.durationNano),
    spanCount: trace.spanCount || spans.length,
    serviceCount: trace.serviceCount || serviceMap.size,
    hasError,
    services,
  };
}

// ============================================================================
// Span Tree Building
// ============================================================================

/**
 * Build a span tree from an OTelTrace for timeline rendering.
 */
export function buildSpanTree(trace: OTelTrace): SpanTreeNode[] {
  const { spans } = trace;
  if (spans.length === 0) return [];

  // Calculate trace boundaries
  let traceStartNano = Infinity;
  let traceEndNano = 0;
  for (const span of spans) {
    const start = Number(span.startTimeUnixNano);
    const end = Number(span.endTimeUnixNano);
    if (start < traceStartNano) traceStartNano = start;
    if (end > traceEndNano) traceEndNano = end;
  }
  const traceDurationNano = traceEndNano - traceStartNano;

  // Build spanId → span map
  const spanMap = new Map<string, OTelSpan>();
  for (const span of spans) {
    spanMap.set(span.spanId, span);
  }

  // Build parentId → children map
  const childrenMap = new Map<string, string[]>();
  const rootSpanIds: string[] = [];

  for (const span of spans) {
    if (span.parentSpanId && spanMap.has(span.parentSpanId)) {
      const children = childrenMap.get(span.parentSpanId) ?? [];
      children.push(span.spanId);
      childrenMap.set(span.parentSpanId, children);
    } else {
      rootSpanIds.push(span.spanId);
    }
  }

  // Recursively build tree nodes
  function buildNode(spanId: string, depth: number): SpanTreeNode | null {
    const span = spanMap.get(spanId);
    if (!span) return null;

    const childIds = childrenMap.get(spanId) ?? [];
    const children = childIds
      .map(id => buildNode(id, depth + 1))
      .filter((n): n is SpanTreeNode => n !== null)
      .sort((a, b) => Number(a.span.startTimeUnixNano) - Number(b.span.startTimeUnixNano));

    const spanStartNano = Number(span.startTimeUnixNano);
    const spanDurationNano = Number(span.durationNano) || (Number(span.endTimeUnixNano) - spanStartNano);

    return {
      span,
      children,
      depth,
      relativeStartNano: spanStartNano - traceStartNano,
      percentOfTrace: traceDurationNano > 0 ? (spanDurationNano / traceDurationNano) * 100 : 0,
    };
  }

  return rootSpanIds
    .map(id => buildNode(id, 0))
    .filter((n): n is SpanTreeNode => n !== null)
    .sort((a, b) => Number(a.span.startTimeUnixNano) - Number(b.span.startTimeUnixNano));
}

// ============================================================================
// Formatting
// ============================================================================

/**
 * Format nanosecond duration to human-readable string.
 */
export function formatDuration(nanoStr: string | number): string {
  const nanos = typeof nanoStr === 'string' ? Number(nanoStr) : nanoStr;
  if (nanos <= 0) return '0μs';

  const us = nanos / 1_000;
  if (us < 1000) {
    return `${us.toFixed(us < 10 ? 1 : 0)}μs`;
  }
  const ms = us / 1000;
  if (ms < 1000) {
    return `${ms.toFixed(2)}ms`;
  }
  const s = ms / 1000;
  return `${s.toFixed(2)}s`;
}

/**
 * Format nanosecond timestamp to readable date-time string.
 */
export function formatTimestamp(nanoStr: string | number): string {
  const nanos = typeof nanoStr === 'string' ? Number(nanoStr) : nanoStr;
  const ms = nanos / 1_000_000;
  const date = new Date(ms);
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
 * Format SpanKind for display.
 */
export function formatSpanKind(kind: string): string {
  const map: Record<string, string> = {
    'SPAN_KIND_UNSPECIFIED': 'unspecified',
    'SPAN_KIND_INTERNAL': 'internal',
    'SPAN_KIND_SERVER': 'server',
    'SPAN_KIND_CLIENT': 'client',
    'SPAN_KIND_PRODUCER': 'producer',
    'SPAN_KIND_CONSUMER': 'consumer',
  };
  return map[kind] ?? kind;
}

// ============================================================================
// Service Colors
// ============================================================================

const SERVICE_COLORS = [
  '#17B8BE', '#F8DCA1', '#B7885E', '#FFCB99', '#F89570',
  '#829AE3', '#E79FD5', '#1E96BE', '#89DAC1', '#B3AD9E',
  '#12939A', '#DDB27C', '#88572C', '#FF9833', '#EF5D28',
  '#162A65', '#DA70BF', '#125C77', '#4DC19C', '#776E57',
];

/**
 * Generate a stable color for a service name.
 */
export function getServiceColor(serviceName: string): string {
  let hash = 0;
  for (let i = 0; i < serviceName.length; i++) {
    hash = ((hash << 5) - hash + serviceName.charCodeAt(i)) | 0;
  }
  return SERVICE_COLORS[Math.abs(hash) % SERVICE_COLORS.length]!;
}
