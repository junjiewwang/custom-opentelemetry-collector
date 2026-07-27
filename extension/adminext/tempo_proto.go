// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/hex"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"

	v1common "go.opentelemetry.io/proto/otlp/common/v1"
	v1resource "go.opentelemetry.io/proto/otlp/resource/v1"
	v1trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

// This file holds the OTLP protobuf encoding for the Tempo-compatible V2
// trace endpoints. It is purely free functions (no *tempoHandlers state) and
// was extracted from tempo_handler.go to improve cohesion.

// ── OTLP Protobuf Encoding ─────────────────────────
// Converts internal Trace → OTLP protobuf binary for Grafana 12+ trace endpoints.

// convertTraceToProtobuf converts an internal Trace to OTLP protobuf binary bytes.
// Spans are grouped by service name → ResourceSpans, using raw OTLP proto types.
// Returns an error if the trace has no spans or the resulting protobuf is empty.
func convertTraceToProtobuf(trace *observabilitystorageext.Trace) ([]byte, error) {
	if trace == nil || len(trace.Spans) == 0 {
		return nil, fmt.Errorf("trace has no spans")
	}

	grouped := groupSpansByService(trace.Spans)

	resourceSpans := make([]*v1trace.ResourceSpans, 0, len(grouped))
	for _, g := range grouped {
		spans := convertSpansToProto(g.spans)
		if len(spans) == 0 {
			continue // skip groups where all spans failed conversion
		}
		rs := &v1trace.ResourceSpans{
			Resource: buildProtoResource(g.resourceAttrs),
			ScopeSpans: []*v1trace.ScopeSpans{
				{
					Scope: &v1common.InstrumentationScope{Name: "opentelemetry"},
					Spans: spans,
				},
			},
		}
		resourceSpans = append(resourceSpans, rs)
	}

	if len(resourceSpans) == 0 {
		return nil, fmt.Errorf("all spans failed protobuf conversion (check traceID/spanID hex encoding)")
	}

	td := &v1trace.TracesData{
		ResourceSpans: resourceSpans,
	}

	bytes, err := proto.Marshal(td)
	if err != nil {
		return nil, fmt.Errorf("proto.Marshal: %w", err)
	}
	if len(bytes) == 0 {
		return nil, fmt.Errorf("proto.Marshal produced empty output for %d resource spans", len(resourceSpans))
	}

	return bytes, nil
}

// wrapAsTraceByIDResponse wraps raw TracesData protobuf bytes into a Tempo
// TraceByIDResponse wire format envelope.
//
// Grafana 12+ Tempo plugin V2 endpoint (/api/v2/traces/{traceID}) expects:
//
//	message TraceByIDResponse {
//	    Trace trace = 1;               // field_number=1, wire_type=LEN (2)
//	    TraceByIDMetrics metrics = 2;
//	    PartialStatus status = 3;
//	    string message = 4;
//	}
//	message Trace {
//	    repeated opentelemetry.proto.trace.v1.ResourceSpans resourceSpans = 1;
//	}
//
// Since tempopb.Trace and OTLP TracesData share identical wire format
// (field 1 = repeated ResourceSpans with the same field numbers), we can
// directly use TracesData bytes as the Trace message body.
//
// Wire encoding: [tag: field=1, type=LEN] [varint: length] [TracesData bytes]
//
// This avoids importing the heavyweight github.com/grafana/tempo module while
// maintaining 100% wire format compatibility. The field number contract
// (TraceByIDResponse.trace = field 1) is part of Tempo's public proto API and
// will not change without a major version bump (which would break all existing
// Grafana clients).
//
// Reference: https://github.com/grafana/tempo/blob/main/pkg/tempopb/tempo.proto
func wrapAsTraceByIDResponse(tracesDataBytes []byte) []byte {
	// TraceByIDResponse.trace is field_number=1, wire_type=LEN(2)
	// In protobuf wire format: tag = (field_number << 3) | wire_type = (1 << 3) | 2 = 0x0A
	const fieldNumber = 1

	// Calculate total size: tag (1 byte typically) + varint length + payload
	tagSize := protowire.SizeTag(fieldNumber)
	lengthSize := protowire.SizeBytes(len(tracesDataBytes)) // includes tag + varint len + payload len

	buf := make([]byte, 0, tagSize+lengthSize)
	buf = protowire.AppendTag(buf, fieldNumber, protowire.BytesType)
	buf = protowire.AppendVarint(buf, uint64(len(tracesDataBytes)))
	buf = append(buf, tracesDataBytes...)

	return buf
}

