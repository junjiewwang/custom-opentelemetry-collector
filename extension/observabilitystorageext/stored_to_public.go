// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"strconv"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// StoredSpanToPublic converts a stored Span to the public Span type.
// This is the ONLY place that converts compact flat maps (map[string]any)
// to OTel []KeyValue format. All Providers share this function.
func StoredSpanToPublic(ss storedmodel.StoredSpan) Span {
	return Span{
		TraceID:           ss.TraceID,
		SpanID:            ss.SpanID,
		ParentSpanID:      ss.ParentSpanID,
		Name:              ss.Name,
		Kind:              SpanKind(ss.Kind),
		StartTimeUnixNano: strconv.FormatInt(ss.StartUnixNano, 10),
		EndTimeUnixNano:   strconv.FormatInt(ss.EndUnixNano, 10),
		TraceState:        ss.TraceState,
		ServiceName:       ss.ServiceName,
		DurationNano:      strconv.FormatInt(ss.DurationNano, 10),
		Status: SpanStatus{
			Code:    StatusCode(ss.Status.Code),
			Message: ss.Status.Message,
		},
		Attributes: MapToKeyValues(ss.Attributes),
		Resource:   MapToKeyValues(ss.Resource),
		Events:     storedEventsToPublic(ss.Events),
		Links:      storedLinksToPublic(ss.Links),
	}
}

func storedEventsToPublic(events []storedmodel.StoredEvent) []SpanEvent {
	if len(events) == 0 {
		return nil
	}
	result := make([]SpanEvent, len(events))
	for i, e := range events {
		result[i] = SpanEvent{
			Name:         e.Name,
			TimeUnixNano: strconv.FormatInt(e.TimeUnixNano, 10),
			Attributes:   MapToKeyValues(e.Attributes),
		}
	}
	return result
}

func storedLinksToPublic(links []storedmodel.StoredLink) []SpanLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]SpanLink, len(links))
	for i, l := range links {
		result[i] = SpanLink{
			TraceID:    l.TraceID,
			SpanID:     l.SpanID,
			TraceState: l.TraceState,
			Attributes: MapToKeyValues(l.Attributes),
		}
	}
	return result
}
