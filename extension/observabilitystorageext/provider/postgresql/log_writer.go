// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"
	"sync"
	"time"

	"encoding/json"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// logRow represents a single log record for COPY insertion.
type logRow struct {
	Timestamp      time.Time
	ObservedTime   *time.Time
	SeverityNumber int
	SeverityText   string
	Body           string
	ServiceName    string
	AppID          string
	TraceID        string
	SpanID         string
	Attributes     []byte // JSON
	Resource       []byte // JSON
}

// LogWriter writes log data to PostgreSQL using COPY protocol.
type LogWriter struct {
	client *Client
	config *Config
	logger *zap.Logger

	mu      sync.Mutex
	buffer  []logRow
	stopCh  chan struct{}
	stopped bool
}

// NewLogWriter creates a new LogWriter instance.
func NewLogWriter(client *Client, config *Config, logger *zap.Logger) *LogWriter {
	return &LogWriter{
		client: client,
		config: config,
		logger: logger.Named("pg-log-writer"),
		buffer: make([]logRow, 0, config.BatchSize),
		stopCh: make(chan struct{}),
	}
}

// Start begins the background flush loop.
func (w *LogWriter) Start() {
	go w.flushLoop()
}

// Stop signals the flush loop to stop.
func (w *LogWriter) Stop() {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
	w.mu.Unlock()
}

// WriteLogs converts plog.Logs into rows and buffers them.
func (w *LogWriter) WriteLogs(ctx context.Context, ld plog.Logs) error {
	rows := w.convertLogs(ld)
	if len(rows) == 0 {
		return nil
	}

	w.mu.Lock()
	w.buffer = append(w.buffer, rows...)
	shouldFlush := len(w.buffer) >= w.config.BatchSize
	w.mu.Unlock()

	if shouldFlush {
		return w.Flush(ctx)
	}
	return nil
}

// Flush writes all buffered rows to PostgreSQL using COPY protocol.
func (w *LogWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return nil
	}
	rows := w.buffer
	w.buffer = make([]logRow, 0, w.config.BatchSize)
	w.mu.Unlock()

	return w.copyRows(ctx, rows)
}

// flushLoop periodically flushes buffered data.
func (w *LogWriter) flushLoop() {
	ticker := time.NewTicker(w.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := w.Flush(context.Background()); err != nil {
				w.logger.Error("Background flush failed", zap.Error(err))
			}
		case <-w.stopCh:
			return
		}
	}
}

// copyRows uses COPY FROM to efficiently insert multiple rows.
// Note: body_tsv is populated by the trigger, so we don't need to include it here.
func (w *LogWriter) copyRows(ctx context.Context, rows []logRow) error {
	columns := []string{
		"timestamp", "observed_time", "severity_number", "severity_text",
		"body", "service_name", "app_id", "trace_id", "span_id",
		"attributes", "resource",
	}

	copyCount, err := w.client.Pool().CopyFrom(
		ctx,
		pgx.Identifier{w.config.Logs.TableName},
		columns,
		&logRowSource{rows: rows, idx: -1},
	)
	if err != nil {
		return fmt.Errorf("COPY logs failed: %w", err)
	}

	w.logger.Debug("Flushed logs", zap.Int64("count", copyCount))
	return nil
}

// convertLogs converts plog.Logs to internal row format.
func (w *LogWriter) convertLogs(ld plog.Logs) []logRow {
	var rows []logRow

	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		rl := rls.At(i)
		resource := rl.Resource()
		resourceJSON := attributesToJSON(resource.Attributes())
		serviceName := extractServiceName(resource)
		appID := extractAppID(resource)

		slls := rl.ScopeLogs()
		for j := 0; j < slls.Len(); j++ {
			sll := slls.At(j)
			logs := sll.LogRecords()
			for k := 0; k < logs.Len(); k++ {
				lr := logs.At(k)

				var observedTime *time.Time
				if lr.ObservedTimestamp() != 0 {
					t := lr.ObservedTimestamp().AsTime()
					observedTime = &t
				}

				row := logRow{
					Timestamp:      lr.Timestamp().AsTime(),
					ObservedTime:   observedTime,
					SeverityNumber: int(lr.SeverityNumber()),
					SeverityText:   lr.SeverityText(),
					Body:           lr.Body().AsString(),
					ServiceName:    serviceName,
					AppID:          appID,
					TraceID:        lr.TraceID().String(),
					SpanID:         lr.SpanID().String(),
					Attributes:     attributesToJSON(lr.Attributes()),
					Resource:       resourceJSON,
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// logRowSource implements pgx.CopyFromSource for log rows.
type logRowSource struct {
	rows []logRow
	idx  int
}

func (s *logRowSource) Next() bool {
	s.idx++
	return s.idx < len(s.rows)
}

func (s *logRowSource) Values() ([]any, error) {
	r := s.rows[s.idx]
	return []any{
		r.Timestamp, r.ObservedTime, r.SeverityNumber, r.SeverityText,
		r.Body, r.ServiceName, r.AppID, r.TraceID, r.SpanID,
		r.Attributes, r.Resource,
	}, nil
}

func (s *logRowSource) Err() error {
	return nil
}

// WriteLogRecords writes pre-converted StoredLogRecord documents.
func (w *LogWriter) WriteLogRecords(ctx context.Context, records []storedmodel.StoredLogRecord) error {
	rows := make([]logRow, len(records))
	for i, lr := range records {
		attrsJSON, _ := json.Marshal(lr.Attributes)
		resJSON, _ := json.Marshal(lr.Resource)
		var obsTime *time.Time
		if lr.ObservedTimeUnixNano > 0 {
			t := time.Unix(0, lr.ObservedTimeUnixNano)
			obsTime = &t
		}
		rows[i] = logRow{
			Timestamp:      time.Unix(0, lr.TimeUnixNano),
			ObservedTime:   obsTime,
			SeverityNumber: int(lr.SeverityNumber),
			SeverityText:   lr.SeverityText,
			Body:           lr.Body,
			ServiceName:    lr.ServiceName,
			AppID:          lr.AppID,
			TraceID:        lr.TraceID,
			SpanID:         lr.SpanID,
			Attributes:     attrsJSON,
			Resource:       resJSON,
		}
	}
	w.mu.Lock()
	w.buffer = append(w.buffer, rows...)
	shouldFlush := len(w.buffer) >= w.config.BatchSize
	w.mu.Unlock()
	if shouldFlush {
		return w.Flush(ctx)
	}
	return nil
}