// mergeTracesToProtobuf merges multiple traces into a single OTLP protobuf binary.
// Each trace's spans are grouped by service and appended to the ResourceSpans list.
// Used by V2 search to return full trace data in a single protobuf response.
func mergeTracesToProtobuf(traces []*observabilitystorageext.Trace) ([]byte, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("no traces to encode")
	}

	var allResourceSpans []*v1trace.ResourceSpans

	for _, trace := range traces {
		if trace == nil || len(trace.Spans) == 0 {
			continue
		}
		grouped := groupSpansByService(trace.Spans)
		for _, g := range grouped {
			spans := convertSpansToProto(g.spans)
			if len(spans) == 0 {
				continue
			}
			rs := &v1trace.ResourceSpans{
				Resource: buildProtoResource(g.resourceAttrs),
				ScopeSpans: []*v1trace.ScopeSpans{
					{
						Scope: &v1common.InstrumentationScope{Name: "opentelemetry"},
						Spans: spans,
					},
				},
			}
			allResourceSpans = append(allResourceSpans, rs)
		}
	}

	if len(allResourceSpans) == 0 {
		return nil, fmt.Errorf("all traces failed protobuf conversion (%d traces input)", len(traces))
	}

	td := &v1trace.TracesData{
		ResourceSpans: allResourceSpans,
	}

	bytes, err := proto.Marshal(td)
	if err != nil {
		return nil, fmt.Errorf("proto.Marshal: %w", err)
	}
	if len(bytes) == 0 {
		return nil, fmt.Errorf("proto.Marshal produced empty output for %d resource spans", len(allResourceSpans))
	}

	return bytes, nil
}

// spanGroup holds spans grouped by service name.
type spanGroup struct {
	serviceName   string
	resourceAttrs []observabilitystorageext.KeyValue
	spans         []observabilitystorageext.Span
}

// groupSpansByService groups spans by service name, preserving the resource
// attributes from the first span in each group.
func groupSpansByService(spans []observabilitystorageext.Span) []spanGroup {
	seen := make(map[string]int) // serviceName → index in groups
	var groups []spanGroup

	for _, span := range spans {
		svc := span.ServiceName
		if svc == "" {
			svc = "unknown"
		}
		if idx, ok := seen[svc]; ok {
			groups[idx].spans = append(groups[idx].spans, span)
		} else {
			seen[svc] = len(groups)
			groups = append(groups, spanGroup{
				serviceName:   svc,
				resourceAttrs: span.Resource,
				spans:         []observabilitystorageext.Span{span},
			})
		}
	}
	return groups
}

// buildProtoResource builds a proto Resource from KeyValue attributes.
func buildProtoResource(attrs []observabilitystorageext.KeyValue) *v1resource.Resource {
	kvs := publicKeyValuesToProto(attrs)
	return &v1resource.Resource{Attributes: kvs}
}

// convertSpansToProto converts a slice of internal Spans to proto Span slices.
func convertSpansToProto(spans []observabilitystorageext.Span) []*v1trace.Span {
	result := make([]*v1trace.Span, 0, len(spans))
	for _, s := range spans {
		ps := publicSpanToProtoSpan(s)
		if ps != nil {
			result = append(result, ps)
		}
	}
	return result
}

