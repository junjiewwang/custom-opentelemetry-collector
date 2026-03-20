// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observability

import "encoding/json"

// ============================================================================
// Trace Query Types
// ============================================================================

// TraceSearchQuery holds the parameters for searching traces.
type TraceSearchQuery struct {
	// RawQuery is the raw URL query string to be forwarded to the backend.
	// This preserves all original query parameters from the frontend request.
	RawQuery string
}

// TraceSearchResult holds the result of a trace search operation.
// Data contains the raw JSON response from the backend to preserve compatibility.
type TraceSearchResult struct {
	Data       json.RawMessage `json:"data"`
	Total      int             `json:"total"`
	Limit      int             `json:"limit"`
	Offset     int             `json:"offset"`
	Errors     []any           `json:"errors"`
	StatusCode int             `json:"-"`
	RawBody    []byte          `json:"-"`
}

// TraceResult holds the result of a single trace query.
type TraceResult struct {
	Data       json.RawMessage `json:"data"`
	Errors     []any           `json:"errors"`
	StatusCode int             `json:"-"`
	RawBody    []byte          `json:"-"`
}

// ServicesResult holds the result of a services list query.
type ServicesResult struct {
	StatusCode int    `json:"-"`
	RawBody    []byte `json:"-"`
}

// OperationsResult holds the result of an operations list query.
type OperationsResult struct {
	StatusCode int    `json:"-"`
	RawBody    []byte `json:"-"`
}

// DependenciesResult holds the result of a dependencies query.
type DependenciesResult struct {
	StatusCode int    `json:"-"`
	RawBody    []byte `json:"-"`
}

// ============================================================================
// Metric Query Types (raw responses for transparent proxying)
// ============================================================================

// MetricQueryResult holds the raw response from a metric backend.
// Since metric queries transparently proxy responses, we keep the raw bytes
// and status code to forward to the client as-is.
type MetricQueryResult struct {
	StatusCode int               `json:"-"`
	RawBody    []byte            `json:"-"`
	Headers    map[string]string `json:"-"`
}
