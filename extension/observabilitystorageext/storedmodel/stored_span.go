// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package storedmodel defines the canonical storage types shared by all Provider
// implementations (ES, PG, MongoDB, etc.) and the OTLP→StoredSpan conversion.
//
// This is a leaf package — it only depends on OTLP proto (pdata), never on
// the parent observabilitystorageext package. This avoids circular imports:
// both observabilitystorageext and provider/* packages can freely import storedmodel.
//
// Public API conversion (StoredSpan → types.Span) lives in the parent package
// (stored_to_public.go) because it needs both storedmodel and types.go types.
package storedmodel

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// ═══════════════════════════════════════════════════
// Canonical Storage Types
// ═══════════════════════════════════════════════════

// StoredSpan is the unified storage type for traces, used by ALL providers
// for both write and read paths. Field names align with OTLP JSON conventions
// while attributes use compact flat maps (not KeyValue lists) for storage efficiency.
type StoredSpan struct {
	// ═══ OTLP Core Fields ═══
	TraceID       string       `json:"traceId"`
	SpanID        string       `json:"spanId"`
	ParentSpanID  string       `json:"parentSpanId,omitempty"`
	Name          string       `json:"name"`
	Kind          string       `json:"kind"`
	StartUnixNano int64        `json:"startTimeUnixNano"`
	EndUnixNano   int64        `json:"endTimeUnixNano"`
	Status        StoredStatus `json:"status"`
	TraceState    string       `json:"traceState,omitempty"`

	// ═══ Compact Attributes (flat map, NOT KeyValue list) ═══
	Attributes map[string]any `json:"attributes"`
	Resource   map[string]any `json:"resource"`

	// ═══ Scope Information ═══
	Scope StoredScope `json:"scope,omitempty"`

	// ═══ Events & Links ═══
	Events []StoredEvent `json:"events,omitempty"`
	Links  []StoredLink  `json:"links,omitempty"`

	// ═══ Derived / Enriched Fields ═══
	DurationNano int64  `json:"durationNano"`
	ServiceName  string `json:"serviceName"`
	AppID        string `json:"appId,omitempty"`
}