// publicSpanToProtoSpan converts a single public Span to a proto Span.
// Returns nil if traceID or spanID cannot be decoded.
func publicSpanToProtoSpan(s observabilitystorageext.Span) *v1trace.Span {
	traceID, err := hexTo16Bytes(s.TraceID)
	if err != nil {
		return nil
	}
	spanID, err := hexTo8Bytes(s.SpanID)
	if err != nil {
		return nil
	}

	ps := &v1trace.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		Name:              s.Name,
		Kind:              mapSpanKind(s.Kind),
		StartTimeUnixNano: parseUnixNano(s.StartTimeUnixNano),
		EndTimeUnixNano:   parseUnixNano(s.EndTimeUnixNano),
		Attributes:        publicKeyValuesToProto(s.Attributes),
		Events:            publicEventsToProtoEvents(s.Events),
		Links:             publicLinksToProtoLinks(s.Links),
	}

	// ParentSpanID is optional.
	if s.ParentSpanID != "" {
		parentID, err := hexTo8Bytes(s.ParentSpanID)
		if err == nil {
			ps.ParentSpanId = parentID
		}
	}

	// TraceState is optional.
	if s.TraceState != "" {
		ps.TraceState = s.TraceState
	}

	// Status is optional (only set when code is not "unset").
	if s.Status.Code != "" && s.Status.Code != observabilitystorageext.StatusCodeUnset {
		ps.Status = &v1trace.Status{
			Code:    mapStatusCode(s.Status.Code),
			Message: s.Status.Message,
		}
	}

	return ps
}

// ── Value Conversion Helpers ────────────────────────

// hexTo16Bytes decodes a 32-char hex string into a 16-byte TraceID.
func hexTo16Bytes(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 16 {
		return nil, fmt.Errorf("traceID must be 16 bytes, got %d", len(b))
	}
	return b, nil
}

// hexTo8Bytes decodes a 16-char hex string into an 8-byte SpanID.
func hexTo8Bytes(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 8 {
		return nil, fmt.Errorf("spanID must be 8 bytes, got %d", len(b))
	}
	return b, nil
}

// parseUnixNano converts a nanosecond timestamp string (e.g. "1783919303169446540") to uint64.
func parseUnixNano(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// mapSpanKind maps the internal SpanKind string to proto Span_SpanKind.
func mapSpanKind(kind observabilitystorageext.SpanKind) v1trace.Span_SpanKind {
	switch kind {
	case observabilitystorageext.SpanKindInternal:
		return v1trace.Span_SPAN_KIND_INTERNAL
	case observabilitystorageext.SpanKindServer:
		return v1trace.Span_SPAN_KIND_SERVER
	case observabilitystorageext.SpanKindClient:
		return v1trace.Span_SPAN_KIND_CLIENT
	case observabilitystorageext.SpanKindProducer:
		return v1trace.Span_SPAN_KIND_PRODUCER
	case observabilitystorageext.SpanKindConsumer:
		return v1trace.Span_SPAN_KIND_CONSUMER
	default:
		return v1trace.Span_SPAN_KIND_UNSPECIFIED
	}
}

// mapStatusCode maps the internal StatusCode string to proto Status_StatusCode.
func mapStatusCode(code observabilitystorageext.StatusCode) v1trace.Status_StatusCode {
	switch code {
	case observabilitystorageext.StatusCodeOk:
		return v1trace.Status_STATUS_CODE_OK
	case observabilitystorageext.StatusCodeError:
		return v1trace.Status_STATUS_CODE_ERROR
	default:
		return v1trace.Status_STATUS_CODE_UNSET
	}
}

// ── Proto Attribute Conversion ──────────────────────

// publicKeyValuesToProto converts internal KeyValue list to proto KeyValue list.
func publicKeyValuesToProto(kvs []observabilitystorageext.KeyValue) []*v1common.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	result := make([]*v1common.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		pv := publicAnyValueToProtoValue(kv.Value)
		if pv == nil {
			continue
		}
		result = append(result, &v1common.KeyValue{
			Key:   kv.Key,
			Value: pv,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// publicAnyValueToProtoValue converts an internal AnyValue to a proto AnyValue.
// Returns nil for zero values that should be omitted.
func publicAnyValueToProtoValue(v observabilitystorageext.AnyValue) *v1common.AnyValue {
	switch {
	case v.StringValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_StringValue{StringValue: *v.StringValue},
		}
	case v.IntValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_IntValue{IntValue: *v.IntValue},
		}
	case v.DoubleValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_DoubleValue{DoubleValue: *v.DoubleValue},
		}
	case v.BoolValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_BoolValue{BoolValue: *v.BoolValue},
		}
	case v.BytesValue != nil:
		decoded, _ := hex.DecodeString(*v.BytesValue)
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_BytesValue{BytesValue: decoded},
		}
	case v.ArrayValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_ArrayValue{
				ArrayValue: publicArrayToProto(v.ArrayValue),
			},
		}
	case v.KvlistValue != nil:
		return &v1common.AnyValue{
			Value: &v1common.AnyValue_KvlistValue{
				KvlistValue: publicKvlistToProto(v.KvlistValue),
			},
		}
	default:
		return nil
	}
}

