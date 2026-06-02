/**
 * OTel Standard Trace Types
 *
 * Aligned with OpenTelemetry OTLP JSON Protobuf Encoding.
 * Reference: https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding
 */

// ============================================================================
// OTel Common Types
// ============================================================================

/** OTel KeyValue attribute (typed) */
export interface KeyValue {
  key: string;
  value: AnyValue;
}

/** OTel AnyValue — typed attribute value */
export interface AnyValue {
  stringValue?: string;
  intValue?: string;      // int64 as string for precision
  doubleValue?: number;
  boolValue?: boolean;
  arrayValue?: { values: AnyValue[] };
  kvlistValue?: { values: KeyValue[] };
  bytesValue?: string;    // base64 encoded
}

// ============================================================================
// Span Enums
// ============================================================================

/** OTel SpanKind enum values */
export type SpanKind =
  | 'SPAN_KIND_UNSPECIFIED'
  | 'SPAN_KIND_INTERNAL'
  | 'SPAN_KIND_SERVER'
  | 'SPAN_KIND_CLIENT'
  | 'SPAN_KIND_PRODUCER'
  | 'SPAN_KIND_CONSUMER';

/** OTel StatusCode enum values */
export type StatusCode =
  | 'STATUS_CODE_UNSET'
  | 'STATUS_CODE_OK'
  | 'STATUS_CODE_ERROR';

/** OTel Span Status */
export interface SpanStatus {
  code: StatusCode;
  message?: string;
}

// ============================================================================
// Trace Data Model — aligned with OTLP proto
// ============================================================================

/** Trace (with derived fields for UI convenience) */
export interface OTelTrace {
  traceId: string;
  spans: OTelSpan[];
  /** Nanoseconds as string */
  durationNano: string;
  spanCount: number;
  serviceCount: number;
  rootServiceName?: string;
  rootSpanName?: string;
}

/** Span — aligned with opentelemetry.proto.trace.v1.Span */
export interface OTelSpan {
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  traceState?: string;
  /** Operation name (OTel uses "name") */
  name: string;
  kind: SpanKind;
  /** Nanosecond Unix timestamp as string */
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  attributes?: KeyValue[];
  events?: SpanEvent[];
  links?: SpanLink[];
  status: SpanStatus;
  /** Derived: extracted from resource["service.name"] */
  serviceName: string;
  /** Derived: endTime - startTime in nanoseconds */
  durationNano: string;
  /** Resource attributes */
  resource?: KeyValue[];
}

/** Span Event — aligned with opentelemetry.proto.trace.v1.Span.Event */
export interface SpanEvent {
  timeUnixNano: string;
  name: string;
  attributes?: KeyValue[];
}

/** Span Link — aligned with opentelemetry.proto.trace.v1.Span.Link */
export interface SpanLink {
  traceId: string;
  spanId: string;
  traceState?: string;
  attributes?: KeyValue[];
}

// ============================================================================
// Service & Operation
// ============================================================================

/** Service info */
export interface Service {
  name: string;
  spanCount?: number;
}

/** Operation info */
export interface Operation {
  name: string;
  spanKind?: SpanKind;
}

/** Service dependency link */
export interface DependencyLink {
  parent: string;
  child: string;
  callCount: number;
}

// ============================================================================
// Search Types
// ============================================================================

/** Trace search result from V2 API */
export interface TraceSearchResult {
  traces: OTelTrace[];
  total: number;
}

/** Trace search parameters */
export interface TraceSearchParams {
  service?: string;
  operation?: string;
  tags?: string;
  limit?: number;
  start?: number;       // Unix ms
  end?: number;         // Unix ms
  minDuration?: string; // e.g. "1.2s", "100ms"
  maxDuration?: string;
  lookback?: string;    // e.g. "1h", "2d"
}

// ============================================================================
// Frontend Display Derived Types
// ============================================================================

/** Trace list display item (derived from OTelTrace) */
export interface TraceListItem {
  traceId: string;
  rootServiceName: string;
  rootSpanName: string;
  /** Start time in milliseconds (for display) */
  startTimeMs: number;
  /** Duration in microseconds (for display) */
  durationUs: number;
  spanCount: number;
  serviceCount: number;
  hasError: boolean;
  services: { name: string; count: number }[];
}

/** Span tree node (with hierarchy info for timeline rendering) */
export interface SpanTreeNode {
  span: OTelSpan;
  children: SpanTreeNode[];
  depth: number;
  /** Relative offset from trace start (nanoseconds) */
  relativeStartNano: number;
  /** Percentage of total trace duration */
  percentOfTrace: number;
}
