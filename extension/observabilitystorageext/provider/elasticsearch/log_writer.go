// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// LogWriter implements storage.LogWriter for Elasticsearch.
// It converts plog.Logs to ES documents and buffers them for bulk indexing.
type LogWriter struct {
	buffer *bulkBuffer
	config *Config
	logger *zap.Logger
}

// NewLogWriter creates a new ES log writer.
func NewLogWriter(client *Client, config *Config, logger *zap.Logger) *LogWriter {
	return &LogWriter{
		buffer: newBulkBuffer(client, config, logger, "log"),
		config: config,
		logger: logger.Named("log-writer"),
	}
}

// Start begins the background flush loop.
func (w *LogWriter) Start() {
	w.buffer.Start()
}

// Stop stops the background flush loop.
func (w *LogWriter) Stop() {
	w.buffer.Stop()
}

// WriteLogs converts plog.Logs to ES documents and buffers them.
func (w *LogWriter) WriteLogs(ctx context.Context, ld plog.Logs) error {
	resourceLogs := ld.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		rl := resourceLogs.At(i)
		resource := extractResourceAttributes(rl.Resource())
		serviceName := getServiceNameFromResourceLogs(rl.Resource())

		scopeLogs := rl.ScopeLogs()
		for j := 0; j < scopeLogs.Len(); j++ {
			sl := scopeLogs.At(j)
			logRecords := sl.LogRecords()
			for k := 0; k < logRecords.Len(); k++ {
				lr := logRecords.At(k)
				doc := w.logRecordToDoc(lr, resource, serviceName)
				indexName := w.getIndexName(lr.Timestamp().AsTime())

				if err := w.buffer.Add(indexName, doc); err != nil {
					return fmt.Errorf("failed to buffer log document: %w", err)
				}
			}
		}
	}
	return nil
}

// Flush forces any buffered log data to be written to ES.
func (w *LogWriter) Flush(ctx context.Context) error {
	return w.buffer.Flush(ctx)
}

// logRecordToDoc converts a single log record to an ES document.
func (w *LogWriter) logRecordToDoc(lr plog.LogRecord, resource map[string]any, serviceName string) map[string]any {
	doc := map[string]any{
		"timestamp":       formatTimestamp(lr.Timestamp().AsTime()),
		"observed_time":   formatTimestamp(lr.ObservedTimestamp().AsTime()),
		"severity":        lr.SeverityText(),
		"severity_number": int32(lr.SeverityNumber()),
		"service_name":    serviceName,
		"resource":        resource,
	}

	// Body
	if body := lr.Body(); body.Type() != pcommon.ValueTypeEmpty {
		doc["body"] = body.AsString()
	}

	// Trace context
	traceID := lr.TraceID().String()
	if traceID != "" && traceID != "00000000000000000000000000000000" {
		doc["trace_id"] = traceID
	}
	spanID := lr.SpanID().String()
	if spanID != "" && spanID != "0000000000000000" {
		doc["span_id"] = spanID
	}

	// Attributes
	attrs := attributesToMap(lr.Attributes())
	if len(attrs) > 0 {
		doc["attributes"] = attrs
	}

	// App ID from resource
	if appID, ok := resource["app_id"]; ok {
		doc["app_id"] = appID
	}

	return doc
}

// getIndexName returns the date-based index name for a given timestamp.
func (w *LogWriter) getIndexName(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return fmt.Sprintf("%s-%s",
		w.config.Logs.IndexPrefix,
		t.UTC().Format(w.config.Logs.IndexDateFormat),
	)
}
