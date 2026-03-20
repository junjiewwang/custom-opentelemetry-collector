// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package observability provides abstraction interfaces and implementations
// for querying observability backends (Trace and Metric).
//
// Design principles:
//   - TraceReader and MetricReader define backend-agnostic query interfaces
//   - JaegerTraceReader implements TraceReader by proxying to Jaeger Query API
//   - PrometheusMetricReader implements MetricReader by proxying to Prometheus HTTP API
//   - Frontend does not access backends directly; all queries go through Admin API auth
package observability

import (
	"context"
	"time"
)

// TraceReader defines the abstraction interface for Trace queries.
// Implementations should handle communication with specific trace backends
// (e.g., Jaeger, Tempo) and return normalized results.
type TraceReader interface {
	// SearchTraces searches for traces matching the given query parameters.
	SearchTraces(ctx context.Context, query TraceSearchQuery) (*TraceSearchResult, error)

	// GetTrace retrieves a single trace by its trace ID.
	GetTrace(ctx context.Context, traceID string) (*TraceResult, error)

	// GetServices returns a list of all available service names.
	GetServices(ctx context.Context) (*ServicesResult, error)

	// GetOperations returns a list of operations for the specified service.
	GetOperations(ctx context.Context, service string) (*OperationsResult, error)

	// GetDependencies returns service dependency links for the Service Map.
	// endTs is the end timestamp, lookback is the duration to look back from endTs.
	GetDependencies(ctx context.Context, endTs time.Time, lookback time.Duration) (*DependenciesResult, error)
}

// MetricReader defines the abstraction interface for Metric queries.
// Implementations should handle communication with specific metric backends
// (e.g., Prometheus, VictoriaMetrics) and return raw JSON responses.
type MetricReader interface {
	// QueryInstant executes a Prometheus-compatible instant query.
	// The rawQuery contains the original URL query string to be forwarded.
	QueryInstant(ctx context.Context, rawQuery string) ([]byte, error)

	// QueryRange executes a Prometheus-compatible range query.
	// The rawQuery contains the original URL query string to be forwarded.
	QueryRange(ctx context.Context, rawQuery string) ([]byte, error)

	// GetLabels returns the list of all label names.
	GetLabels(ctx context.Context) ([]byte, error)

	// GetLabelValues returns the values for a specific label name.
	GetLabelValues(ctx context.Context, labelName string) ([]byte, error)

	// GetSeries queries series metadata.
	// The rawQuery contains the original URL query string to be forwarded.
	GetSeries(ctx context.Context, rawQuery string) ([]byte, error)

	// GetMetadata returns metric metadata.
	// The rawQuery contains the original URL query string to be forwarded.
	GetMetadata(ctx context.Context, rawQuery string) ([]byte, error)
}
