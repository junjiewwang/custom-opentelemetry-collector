// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

// StoredLogRecord is the unified storage type for logs, used by ALL providers
// for both write and read paths. Field names align with OTLP JSON conventions.
type StoredLogRecord struct {
	TimeUnixNano         int64          `json:"timeUnixNano"`
	ObservedTimeUnixNano int64          `json:"observedTimeUnixNano"`
	SeverityNumber       int32          `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 string         `json:"body,omitempty"`
	TraceID              string         `json:"traceId,omitempty"`
	SpanID               string         `json:"spanId,omitempty"`
	Attributes           map[string]any `json:"attributes,omitempty"`
	Resource             map[string]any `json:"resource,omitempty"`
	ServiceName          string         `json:"serviceName"`
	AppID                string         `json:"appId,omitempty"`
}

// ConvertOTLPLog converts an OTLP plog.LogRecord to StoredLogRecord.
func ConvertOTLPLog(lr plog.LogRecord, resource pcommon.Resource) StoredLogRecord {
	resourceAttrs := resource.Attributes()
	serviceName := getAttrStr(resourceAttrs, "service.name", "unknown")
	appID := getAppIDAttr(resourceAttrs)

	rec := StoredLogRecord{
		TimeUnixNano:         int64(lr.Timestamp()),
		ObservedTimeUnixNano: int64(lr.ObservedTimestamp()),
		SeverityNumber:       int32(lr.SeverityNumber()),
		SeverityText:         lr.SeverityText(),
		ServiceName:          serviceName,
		AppID:                appID,
		Attributes:           pcommonMapToFlat(lr.Attributes()),
		Resource:             pcommonMapToFlat(resourceAttrs),
	}

	// Body — skip if empty
	if lr.Body().Type() != pcommon.ValueTypeEmpty {
		rec.Body = lr.Body().AsString()
	}

	// Trace/Span IDs — skip if zero
	traceID := lr.TraceID()
	if !traceID.IsEmpty() {
		rec.TraceID = traceID.String()
	}
	spanID := lr.SpanID()
	if !spanID.IsEmpty() {
		rec.SpanID = spanID.String()
	}

	return rec
}
