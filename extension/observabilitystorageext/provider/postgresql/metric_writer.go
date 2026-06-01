// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// metricRow represents a single metric data point for COPY insertion.
type metricRow struct {
	MetricName    string
	MetricType    string
	ServiceName   string
	AppID         string
	Timestamp     time.Time
	Value         *float64
	HistogramMin  *float64
	HistogramMax  *float64
	HistogramSum  *float64
	HistogramCount *int64
	Exemplars     []byte // JSON
	Labels        []byte // JSON
	Resource      []byte // JSON
}

// MetricWriter writes metric data to PostgreSQL using COPY protocol.
type MetricWriter struct {
	client *Client
	config *Config
	logger *zap.Logger

	mu      sync.Mutex
	buffer  []metricRow
	stopCh  chan struct{}
	stopped bool
}

// NewMetricWriter creates a new MetricWriter instance.
func NewMetricWriter(client *Client, config *Config, logger *zap.Logger) *MetricWriter {
	return &MetricWriter{
		client: client,
		config: config,
		logger: logger.Named("pg-metric-writer"),
		buffer: make([]metricRow, 0, config.BatchSize),
		stopCh: make(chan struct{}),
	}
}

// Start begins the background flush loop.
func (w *MetricWriter) Start() {
	go w.flushLoop()
}

// Stop signals the flush loop to stop.
func (w *MetricWriter) Stop() {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
	w.mu.Unlock()
}

// WriteMetrics converts pmetric.Metrics into rows and buffers them.
func (w *MetricWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	rows := w.convertMetrics(md)
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
func (w *MetricWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffer) == 0 {
		w.mu.Unlock()
		return nil
	}
	rows := w.buffer
	w.buffer = make([]metricRow, 0, w.config.BatchSize)
	w.mu.Unlock()

	return w.copyRows(ctx, rows)
}

// flushLoop periodically flushes buffered data.
func (w *MetricWriter) flushLoop() {
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
func (w *MetricWriter) copyRows(ctx context.Context, rows []metricRow) error {
	columns := []string{
		"metric_name", "metric_type", "service_name", "app_id", "timestamp",
		"value", "histogram_min", "histogram_max", "histogram_sum", "histogram_count",
		"exemplars", "labels", "resource",
	}

	copyCount, err := w.client.Pool().CopyFrom(
		ctx,
		pgx.Identifier{w.config.Metrics.TableName},
		columns,
		&metricRowSource{rows: rows, idx: -1},
	)
	if err != nil {
		return fmt.Errorf("COPY metrics failed: %w", err)
	}

	w.logger.Debug("Flushed metrics", zap.Int64("count", copyCount))
	return nil
}

// convertMetrics converts pmetric.Metrics to internal row format.
func (w *MetricWriter) convertMetrics(md pmetric.Metrics) []metricRow {
	var rows []metricRow

	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		rm := rms.At(i)
		resource := rm.Resource()
		resourceJSON := attributesToJSON(resource.Attributes())
		serviceName := extractServiceName(resource)
		appID := extractAppID(resource)

		ilms := rm.ScopeMetrics()
		for j := 0; j < ilms.Len(); j++ {
			ilm := ilms.At(j)
			metrics := ilm.Metrics()
			for k := 0; k < metrics.Len(); k++ {
				m := metrics.At(k)
				metricRows := w.convertSingleMetric(m, serviceName, appID, resourceJSON)
				rows = append(rows, metricRows...)
			}
		}
	}
	return rows
}

// convertSingleMetric converts a single metric to rows based on its type.
func (w *MetricWriter) convertSingleMetric(m pmetric.Metric, serviceName, appID string, resourceJSON []byte) []metricRow {
	var rows []metricRow

	switch m.Type() {
	case pmetric.MetricTypeGauge:
		dps := m.Gauge().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			val := dp.DoubleValue()
			if dp.ValueType() == pmetric.NumberDataPointValueTypeInt {
				val = float64(dp.IntValue())
			}
			rows = append(rows, metricRow{
				MetricName:  m.Name(),
				MetricType:  "gauge",
				ServiceName: serviceName,
				AppID:       appID,
				Timestamp:   dp.Timestamp().AsTime(),
				Value:       &val,
				Labels:      attributesToJSON(dp.Attributes()),
				Resource:    resourceJSON,
				Exemplars:   []byte("[]"),
			})
		}
	case pmetric.MetricTypeSum:
		dps := m.Sum().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			val := dp.DoubleValue()
			if dp.ValueType() == pmetric.NumberDataPointValueTypeInt {
				val = float64(dp.IntValue())
			}
			rows = append(rows, metricRow{
				MetricName:  m.Name(),
				MetricType:  "sum",
				ServiceName: serviceName,
				AppID:       appID,
				Timestamp:   dp.Timestamp().AsTime(),
				Value:       &val,
				Labels:      attributesToJSON(dp.Attributes()),
				Resource:    resourceJSON,
				Exemplars:   []byte("[]"),
			})
		}
	case pmetric.MetricTypeHistogram:
		dps := m.Histogram().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			hMin := dp.Min()
			hMax := dp.Max()
			hSum := dp.Sum()
			hCount := int64(dp.Count())
			rows = append(rows, metricRow{
				MetricName:     m.Name(),
				MetricType:     "histogram",
				ServiceName:    serviceName,
				AppID:          appID,
				Timestamp:      dp.Timestamp().AsTime(),
				HistogramMin:   &hMin,
				HistogramMax:   &hMax,
				HistogramSum:   &hSum,
				HistogramCount: &hCount,
				Labels:         attributesToJSON(dp.Attributes()),
				Resource:       resourceJSON,
				Exemplars:      []byte("[]"),
			})
		}
	}
	return rows
}

// metricRowSource implements pgx.CopyFromSource for metric rows.
type metricRowSource struct {
	rows []metricRow
	idx  int
}

func (s *metricRowSource) Next() bool {
	s.idx++
	return s.idx < len(s.rows)
}

func (s *metricRowSource) Values() ([]any, error) {
	r := s.rows[s.idx]
	return []any{
		r.MetricName, r.MetricType, r.ServiceName, r.AppID, r.Timestamp,
		r.Value, r.HistogramMin, r.HistogramMax, r.HistogramSum, r.HistogramCount,
		r.Exemplars, r.Labels, r.Resource,
	}, nil
}

func (s *metricRowSource) Err() error {
	return nil
}
