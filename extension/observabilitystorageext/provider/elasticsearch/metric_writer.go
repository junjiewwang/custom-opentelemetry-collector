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
				if len(points) == 0 && metric.Type() != pmetric.MetricTypeEmpty {
					// ConvertOTLPMetric returns nothing for a metric type it does
					// not handle (notably ExponentialHistogram). Dropping it
					// silently is indistinguishable downstream from the metric
					// never being sent, so say so.
					w.logger.Warn("dropping metric of unsupported type",
						zap.String("metric", metric.Name()),
						zap.String("type", metric.Type().String()))
					continue
				}
				for _, pt := range points {
					if pt.AppID == "" {
						return fmt.Errorf("app_id is required, refusing to write metrics without app-level data isolation")
					}
					indexName := w.getIndexName(pt.AppID, time.UnixMilli(pt.TimeUnixMilli))
					if err := w.buffer.Add(ctx, indexName, pt); err != nil {
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
		if err := w.buffer.Add(ctx, indexName, dp); err != nil {
			return fmt.Errorf("failed to buffer metric document: %w", err)
		}
	}
	return nil
}

// Flush forces any buffered metric data to be written to ES.
func (w *MetricWriter) Flush(ctx context.Context) error {
	return w.buffer.Flush(ctx)
}

// WriteRollupPoints writes pre-aggregated rollup documents to the 5m rollup
// tier index with deterministic _id. The _id is "{tier}:{metric}:{labelsHash}:{bucketMs}"
// so re-running a rollup slice overwrites the same document (idempotent).
func (w *MetricWriter) WriteRollupPoints(ctx context.Context, tier string, points []rollupPoint) error {
	for _, p := range points {
		if p.AppID == "" {
			return fmt.Errorf("app_id is required for rollup data")
		}
		indexName := w.getRollupIndexName(tier, p.AppID, time.UnixMilli(p.BucketMs))
		if err := w.buffer.AddWithID(ctx, indexName, p.DocID, p.Doc); err != nil {
			return fmt.Errorf("failed to buffer rollup document: %w", err)
		}
	}
	return nil
}

// getRollupIndexName returns the rollup tier index name for a bucket timestamp.
// Format: {prefix}-rollup-{tier}-{app_id}-{date}, e.g., "otel-metrics-rollup-5m-app001-2026.08.13".
func (w *MetricWriter) getRollupIndexName(tier, appID string, t time.Time) string {
	return fmt.Sprintf("%s-rollup-%s-%s-%s",
		w.config.Metrics.IndexPrefix,
		tier,
		appID,
		t.UTC().Format(w.config.Metrics.IndexDateFormat),
	)
}

// rollupPoint is a single pre-aggregated rollup document, carrying its bucket
// timestamp, deterministic doc ID, and the document to write.
type rollupPoint struct {
	AppID    string
	BucketMs int64
	DocID    string
	Doc      storedmodel.StoredMetricDataPoint
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
