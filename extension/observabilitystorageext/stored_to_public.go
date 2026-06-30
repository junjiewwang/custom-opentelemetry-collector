// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"fmt"
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

// StoredLogRecordToPublic converts a stored LogRecord to the public LogRecord type.
func StoredLogRecordToPublic(lr storedmodel.StoredLogRecord) LogRecord {
	return LogRecord{
		TimeUnixNano:         strconv.FormatInt(lr.TimeUnixNano, 10),
		ObservedTimeUnixNano: strconv.FormatInt(lr.ObservedTimeUnixNano, 10),
		TraceID:              lr.TraceID,
		SpanID:               lr.SpanID,
		SeverityNumber:       lr.SeverityNumber,
		SeverityText:         lr.SeverityText,
		Body:                 lr.Body,
		Attributes:           MapToKeyValues(lr.Attributes),
		Resource:             MapToKeyValues(lr.Resource),
		ServiceName:          lr.ServiceName,
		AppID:                lr.AppID,
	}
}

// StoredMetricDataPointToPublic converts a stored metric data point to public format.
func StoredMetricDataPointToPublic(dp storedmodel.StoredMetricDataPoint) MetricDataPoint {
	return MetricDataPoint{
		Labels:       toStringMap(dp.Labels),
		Value:        dp.Value,
		TimeUnixNano: strconv.FormatInt(dp.TimeUnixNano, 10),
	}
}

// toStringMap converts map[string]any to map[string]string for labels.
func toStringMap(m map[string]any) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}
