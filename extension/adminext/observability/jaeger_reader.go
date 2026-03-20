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

// maxResponseSize is the maximum allowed response body size (50MB) to prevent OOM.
const maxResponseSize = 50 * 1024 * 1024

// JaegerTraceReader implements TraceReader by proxying requests to the Jaeger Query HTTP API.
//
// Jaeger Query API reference: https://www.jaegertracing.io/docs/apis/#http-json
type JaegerTraceReader struct {
	endpoint   string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewJaegerTraceReader creates a new JaegerTraceReader.
//
// Parameters:
//   - logger: structured logger for request/error logging
//   - endpoint: Jaeger Query HTTP API base URL (e.g., "http://jaeger-query:16686")
func NewJaegerTraceReader(logger *zap.Logger, endpoint string) *JaegerTraceReader {
	return &JaegerTraceReader{
		endpoint: strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// SearchTraces searches for traces by forwarding the raw query to Jaeger.
// GET /api/traces?service=xxx&operation=xxx&tags=xxx&limit=20&start=xxx&end=xxx&minDuration=xxx&maxDuration=xxx
func (r *JaegerTraceReader) SearchTraces(ctx context.Context, query TraceSearchQuery) (*TraceSearchResult, error) {
	targetURL := fmt.Sprintf("%s/api/traces?%s", r.endpoint, query.RawQuery)
	body, statusCode, err := r.doGet(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	return &TraceSearchResult{
		StatusCode: statusCode,
		RawBody:    body,
	}, nil
}

// GetTrace retrieves a single trace by its trace ID.
// GET /api/traces/{traceID}
func (r *JaegerTraceReader) GetTrace(ctx context.Context, traceID string) (*TraceResult, error) {
	targetURL := fmt.Sprintf("%s/api/traces/%s", r.endpoint, url.PathEscape(traceID))
	body, statusCode, err := r.doGet(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	return &TraceResult{
		StatusCode: statusCode,
		RawBody:    body,
	}, nil
}

// GetServices returns all available service names from Jaeger.
// GET /api/services
func (r *JaegerTraceReader) GetServices(ctx context.Context) (*ServicesResult, error) {
	targetURL := fmt.Sprintf("%s/api/services", r.endpoint)
	body, statusCode, err := r.doGet(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	return &ServicesResult{
		StatusCode: statusCode,
		RawBody:    body,
	}, nil
}

// GetOperations returns all operations for the specified service.
// GET /api/services/{service}/operations
func (r *JaegerTraceReader) GetOperations(ctx context.Context, service string) (*OperationsResult, error) {
	targetURL := fmt.Sprintf("%s/api/services/%s/operations", r.endpoint, url.PathEscape(service))
	body, statusCode, err := r.doGet(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	return &OperationsResult{
		StatusCode: statusCode,
		RawBody:    body,
	}, nil
}

// GetDependencies returns service dependency links for the Service Map.
// GET /api/dependencies?endTs=xxx&lookback=xxx
func (r *JaegerTraceReader) GetDependencies(ctx context.Context, endTs time.Time, lookback time.Duration) (*DependenciesResult, error) {
	params := url.Values{}
	params.Set("endTs", fmt.Sprintf("%d", endTs.UnixMilli()))
	params.Set("lookback", fmt.Sprintf("%d", lookback.Milliseconds()))

	targetURL := fmt.Sprintf("%s/api/dependencies?%s", r.endpoint, params.Encode())
	body, statusCode, err := r.doGet(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	return &DependenciesResult{
		StatusCode: statusCode,
		RawBody:    body,
	}, nil
}

// doGet performs an HTTP GET request to the target URL and returns the response body,
// status code, and any error. It enforces the maxResponseSize limit to prevent OOM.
func (r *JaegerTraceReader) doGet(ctx context.Context, targetURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.logger.Warn("Jaeger proxy request failed",
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
		r.logger.Warn("Failed to read Jaeger response",
			zap.String("url", targetURL),
			zap.Error(err),
		)
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	return body, resp.StatusCode, nil
}
