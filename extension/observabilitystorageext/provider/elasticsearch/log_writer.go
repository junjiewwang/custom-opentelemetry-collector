// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
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
		res := rl.Resource()
		appID := getAppID(res)

		if appID == "" {
			return fmt.Errorf("app_id is required in resource attributes (app_id or app.id), refusing to write logs without app-level data isolation")
		}

		scopeLogs := rl.ScopeLogs()
		for j := 0; j < scopeLogs.Len(); j++ {
			sl := scopeLogs.At(j)
			logRecords := sl.LogRecords()
			for k := 0; k < logRecords.Len(); k++ {
				lr := logRecords.At(k)
				doc := w.logRecordToDoc(lr, res)
				doc.AppID = appID
				indexName := w.getIndexName(appID, lr.Timestamp().AsTime())

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

// logRecordToDoc converts a log record to canonical format.
func (w *LogWriter) logRecordToDoc(lr plog.LogRecord, res pcommon.Resource) StoredLogRecord {
	return storedmodel.ConvertOTLPLog(lr, res)
}

// WriteLogRecords writes pre-converted StoredLogRecord documents.
func (w *LogWriter) WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error {
	for _, rec := range records {
		appID := rec.AppID
		if appID == "" {
			return fmt.Errorf("app_id is required, refusing to write logs without app-level data isolation")
		}
		indexName := w.getIndexName(appID, time.Unix(0, rec.TimeUnixNano))
		if err := w.buffer.Add(indexName, rec); err != nil {
			return fmt.Errorf("failed to buffer log document: %w", err)
		}
	}
	return nil
}

// getIndexName returns the app-scoped, date-based index name for a given timestamp.
// Format: {prefix}-{app_id}-{date}, e.g., "otel-logs-app001-2026.06.01"
func (w *LogWriter) getIndexName(appID string, t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return fmt.Sprintf("%s-%s-%s",
		w.config.Logs.IndexPrefix,
		appID,
		t.UTC().Format(w.config.Logs.IndexDateFormat),
	)
}
