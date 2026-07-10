// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// MetricWriter implements storage.MetricWriter for Elasticsearch.
// It converts pmetric.Metrics to ES documents and buffers them for bulk indexing.
type MetricWriter struct {
	buffer *bulkBuffer
	config *Config
	logger *zap.Logger
}

// NewMetricWriter creates a new ES metric writer.
func NewMetricWriter(client *Client, config *Config, logger *zap.Logger) *MetricWriter {
	return &MetricWriter{
		buffer: newBulkBuffer(client, config, logger, "metric"),
		config: config,
		logger: logger.Named("metric-writer"),
	}
}

// Start begins the background flush loop.
func (w *MetricWriter) Start() {
	w.buffer.Start()
}

// Stop stops the background flush loop.
func (w *MetricWriter) Stop() {
	w.buffer.Stop()
}

// WriteMetrics converts OTLP metrics to StoredMetricDataPoint documents.
// AppID validation happens per data point (not per resource) because
// storedmodel.ConvertOTLPMetric is the single source of truth for AppID
// extraction/sanitization — validating its output directly avoids
// re-implementing a duplicate, potentially inconsistent check here.
func (w *MetricWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		res := rm.Resource()

		scopeMetrics := rm.ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			sm := scopeMetrics.At(j)
			metrics := sm.Metrics()
			for k := 0; k < metrics.Len(); k++ {
				metric := metrics.At(k)
				points := storedmodel.ConvertOTLPMetric(metric, res)
				for _, pt := range points {
					if pt.AppID == "" {
						return fmt.Errorf("app_id is required, refusing to write metrics without app-level data isolation")
					}
					indexName := w.getIndexName(pt.AppID, time.UnixMilli(pt.TimeUnixMilli))
					if err := w.buffer.Add(indexName, pt); err != nil {
						return fmt.Errorf("failed to buffer metric document: %w", err)
					}
				}
			}
		}
	}
	return nil
}

// WriteMetricPoints writes pre-converted StoredMetricDataPoint documents.
func (w *MetricWriter) WriteMetricPoints(ctx context.Context, points []storedmodel.StoredMetricDataPoint) error {
	for _, dp := range points {
		appID := dp.AppID
		if appID == "" {
			return fmt.Errorf("app_id is required for metric data")
		}
		indexName := w.getIndexName(appID, time.UnixMilli(dp.TimeUnixMilli))
		if err := w.buffer.Add(indexName, dp); err != nil {
			return fmt.Errorf("failed to buffer metric document: %w", err)
		}
	}
	return nil
}

// Flush forces any buffered metric data to be written to ES.
func (w *MetricWriter) Flush(ctx context.Context) error {
	return w.buffer.Flush(ctx)
}

// gaugeToDoc, sumToDoc, histogramToDoc, summaryToDoc, metricToDocs removed —
// replaced by storedmodel.ConvertOTLPMetric().

// getIndexName returns the app-scoped, date-based index name for a given timestamp.
// Format: {prefix}-{app_id}-{date}, e.g., "otel-metrics-app001-2026.06.01"
func (w *MetricWriter) getIndexName(appID string, t time.Time) string {
	return fmt.Sprintf("%s-%s-%s",
		w.config.Metrics.IndexPrefix,
		appID,
		t.UTC().Format(w.config.Metrics.IndexDateFormat),
	)
}
