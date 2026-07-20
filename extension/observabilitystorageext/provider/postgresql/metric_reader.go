// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// MetricQuery represents parameters for an instant metric query.
type MetricQuery struct {
	MetricName  string
	Labels      map[string]string
	Time        time.Time
	ServiceName string
}

// MetricRangeQuery represents parameters for a range metric query.
type MetricRangeQuery struct {
	MetricName  string
	Labels      map[string]string
	TimeRange   TimeRange
	Step        time.Duration
	ServiceName string
}

// MetricResult holds the result of an instant metric query.
type MetricResult struct {
	Samples []MetricSample
}

// MetricRangeResult holds the result of a range metric query.
type MetricRangeResult struct {
	Series []MetricSeries
}

// MetricSample is a single metric data point.
type MetricSample struct {
	Labels    map[string]string
	Value     float64
	Timestamp time.Time
}

// MetricSeries is a time series of metric data points.
type MetricSeries struct {
	Labels     map[string]string
	DataPoints []MetricDataPoint
}

// MetricDataPoint is a single point in a time series.
type MetricDataPoint struct {
	Timestamp time.Time
	Value     float64
}

// MetricReader queries metric data from PostgreSQL.
type MetricReader struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// NewMetricReader creates a new MetricReader instance.
func NewMetricReader(client *Client, config *Config, logger *zap.Logger) *MetricReader {
	return &MetricReader{
		client: client,
		config: config,
		logger: logger.Named("pg-metric-reader"),
	}
}

// Query executes an instant metric query (latest value at or before a given time).
func (r *MetricReader) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("metric_name = $%d", argIdx))
	args = append(args, query.MetricName)
	argIdx++

	if !query.Time.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, query.Time)
		argIdx++
	}

	if query.ServiceName != "" {
		conditions = append(conditions, fmt.Sprintf("service_name = $%d", argIdx))
		args = append(args, query.ServiceName)
		argIdx++
	}

	for k, v := range query.Labels {
		conditions = append(conditions, fmt.Sprintf("labels @> $%d::jsonb", argIdx))
		labelJSON, _ := json.Marshal(map[string]string{k: v})
		args = append(args, string(labelJSON))
		argIdx++
	}

	whereClause := strings.Join(conditions, " AND ")

	sql := fmt.Sprintf(`
		SELECT DISTINCT ON (labels)
			   labels, value, timestamp
		FROM %s
		WHERE %s
		ORDER BY labels, timestamp DESC
	`, r.config.Metrics.TableName, whereClause)

	rows, err := r.client.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("metric query failed: %w", err)
	}
	defer rows.Close()

	var samples []MetricSample
	for rows.Next() {
		var labelsJSON []byte
		var s MetricSample
		var val *float64
		if err := rows.Scan(&labelsJSON, &val, &s.Timestamp); err != nil {
			continue
		}
		if val != nil {
			s.Value = *val
		}
		_ = json.Unmarshal(labelsJSON, &s.Labels)
		samples = append(samples, s)
	}

	return &MetricResult{Samples: samples}, nil
}

// QueryRange executes a range metric query with step-based aggregation.
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("metric_name = $%d", argIdx))
	args = append(args, query.MetricName)
	argIdx++

	conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
	args = append(args, query.TimeRange.Start)
	argIdx++

	conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
	args = append(args, query.TimeRange.End)
	argIdx++

	if query.ServiceName != "" {
		conditions = append(conditions, fmt.Sprintf("service_name = $%d", argIdx))
		args = append(args, query.ServiceName)
		argIdx++
	}

	for k, v := range query.Labels {
		conditions = append(conditions, fmt.Sprintf("labels @> $%d::jsonb", argIdx))
		labelJSON, _ := json.Marshal(map[string]string{k: v})
		args = append(args, string(labelJSON))
		argIdx++
	}

	whereClause := strings.Join(conditions, " AND ")

	// Use time_bucket if TimescaleDB is available, otherwise manual bucketing
	stepSeconds := int(query.Step.Seconds())
	if stepSeconds <= 0 {
		stepSeconds = 60
	}

	sql := fmt.Sprintf(`
		SELECT labels,
			   date_trunc('second', timestamp) -
			   (EXTRACT(EPOCH FROM date_trunc('second', timestamp))::int %% %d) * INTERVAL '1 second' AS bucket,
			   AVG(value) AS avg_value
		FROM %s
		WHERE %s
		GROUP BY labels, bucket
		ORDER BY labels, bucket
	`, stepSeconds, r.config.Metrics.TableName, whereClause)

	rows, err := r.client.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("metric range query failed: %w", err)
	}
	defer rows.Close()

	// Group by labels into series
	seriesMap := make(map[string]*MetricSeries)
	for rows.Next() {
		var labelsJSON []byte
		var ts time.Time
		var val *float64
		if err := rows.Scan(&labelsJSON, &ts, &val); err != nil {
			continue
		}

		key := string(labelsJSON)
		series, ok := seriesMap[key]
		if !ok {
			var labels map[string]string
			_ = json.Unmarshal(labelsJSON, &labels)
			series = &MetricSeries{Labels: labels}
			seriesMap[key] = series
		}

		dp := MetricDataPoint{Timestamp: ts}
		if val != nil {
			dp.Value = *val
		}
		series.DataPoints = append(series.DataPoints, dp)
	}

	result := &MetricRangeResult{}
	for _, series := range seriesMap {
		result.Series = append(result.Series, *series)
	}
	return result, nil
}

// ListMetricNames returns all available metric names.
func (r *MetricReader) ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	sql := fmt.Sprintf(`
		SELECT DISTINCT metric_name
		FROM %s
		WHERE timestamp >= $1 AND timestamp <= $2
		ORDER BY metric_name
	`, r.config.Metrics.TableName)

	rows, err := r.client.Query(ctx, sql, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("list metric names failed: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// ListLabelNames returns all label keys across all metrics.
func (r *MetricReader) ListLabelNames(ctx context.Context, timeRange TimeRange, metricName string) ([]string, error) {
	sql := fmt.Sprintf(`
		SELECT DISTINCT jsonb_object_keys(labels) AS label_name
		FROM %s
		WHERE timestamp >= $1 AND timestamp <= $2
		ORDER BY label_name
	`, r.config.Metrics.TableName)

	rows, err := r.client.Query(ctx, sql, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("list label names failed: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// ListLabelValues returns all values for a specific label.
func (r *MetricReader) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	sql := fmt.Sprintf(`
		SELECT DISTINCT labels->>$1 AS label_value
		FROM %s
		WHERE timestamp >= $2 AND timestamp <= $3
			AND labels ? $1
		ORDER BY label_value
	`, r.config.Metrics.TableName)

	rows, err := r.client.Query(ctx, sql, label, timeRange.Start, timeRange.End)
	if err != nil {
		return nil, fmt.Errorf("list label values failed: %w", err)
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			continue
		}
		values = append(values, val)
	}
	return values, nil
}
