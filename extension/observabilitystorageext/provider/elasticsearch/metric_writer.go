// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

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

// WriteMetrics converts pmetric.Metrics to ES documents and buffers them.
func (w *MetricWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := extractResourceAttributes(rm.Resource())
		serviceName := getServiceNameFromResource(rm.Resource())

		scopeMetrics := rm.ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			sm := scopeMetrics.At(j)
			metrics := sm.Metrics()
			for k := 0; k < metrics.Len(); k++ {
				metric := metrics.At(k)
				docs := w.metricToDocs(metric, resource, serviceName)
			for _, doc := range docs {
				ts, _ := doc["@timestamp"].(string)
				t, _ := time.Parse(esTimestampFormat, ts)
				if t.IsZero() {
					t = time.Now()
				}
					indexName := w.getIndexName(t)
					if err := w.buffer.Add(indexName, doc); err != nil {
						return fmt.Errorf("failed to buffer metric document: %w", err)
					}
				}
			}
		}
	}
	return nil
}

// Flush forces any buffered metric data to be written to ES.
func (w *MetricWriter) Flush(ctx context.Context) error {
	return w.buffer.Flush(ctx)
}

// metricToDocs converts a single metric to one or more ES documents.
// Each data point becomes a separate document.
func (w *MetricWriter) metricToDocs(metric pmetric.Metric, resource map[string]any, serviceName string) []map[string]any {
	var docs []map[string]any

	switch metric.Type() {
	case pmetric.MetricTypeGauge:
		docs = w.gaugeToDoc(metric, resource, serviceName)
	case pmetric.MetricTypeSum:
		docs = w.sumToDoc(metric, resource, serviceName)
	case pmetric.MetricTypeHistogram:
		docs = w.histogramToDoc(metric, resource, serviceName)
	case pmetric.MetricTypeSummary:
		docs = w.summaryToDoc(metric, resource, serviceName)
	default:
		w.logger.Debug("Unsupported metric type, skipping",
			zap.String("metric_name", metric.Name()),
			zap.String("type", metric.Type().String()),
		)
	}
	return docs
}

func (w *MetricWriter) gaugeToDoc(metric pmetric.Metric, resource map[string]any, serviceName string) []map[string]any {
	dps := metric.Gauge().DataPoints()
	docs := make([]map[string]any, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		doc := map[string]any{
			"@timestamp":   formatTimestamp(dp.Timestamp().AsTime()),
			"metric_name":  metric.Name(),
			"metric_type":  "gauge",
			"service_name": serviceName,
			"resource":     resource,
			"labels":       attributesToMap(dp.Attributes()),
		}
		switch dp.ValueType() {
		case pmetric.NumberDataPointValueTypeDouble:
			doc["value"] = dp.DoubleValue()
		case pmetric.NumberDataPointValueTypeInt:
			doc["value"] = float64(dp.IntValue())
		}
		if appID, ok := resource["app_id"]; ok {
			doc["app_id"] = appID
		}
		docs = append(docs, doc)
	}
	return docs
}

func (w *MetricWriter) sumToDoc(metric pmetric.Metric, resource map[string]any, serviceName string) []map[string]any {
	dps := metric.Sum().DataPoints()
	docs := make([]map[string]any, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		doc := map[string]any{
			"@timestamp":   formatTimestamp(dp.Timestamp().AsTime()),
			"metric_name":  metric.Name(),
			"metric_type":  "counter",
			"service_name": serviceName,
			"resource":     resource,
			"labels":       attributesToMap(dp.Attributes()),
		}
		switch dp.ValueType() {
		case pmetric.NumberDataPointValueTypeDouble:
			doc["value"] = dp.DoubleValue()
		case pmetric.NumberDataPointValueTypeInt:
			doc["value"] = float64(dp.IntValue())
		}
		if appID, ok := resource["app_id"]; ok {
			doc["app_id"] = appID
		}
		docs = append(docs, doc)
	}
	return docs
}

func (w *MetricWriter) histogramToDoc(metric pmetric.Metric, resource map[string]any, serviceName string) []map[string]any {
	dps := metric.Histogram().DataPoints()
	docs := make([]map[string]any, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		doc := map[string]any{
			"@timestamp":   formatTimestamp(dp.Timestamp().AsTime()),
			"metric_name":  metric.Name(),
			"metric_type":  "histogram",
			"service_name": serviceName,
			"resource":     resource,
			"labels":       attributesToMap(dp.Attributes()),
			"value":        dp.Sum(),
		}
		if dp.HasSum() {
			doc["value"] = dp.Sum()
		}
		// Store histogram bucket data
		if dp.BucketCounts().Len() > 0 {
			counts := make([]uint64, dp.BucketCounts().Len())
			for j := 0; j < dp.BucketCounts().Len(); j++ {
				counts[j] = dp.BucketCounts().At(j)
			}
			bounds := make([]float64, dp.ExplicitBounds().Len())
			for j := 0; j < dp.ExplicitBounds().Len(); j++ {
				bounds[j] = dp.ExplicitBounds().At(j)
			}
			doc["histogram"] = map[string]any{
				"counts": counts,
				"values": bounds,
			}
		}
		if appID, ok := resource["app_id"]; ok {
			doc["app_id"] = appID
		}
		docs = append(docs, doc)
	}
	return docs
}

func (w *MetricWriter) summaryToDoc(metric pmetric.Metric, resource map[string]any, serviceName string) []map[string]any {
	dps := metric.Summary().DataPoints()
	docs := make([]map[string]any, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		doc := map[string]any{
			"@timestamp":   formatTimestamp(dp.Timestamp().AsTime()),
			"metric_name":  metric.Name(),
			"metric_type":  "summary",
			"service_name": serviceName,
			"resource":     resource,
			"labels":       attributesToMap(dp.Attributes()),
			"value":        dp.Sum(),
		}
		if appID, ok := resource["app_id"]; ok {
			doc["app_id"] = appID
		}
		docs = append(docs, doc)
	}
	return docs
}

// getIndexName returns the date-based index name for a given timestamp.
func (w *MetricWriter) getIndexName(t time.Time) string {
	return fmt.Sprintf("%s-%s",
		w.config.Metrics.IndexPrefix,
		t.UTC().Format(w.config.Metrics.IndexDateFormat),
	)
}
