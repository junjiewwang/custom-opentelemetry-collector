// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// PrometheusMetricReader implements MetricReader by proxying requests to the Prometheus HTTP API.
//
// Prometheus HTTP API reference: https://prometheus.io/docs/prometheus/latest/querying/api/
type PrometheusMetricReader struct {
	endpoint   string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewPrometheusMetricReader creates a new PrometheusMetricReader.
//
// Parameters:
//   - logger: structured logger for request/error logging
//   - endpoint: Prometheus HTTP API base URL (e.g., "http://prometheus:9090")
func NewPrometheusMetricReader(logger *zap.Logger, endpoint string) *PrometheusMetricReader {
	return &PrometheusMetricReader{
		endpoint: strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// QueryInstant executes a Prometheus instant query.
// GET /api/v1/query?query=xxx&time=xxx
func (r *PrometheusMetricReader) QueryInstant(ctx context.Context, rawQuery string) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/query?%s", r.endpoint, rawQuery)
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// QueryRange executes a Prometheus range query.
// GET /api/v1/query_range?query=xxx&start=xxx&end=xxx&step=xxx
func (r *PrometheusMetricReader) QueryRange(ctx context.Context, rawQuery string) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/query_range?%s", r.endpoint, rawQuery)
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// GetLabels returns all available label names from Prometheus.
// GET /api/v1/labels
func (r *PrometheusMetricReader) GetLabels(ctx context.Context) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/labels", r.endpoint)
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// GetLabelValues returns all values for the specified label name.
// GET /api/v1/label/{labelName}/values
func (r *PrometheusMetricReader) GetLabelValues(ctx context.Context, labelName string) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/label/%s/values", r.endpoint, url.PathEscape(labelName))
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// GetSeries queries series metadata from Prometheus.
// GET /api/v1/series?match[]=xxx
func (r *PrometheusMetricReader) GetSeries(ctx context.Context, rawQuery string) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/series?%s", r.endpoint, rawQuery)
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// GetMetadata returns metric metadata from Prometheus.
// GET /api/v1/metadata?metric=xxx
func (r *PrometheusMetricReader) GetMetadata(ctx context.Context, rawQuery string) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/api/v1/metadata?%s", r.endpoint, rawQuery)
	body, _, err := r.doGet(ctx, targetURL)
	return body, err
}

// doGet performs an HTTP GET request and returns the response body, status code, and any error.
// It enforces the maxResponseSize limit to prevent OOM.
func (r *PrometheusMetricReader) doGet(ctx context.Context, targetURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.logger.Warn("Prometheus proxy request failed",
			zap.String("url", targetURL),
			zap.Error(err),
		)
		return nil, 0, fmt.Errorf("backend request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit response size to prevent OOM (50MB)
	limited := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limited)
	if err != nil {
		r.logger.Warn("Failed to read Prometheus response",
			zap.String("url", targetURL),
			zap.Error(err),
		)
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	return body, resp.StatusCode, nil
}