// publicArrayToProto converts an internal ArrayValue to a proto ArrayValue.
func publicArrayToProto(a *observabilitystorageext.ArrayValue) *v1common.ArrayValue {
	if a == nil || len(a.Values) == 0 {
		return nil
	}
	values := make([]*v1common.AnyValue, 0, len(a.Values))
	for _, v := range a.Values {
		pv := publicAnyValueToProtoValue(v)
		if pv != nil {
			values = append(values, pv)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return &v1common.ArrayValue{Values: values}
}

// publicKvlistToProto converts an internal KvlistValue to a proto KeyValueList.
func publicKvlistToProto(kvlist *observabilitystorageext.KvlistValue) *v1common.KeyValueList {
	if kvlist == nil || len(kvlist.Values) == 0 {
		return nil
	}
	values := make([]*v1common.KeyValue, 0, len(kvlist.Values))
	for _, kv := range kvlist.Values {
		pv := publicAnyValueToProtoValue(kv.Value)
		if pv == nil {
			continue
		}
		values = append(values, &v1common.KeyValue{
			Key:   kv.Key,
			Value: pv,
		})
	}
	if len(values) == 0 {
		return nil
	}
	return &v1common.KeyValueList{Values: values}
}

// ── Proto Event & Link Conversion ───────────────────

// publicEventsToProtoEvents converts internal SpanEvent list to proto Span_Event list.
func publicEventsToProtoEvents(events []observabilitystorageext.SpanEvent) []*v1trace.Span_Event {
	if len(events) == 0 {
		return nil
	}
	result := make([]*v1trace.Span_Event, 0, len(events))
	for _, e := range events {
		result = append(result, &v1trace.Span_Event{
			TimeUnixNano: parseUnixNano(e.TimeUnixNano),
			Name:         e.Name,
			Attributes:   publicKeyValuesToProto(e.Attributes),
		})
	}
	return result
}

// publicLinksToProtoLinks converts internal SpanLink list to proto Span_Link list.
func publicLinksToProtoLinks(links []observabilitystorageext.SpanLink) []*v1trace.Span_Link {
	if len(links) == 0 {
		return nil
	}
	result := make([]*v1trace.Span_Link, 0, len(links))
	for _, l := range links {
		traceID, err := hexTo16Bytes(l.TraceID)
		if err != nil {
			continue
		}
		spanID, err := hexTo8Bytes(l.SpanID)
		if err != nil {
			continue
		}
		link := &v1trace.Span_Link{
			TraceId:    traceID,
			SpanId:     spanID,
			Attributes: publicKeyValuesToProto(l.Attributes),
		}
		if l.TraceState != "" {
			link.TraceState = l.TraceState
		}
		result = append(result, link)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
