// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"encoding/json"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// attributesToJSON converts pdata attributes to JSON bytes.
func attributesToJSON(attrs pcommon.Map) []byte {
	if attrs.Len() == 0 {
		return []byte("{}")
	}
	m := make(map[string]any, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		m[k] = valueToAny(v)
		return true
	})
	data, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// valueToAny converts a pdata Value to a Go native type.
func valueToAny(v pcommon.Value) any {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return v.Str()
	case pcommon.ValueTypeInt:
		return v.Int()
	case pcommon.ValueTypeDouble:
		return v.Double()
	case pcommon.ValueTypeBool:
		return v.Bool()
	case pcommon.ValueTypeMap:
		m := make(map[string]any)
		v.Map().Range(func(k string, val pcommon.Value) bool {
			m[k] = valueToAny(val)
			return true
		})
		return m
	case pcommon.ValueTypeSlice:
		s := v.Slice()
		arr := make([]any, s.Len())
		for i := 0; i < s.Len(); i++ {
			arr[i] = valueToAny(s.At(i))
		}
		return arr
	default:
		return v.AsString()
	}
}

// extractServiceName extracts the service.name from resource attributes.
func extractServiceName(resource pcommon.Resource) string {
	if v, ok := resource.Attributes().Get("service.name"); ok {
		return v.AsString()
	}
	return "unknown"
}

// extractAppID extracts the app_id from resource attributes without sanitization
// (PostgreSQL stores it as a plain field value and uses parameterized queries,
// so no character restrictions apply). Delegates to the shared canonical
// implementation to avoid maintaining a duplicate extraction rule.
func extractAppID(resource pcommon.Resource) string {
	return storedmodel.ExtractAppID(resource.Attributes())
}

// eventsToJSON converts span events to JSON bytes.
func eventsToJSON(events ptrace.SpanEventSlice) []byte {
	if events.Len() == 0 {
		return []byte("[]")
	}
	result := make([]map[string]any, events.Len())
	for i := 0; i < events.Len(); i++ {
		e := events.At(i)
		result[i] = map[string]any{
			"name":       e.Name(),
			"timestamp":  e.Timestamp().AsTime(),
			"attributes": json.RawMessage(attributesToJSON(e.Attributes())),
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return []byte("[]")
	}
	return data
}

// linksToJSON converts span links to JSON bytes.
func linksToJSON(links ptrace.SpanLinkSlice) []byte {
	if links.Len() == 0 {
		return []byte("[]")
	}
	result := make([]map[string]any, links.Len())
	for i := 0; i < links.Len(); i++ {
		l := links.At(i)
		result[i] = map[string]any{
			"trace_id":   l.TraceID().String(),
			"span_id":    l.SpanID().String(),
			"attributes": json.RawMessage(attributesToJSON(l.Attributes())),
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return []byte("[]")
	}
	return data
}
