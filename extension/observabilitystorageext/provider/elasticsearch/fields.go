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
	FieldMetricUnit              = "unit" // OTel metric Unit() (By/1/ms)
	FieldMetricLabels            = "labels"
	FieldMetricBucketCounts      = "bucket_counts"
	FieldMetricExplicitBounds    = "explicit_bounds"
	// FieldMetricAggregationTemporality is the metric's aggregation temporality
	// ("cumulative"/"delta"), written by storedmodel and read back to let the
	// query layer distinguish delta counters/histograms from cumulative ones.
	FieldMetricAggregationTemporality = "aggregation_temporality"
)

// ═══════════════════════ Metric Metadata Fields (MetaDoc) ═══════════════════════
//
// These constants define the field names of the singleton metadata index
// ({prefix}-meta), which stores per-metric type/unit/labelKeys to let metadata
// listing (ListMetricNames/Types/LabelNames) query a small table instead of a
// terms aggregation over the full data index (the ES 429 fielddata trigger).

const (
	FieldMetaLastSeenAt   = "lastSeenAt"
	FieldMetaLabelKeys    = "labelKeys"
	FieldMetaServiceNames = "serviceNames"
	FieldMetaDocCount     = "docCount"
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
