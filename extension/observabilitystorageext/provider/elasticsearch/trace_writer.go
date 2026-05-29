// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// TraceWriter implements storage.TraceWriter for Elasticsearch.
// It converts ptrace.Traces to ES documents and buffers them for bulk indexing.
type TraceWriter struct {
	buffer *bulkBuffer
	config *Config
	logger *zap.Logger
}

// NewTraceWriter creates a new ES trace writer.
func NewTraceWriter(client *Client, config *Config, logger *zap.Logger) *TraceWriter {
	return &TraceWriter{
		buffer: newBulkBuffer(client, config, logger, "trace"),
		config: config,
		logger: logger.Named("trace-writer"),
	}
}

// Start begins the background flush loop.
func (w *TraceWriter) Start() {
	w.buffer.Start()
}

// Stop stops the background flush loop.
func (w *TraceWriter) Stop() {
	w.buffer.Stop()
}

// WriteTraces converts ptrace.Traces to ES documents and buffers them.
func (w *TraceWriter) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	resourceSpans := td.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		resource := extractResourceAttributes(rs.Resource())
		serviceName := getServiceName(rs.Resource())

		scopeSpans := rs.ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				doc := w.spanToDoc(span, resource, serviceName)
				indexName := w.getIndexName(span.StartTimestamp().AsTime())

				if err := w.buffer.Add(indexName, doc); err != nil {
					return fmt.Errorf("failed to buffer trace document: %w", err)
				}
			}
		}
	}
	return nil
}

// Flush forces any buffered trace data to be written to ES.
func (w *TraceWriter) Flush(ctx context.Context) error {
	return w.buffer.Flush(ctx)
}

// spanToDoc converts a single span to an ES document map.
func (w *TraceWriter) spanToDoc(span ptrace.Span, resource map[string]any, serviceName string) map[string]any {
	doc := map[string]any{
		"trace_id":       span.TraceID().String(),
		"span_id":        span.SpanID().String(),
		"operation_name": span.Name(),
		"service_name":   serviceName,
		"span_kind":      span.Kind().String(),
		"status_code":    span.Status().Code().String(),
		"start_time":     formatTimestamp(span.StartTimestamp().AsTime()),
		"end_time":       formatTimestamp(span.EndTimestamp().AsTime()),
		"duration_us":    (span.EndTimestamp().AsTime().Sub(span.StartTimestamp().AsTime())).Microseconds(),
		"resource":       resource,
	}

	// Parent span ID
	parentID := span.ParentSpanID().String()
	if parentID != "" && parentID != "0000000000000000" {
		doc["parent_span_id"] = parentID
	}

	// Status message
	if msg := span.Status().Message(); msg != "" {
		doc["status_message"] = msg
	}

	// Attributes
	attrs := attributesToMap(span.Attributes())
	if len(attrs) > 0 {
		doc["attributes"] = attrs
	}

	// Events
	if span.Events().Len() > 0 {
		events := make([]map[string]any, 0, span.Events().Len())
		for i := 0; i < span.Events().Len(); i++ {
			event := span.Events().At(i)
			e := map[string]any{
				"name":      event.Name(),
				"timestamp": formatTimestamp(event.Timestamp().AsTime()),
			}
			eventAttrs := attributesToMap(event.Attributes())
			if len(eventAttrs) > 0 {
				e["attributes"] = eventAttrs
			}
			events = append(events, e)
		}
		doc["events"] = events
	}

	// Links
	if span.Links().Len() > 0 {
		links := make([]map[string]any, 0, span.Links().Len())
		for i := 0; i < span.Links().Len(); i++ {
			link := span.Links().At(i)
			links = append(links, map[string]any{
				"trace_id": link.TraceID().String(),
				"span_id":  link.SpanID().String(),
			})
		}
		doc["links"] = links
	}

	return doc
}

// getIndexName returns the date-based index name for a given timestamp.
func (w *TraceWriter) getIndexName(t time.Time) string {
	return fmt.Sprintf("%s-%s",
		w.config.Traces.IndexPrefix,
		t.UTC().Format(w.config.Traces.IndexDateFormat),
	)
}
