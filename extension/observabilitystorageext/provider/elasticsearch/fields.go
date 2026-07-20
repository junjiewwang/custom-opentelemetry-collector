// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

// ═══════════════════════════════════════════════════
// ES Document Field Name Constants
// ═══════════════════════════════════════════════════
//
// These constants define the canonical field names used in ES index templates
// and queries. They MUST match the JSON tags of the corresponding storedmodel
// types (StoredSpan, StoredLogRecord, StoredMetricDataPoint).
//
// When changing a field name:
//   1. Update the storedmodel type JSON tag (source of truth for writers)
//   2. Update the corresponding constant here (source of truth for readers)
//   3. Update admin.go index template mappings
//   4. Add a compat*() function in the reader to handle old index data

// ═══════════════════════ Trace Fields (StoredSpan) ═══════════════════════

const (
	FieldTraceID           = "traceId"
	FieldSpanID            = "spanId"
	FieldParentSpanID      = "parentSpanId"
	FieldName              = "name"
	FieldKind              = "kind"
	FieldStartTimeUnixNano = "startTimeUnixNano"
	FieldEndTimeUnixNano   = "endTimeUnixNano"
	FieldDurationNano      = "durationNano"
	FieldTraceState        = "traceState"
	FieldStatus            = "status"
	FieldScope             = "scope"
	FieldEvents            = "events"
	FieldLinks             = "links"
)

// ═══════════════════════ Log Fields (StoredLogRecord) ═══════════════════════

const (
	FieldLogTimeUnixNano         = "timeUnixNano"
	FieldLogObservedTimeUnixNano = "observedTimeUnixNano"
	FieldLogSeverityNumber       = "severityNumber"
	FieldLogSeverityText         = "severityText"
	FieldLogBody                 = "body"
)

// ═══════════════════════ Metric Fields (StoredMetricDataPoint) ═══════════════

const (
	// FieldMetricTimeUnixMilli is the epoch millisecond timestamp field.
	// Stored as ES date type with epoch_millis format for native date_histogram support.
	FieldMetricTimeUnixMilli     = "timeUnixMilli"
	FieldMetricType              = "type"
	FieldMetricValue             = "value"
	FieldMetricLabels            = "labels"
	FieldMetricBucketCounts      = "bucket_counts"
	FieldMetricExplicitBounds    = "explicit_bounds"
)

// ═══════════════════════ Shared Fields ═══════════════════════

const (
	FieldAttributes = "attributes"
	FieldResource   = "resource"
	FieldServiceName  = "serviceName"
	FieldAppID       = "appId"
)

// Old field names used only in compat*() backward-compatibility functions
// for reading data from indices created before the storage format unification.
const (
	fieldLegacyOperationName = "operation_name"
	fieldLegacySpanKind      = "span_kind"
	fieldLegacyStatusCode    = "status_code"
	fieldLegacyStatusMessage = "status_message"
	fieldLegacySeverity      = "severity" // log: old severityText field name, min_read in logs
)
