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
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// traceRow represents a single span row for COPY insertion.
type traceRow struct {
	TraceID        string
	SpanID         string
	ParentSpanID   string
	OperationName  string
	ServiceName    string
	SpanKind       string
	StatusCode     string
	StatusMessage  string
	StartTime      time.Time
	EndTime        time.Time
	DurationMs     float64
	AppID          string
	Attributes     []byte // JSON
	Resource       []byte // JSON
	Events         []byte // JSON
	Links          []byte // JSON
}

// TraceWriter writes trace data to PostgreSQL using COPY protocol for high throughput.
type TraceWriter struct {
	client *Client
	config *Config
	logger *zap.Logger

	mu      sync.Mutex
	buffer  []traceRow
	stopCh  chan struct{}
	stopped bool
}

// NewTraceWriter creates a new TraceWriter instance.
func NewTraceWriter(client *Client, config *Config, logger *zap.Logger) *TraceWriter {
	return &TraceWriter{
		client: client,
		config: config,
		logger: logger.Named("pg-trace-writer"),
		buffer: make([]traceRow, 0, config.BatchSize),
		stopCh: make(chan struct{}),
	}
}

// Start begins the background flush loop.
func (w *TraceWriter) Start() {
	go w.flushLoop()
}

// Stop signals the flush loop to stop.
func (w *TraceWriter) Stop() {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
	w.mu.Unlock()
}

// WriteTraces converts ptrace.Traces into rows and buffers them for batch insertion.
func (w *TraceWriter) WriteTraces(ctx context.Context, td ptrace.Traces) error {
	rows := w.convertTraces(td)
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
func (w *TraceWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return nil
	}
	rows := w.buffer
	w.buffer = make([]traceRow, 0, w.config.BatchSize)
	w.mu.Unlock()

	return w.copyRows(ctx, rows)
}

// flushLoop periodically flushes buffered data.
func (w *TraceWriter) flushLoop() {
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
func (w *TraceWriter) copyRows(ctx context.Context, rows []traceRow) error {
	columns := []string{
		"trace_id", "span_id", "parent_span_id", "operation_name", "service_name",
		"span_kind", "status_code", "status_message", "start_time", "end_time",
		"duration_ms", "app_id", "attributes", "resource", "events", "links",
	}

	copyCount, err := w.client.Pool().CopyFrom(
		ctx,
		pgx.Identifier{w.config.Traces.TableName},
		columns,
		&traceRowSource{rows: rows, idx: -1},
	)
	if err != nil {
		return fmt.Errorf("COPY traces failed: %w", err)
	}

	w.logger.Debug("Flushed traces", zap.Int64("count", copyCount))
	return nil
}

// convertTraces converts ptrace.Traces to internal row format.
func (w *TraceWriter) convertTraces(td ptrace.Traces) []traceRow {
	var rows []traceRow

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource()
		resourceJSON := attributesToJSON(resource.Attributes())
		serviceName := extractServiceName(resource)
		appID := extractAppID(resource)

		ilss := rs.ScopeSpans()
		for j := 0; j < ilss.Len(); j++ {
			ils := ilss.At(j)
			spans := ils.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				row := traceRow{
					TraceID:       span.TraceID().String(),
					SpanID:        span.SpanID().String(),
					ParentSpanID:  span.ParentSpanID().String(),
					OperationName: span.Name(),
					ServiceName:   serviceName,
					SpanKind:      span.Kind().String(),
					StatusCode:    span.Status().Code().String(),
					StatusMessage: span.Status().Message(),
					StartTime:     span.StartTimestamp().AsTime(),
					EndTime:       span.EndTimestamp().AsTime(),
					DurationMs:    float64(span.EndTimestamp()-span.StartTimestamp()) / 1e6,
					AppID:         appID,
					Attributes:    attributesToJSON(span.Attributes()),
					Resource:      resourceJSON,
					Events:        eventsToJSON(span.Events()),
					Links:         linksToJSON(span.Links()),
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// traceRowSource implements pgx.CopyFromSource for trace rows.
type traceRowSource struct {
	rows []traceRow
	idx  int
}

func (s *traceRowSource) Next() bool {
	s.idx++
	return s.idx < len(s.rows)
}

func (s *traceRowSource) Values() ([]any, error) {
	r := s.rows[s.idx]
	return []any{
		r.TraceID, r.SpanID, r.ParentSpanID, r.OperationName, r.ServiceName,
		r.SpanKind, r.StatusCode, r.StatusMessage, r.StartTime, r.EndTime,
		r.DurationMs, r.AppID, r.Attributes, r.Resource, r.Events, r.Links,
	}, nil
}

func (s *traceRowSource) Err() error {
	return nil
}

// WriteSpans writes pre-converted StoredSpan documents using COPY protocol.
func (w *TraceWriter) WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error {
	rows := make([]traceRow, len(spans))
	for i, ss := range spans {
		rows[i] = storedSpanToTraceRow(ss)
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

// storedSpanToTraceRow converts a StoredSpan to a PG trace row.
func storedSpanToTraceRow(ss storedmodel.StoredSpan) traceRow {
	attrsJSON, _ := json.Marshal(ss.Attributes)
	resJSON, _ := json.Marshal(ss.Resource)
	eventsJSON, _ := json.Marshal(ss.Events)
	linksJSON, _ := json.Marshal(ss.Links)

	return traceRow{
		TraceID:        ss.TraceID,
		SpanID:         ss.SpanID,
		ParentSpanID:   ss.ParentSpanID,
		OperationName:  ss.Name,
		ServiceName:    ss.ServiceName,
		SpanKind:       ss.Kind,
		StatusCode:     ss.Status.Code,
		StatusMessage:  ss.Status.Message,
		StartTime:      time.Unix(0, ss.StartUnixNano),
		EndTime:        time.Unix(0, ss.EndUnixNano),
		DurationMs:     float64(ss.DurationNano) / 1e6,
		AppID:          ss.AppID,
		Attributes:     attrsJSON,
		Resource:       resJSON,
		Events:         eventsJSON,
		Links:          linksJSON,
	}
}
