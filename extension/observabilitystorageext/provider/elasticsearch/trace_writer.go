// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pcommon"
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

// WriteTraces converts ptrace.Traces to StoredSpan documents and buffers them.
func (w *TraceWriter) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	resourceSpans := td.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		resource := rs.Resource()

		appID := getAppID(resource)
		if appID == "" {
			return fmt.Errorf("app_id is required in resource attributes (app_id or app.id), refusing to write traces without app-level data isolation")
		}

		scopeSpans := rs.ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				doc := w.convertSpan(span, ss, resource)
				doc.AppID = appID
				indexName := w.getIndexName(appID, span.StartTimestamp().AsTime())

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

// convertSpan converts an OTLP span to the canonical StoredSpan format.
func (w *TraceWriter) convertSpan(span ptrace.Span, scope ptrace.ScopeSpans, resource pcommon.Resource) storedmodel.StoredSpan {
	return storedmodel.ConvertOTLPSpan(span, scope, resource)
}

// getIndexName returns the app-scoped, date-based index name for a given timestamp.
// Format: {prefix}-{app_id}-{date}, e.g., "otel-traces-app001-2026.06.01"
func (w *TraceWriter) getIndexName(appID string, t time.Time) string {
	return fmt.Sprintf("%s-%s-%s",
		w.config.Traces.IndexPrefix,
		appID,
		t.UTC().Format(w.config.Traces.IndexDateFormat),
	)
}
