/**
 * OTel Standard Log Types
 *
 * Aligned with OpenTelemetry OTLP Log data model (camelCase JSON encoding).
 * Reference: opentelemetry.proto.logs.v1.LogRecord
 */

import type { KeyValue } from './trace';

// ============================================================================
// Log Data Model — aligned with OTLP proto
// ============================================================================

/** Single log record — aligned with opentelemetry.proto.logs.v1.LogRecord */
export interface LogRecord {
  id: string;
  /** Nanosecond Unix timestamp as string */
  timeUnixNano: string;
  observedTimeUnixNano?: string;
  traceId?: string;
  spanId?: string;
  severityNumber: number;
  severityText: string;
  body: string;
  attributes?: KeyValue[];
  resource?: KeyValue[];
  /** Derived: extracted from resource["service.name"] */
  serviceName: string;
  appId?: string;
}

/** Log search result */
export interface LogSearchResult {
  logs: LogRecord[];
  total: number;
}

/** Log context (surrounding lines) */
export interface LogContext {
  before: LogRecord[];
  target: LogRecord;
  after: LogRecord[];
}

/** Available log field for filtering */
export interface LogField {
  name: string;
  type: string;   // "keyword", "text", "number"
  count: number;
}

/** Log statistics result */
export interface LogStats {
  totalCount: number;
  severityCounts?: Record<string, number>;
  serviceCounts?: Record<string, number>;
  timeHistogram?: TimeBucket[];
}

/** Time bucket for histogram */
export interface TimeBucket {
  /** Nanosecond Unix timestamp as string */
  timeUnixNano: string;
  count: number;
}

// ============================================================================
// Query Parameters
// ============================================================================

/** Log search parameters */
export interface LogSearchParams {
  query?: string;
  service?: string;
  severity?: string;      // comma-separated: "ERROR,WARN"
  traceId?: string;
  spanId?: string;
  attributes?: string;    // key:value,key:value
  start?: number;         // Unix ms
  end?: number;           // Unix ms
  limit?: number;
  offset?: number;
}

/** Log stats query parameters */
export interface LogStatsParams {
  service?: string;
  start?: number;
  end?: number;
  groupBy?: string;
}

// ============================================================================
// Frontend Display Constants
// ============================================================================

/** Severity level color mapping */
export const SEVERITY_COLORS: Record<string, string> = {
  FATAL: '#dc2626',
  ERROR: '#ef4444',
  WARN: '#f59e0b',
  INFO: '#3b82f6',
  DEBUG: '#6b7280',
  TRACE: '#9ca3af',
};

/** Severity level sort weight (higher = more severe) */
export const SEVERITY_WEIGHTS: Record<string, number> = {
  FATAL: 6,
  ERROR: 5,
  WARN: 4,
  INFO: 3,
  DEBUG: 2,
  TRACE: 1,
};
