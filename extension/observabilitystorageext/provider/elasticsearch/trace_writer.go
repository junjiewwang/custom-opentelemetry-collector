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

// WriteTraces converts ptrace.Traces to StoredSpan documents and writes them
// via WriteSpans. Deprecated: callers should convert to []StoredSpan once at
// the extension layer and call WriteSpans directly (see extension.go
// WriteTraces), avoiding a second, independent conversion path here.
// This method is kept only for direct callers/tests and now delegates to the
// same canonical conversion + write path as WriteSpans, so AppID handling
// (and any other conversion logic) can never drift between the two entry points.
func (w *TraceWriter) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	return w.WriteSpans(ctx, w.convertTraces(td))
}

// WriteSpans writes pre-converted StoredSpan documents to Elasticsearch.
func (w *TraceWriter) WriteSpans(ctx context.Context, spans []StoredSpan) error {
	for _, ss := range spans {
		appID := ss.AppID
		if appID == "" {
			return fmt.Errorf("app_id is required in resource attributes, refusing to write traces without app-level data isolation")
		}
		indexName := w.getIndexName(appID, time.Unix(0, ss.StartUnixNano))
		if err := w.buffer.Add(ctx, indexName, ss); err != nil {
			return fmt.Errorf("failed to buffer trace document: %w", err)
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

// convertTraces converts all spans in ptrace.Traces to the canonical
// StoredSpan format, reusing convertSpan for each span. This is the single
// conversion path shared by both WriteTraces (deprecated) and any future
// direct caller, so there is exactly one place where OTLP→StoredSpan
// conversion happens for this writer.
func (w *TraceWriter) convertTraces(td ptrace.Traces) []StoredSpan {
	resourceSpans := td.ResourceSpans()
	var spans []StoredSpan
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		resource := rs.Resource()
		scopeSpans := rs.ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			sp := ss.Spans()
			for k := 0; k < sp.Len(); k++ {
				spans = append(spans, w.convertSpan(sp.At(k), ss, resource))
			}
		}
	}
	return spans
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