// StoredScope preserves InstrumentationScope info that was previously discarded.
type StoredScope struct {
	Name       string         `json:"name"`
	Version    string         `json:"version,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// StoredStatus represents the span status (code + message).
type StoredStatus struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// StoredEvent represents a timestamped event on a span.
type StoredEvent struct {
	TimeUnixNano int64          `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

// StoredLink represents a link to another span.
type StoredLink struct {
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	TraceState string         `json:"traceState,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// ═══════════════════════════════════════════════════
// OTLP → StoredSpan  Conversion
// ═══════════════════════════════════════════════════

// ConvertOTLPSpan converts an OTLP ptrace.Span (with scope and resource) into
// the canonical StoredSpan format. All Providers share this function.
func ConvertOTLPSpan(span ptrace.Span, scope ptrace.ScopeSpans, resource pcommon.Resource) StoredSpan {
	resourceAttrs := resource.Attributes()
	serviceName := getAttrStr(resourceAttrs, "service.name", "unknown")
	appID := getAppIDAttr(resourceAttrs)

	return StoredSpan{
		TraceID:       span.TraceID().String(),
		SpanID:        span.SpanID().String(),
		ParentSpanID:  toParentID(span.ParentSpanID()),
		Name:          span.Name(),
		Kind:          span.Kind().String(),
		StartUnixNano: int64(span.StartTimestamp()),
		EndUnixNano:   int64(span.EndTimestamp()),
		DurationNano:  safeDuration(span.StartTimestamp(), span.EndTimestamp()),
		Status: StoredStatus{
			Code:    span.Status().Code().String(),
			Message: span.Status().Message(),
		},
		TraceState: span.TraceState().AsRaw(),
		Scope: StoredScope{
			Name:       scope.Scope().Name(),
			Version:    scope.Scope().Version(),
			Attributes: pcommonMapToFlat(scope.Scope().Attributes()),
		},
		Attributes:  pcommonMapToFlat(span.Attributes()),
		Resource:    pcommonMapToFlat(resourceAttrs),
		Events:      convertEvents(span.Events()),
		Links:       convertLinks(span.Links()),
		ServiceName: serviceName,
		AppID:       appID,
	}
}

// ═══════════════════════════════════════════════════
// Internal Helpers
// ═══════════════════════════════════════════════════

// safeDuration returns end-start as int64, clamping negative values to 0.
// pcommon.Timestamp is uint64 nanoseond — direct subtraction can underflow
// to a huge positive if clock has drifted (end < start). Converting to int64
// first saturating at 0 prevents corrupted data.
func safeDuration(start, end pcommon.Timestamp) int64 {
	startNs := int64(start)
	endNs := int64(end)
	if endNs > startNs {
		return endNs - startNs
	}
	return 0
}

// toParentID returns a hex string for the parent span ID, or empty for root spans.
func toParentID(parentID pcommon.SpanID) string {
	s := parentID.String()
	if s == "" || s == "000000000000000" {
		return ""
	}
	return s
}

// pcommonMapToFlat converts a pcommon.Map to a flat map[string]any.
func pcommonMapToFlat(attrs pcommon.Map) map[string]any {
	if attrs.Len() == 0 {
		return nil
	}
	result := make(map[string]any, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		result[k] = valueToAny(v)
		return true
	})
	return result
}

// valueToAny converts a pcommon.Value to a native Go type.
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
		return pcommonMapToFlat(v.Map())
	case pcommon.ValueTypeSlice:
		slice := v.Slice()
		result := make([]any, slice.Len())
		for i := 0; i < slice.Len(); i++ {
			result[i] = valueToAny(slice.At(i))
		}
		return result
	case pcommon.ValueTypeBytes:
		return v.Bytes().AsRaw()
	default:
		return v.AsString()
	}
}

// getAttrStr extracts a string attribute from a pcommon.Map with a default.
func getAttrStr(attrs pcommon.Map, key string, default_ string) string {
	if val, ok := attrs.Get(key); ok {
		return val.AsString()
	}
	return default_
}

// getAppIDAttr extracts the app ID from resource attributes and applies the
// shared storage-safe sanitization (see SanitizeAppID). Does NOT lowercase —
// verified against the production ES cluster that uppercase index names are
// allowed (docs/2026-07-09/appid-sanitize-unification-design.md §2.1), and
// PostgreSQL has no such character restriction at all.
func getAppIDAttr(attrs pcommon.Map) string {
	return SanitizeAppID(ExtractAppID(attrs), SanitizeOptions{Lowercase: false})
}

// convertEvents converts OTLP span events to StoredEvent slice.
func convertEvents(events ptrace.SpanEventSlice) []StoredEvent {
	if events.Len() == 0 {
		return nil
	}
	result := make([]StoredEvent, events.Len())
	for i := 0; i < events.Len(); i++ {
		e := events.At(i)
		result[i] = StoredEvent{
			TimeUnixNano: int64(e.Timestamp()),
			Name:         e.Name(),
			Attributes:   pcommonMapToFlat(e.Attributes()),
		}
	}
	return result
}

// convertLinks converts OTLP span links to StoredLink slice.
func convertLinks(links ptrace.SpanLinkSlice) []StoredLink {
	if links.Len() == 0 {
		return nil
	}
	result := make([]StoredLink, links.Len())
	for i := 0; i < links.Len(); i++ {
		l := links.At(i)
		result[i] = StoredLink{
			TraceID:    l.TraceID().String(),
			SpanID:     l.SpanID().String(),
			TraceState: l.TraceState().AsRaw(),
			Attributes: pcommonMapToFlat(l.Attributes()),
		}
	}
	return result
}

// Public API conversion functions (StoredSpan → Span, storedEventsToPublic, storedLinksToPublic)
// are in the parent package: observabilitystorageext/stored_to_public.go
