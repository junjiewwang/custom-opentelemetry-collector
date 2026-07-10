// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation scope for adminext traces.
const tracerName = "go.opentelemetry.io/collector/custom/extension/adminext"

// maxBodyBytes is the maximum body size to record in trace span attributes.
// Bodies larger than this are truncated to avoid bloating spans.
const maxBodyBytes = 65536 // 64KB

// tracingMiddleware creates OpenTelemetry spans for every HTTP request.
// Spans capture:
//   - HTTP metadata (method, path, status, remote IP)
//   - Full request body (truncated at 64KB, only for text-like Content-Types)
//   - Full query string
//   - Trace context propagation via W3C TraceContext
//
// Downstream handlers can enrich spans with domain-specific attributes
// (e.g., PromQL expressions) via: trace.SpanFromContext(r.Context()).
func (e *Extension) tracingMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer(tracerName)
	propagator := otel.GetTextMapPropagator()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract incoming trace context (W3C TraceContext / B3 / etc.)
		ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Build span attributes
		attrs := []attribute.KeyValue{
			attribute.String("http.method", r.Method),
			attribute.String("http.url", r.URL.String()),
			attribute.String("http.target", r.URL.Path),
			attribute.String("net.peer.ip", r.RemoteAddr),
			attribute.String("net.host.name", r.Host),
			attribute.String("http.scheme", r.URL.Scheme),
			attribute.String("http.user_agent", truncate(r.UserAgent(), 256)),
		}

		// Record query string (separate from body for debugging)
		if r.URL.RawQuery != "" {
			attrs = append(attrs,
				attribute.String("http.query_string", truncate(r.URL.RawQuery, 4096)),
			)
		}

		// Record request body (supports POST form data, JSON, etc.)
		bodyAttr, bodyReader := captureRequestBody(r)
		if bodyAttr.Valid() {
			attrs = append(attrs, bodyAttr)
		}
		r.Body = bodyReader // restore body for downstream handlers

		// Record Grafana-specific headers for debugging
		if grafanaUA := r.Header.Get("X-Grafana-Org-Id"); grafanaUA != "" {
			attrs = append(attrs, attribute.String("grafana.org_id", grafanaUA))
		}

		// Start span
		spanName := r.Method + " " + r.URL.Path
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		// Attach span to context for downstream handlers
		r = r.WithContext(ctx)

		// Capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		// Record response status
		span.SetAttributes(
			attribute.Int("http.status_code", wrapped.statusCode),
		)
		if wrapped.statusCode >= 400 {
			span.SetStatus(codes.Error, http.StatusText(wrapped.statusCode))
			span.SetAttributes(
				attribute.String("error.type", strconv.Itoa(wrapped.statusCode)),
			)
		}
	})
}

// captureRequestBody reads the request body and returns it as a span attribute.
// The body is restored so downstream handlers can still read it.
// Only reads bodies up to maxBodyBytes; binary content is skipped.
func captureRequestBody(r *http.Request) (attribute.KeyValue, io.ReadCloser) {
	if r.Body == nil || r.ContentLength == 0 {
		return attribute.KeyValue{}, r.Body
	}

	// Skip binary content types
	ct := r.Header.Get("Content-Type")
	if ct != "" && !isTextContentType(ct) {
		return attribute.KeyValue{}, r.Body
	}

	// Read up to maxBodyBytes
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil || len(bodyBytes) == 0 {
		_ = r.Body.Close()
		return attribute.KeyValue{}, io.NopCloser(bytes.NewReader(nil))
	}

	// Close original body reader
	_ = r.Body.Close()

	// Create a new reader so downstream handlers can still read the body
	newReader := io.NopCloser(bytes.NewReader(bodyBytes))

	// Truncate attribute value if needed
	bodyStr := string(bodyBytes)
	if len(bodyStr) > maxBodyBytes {
		bodyStr = bodyStr[:maxBodyBytes] + "...[truncated]"
	}

	return attribute.String("http.request_body", bodyStr), newReader
}

// isTextContentType returns true for content types that contain human-readable text.
func isTextContentType(ct string) bool {
	// Strip parameters (e.g., "application/json; charset=utf-8")
	if idx := strings.IndexByte(ct, ';'); idx > 0 {
		ct = ct[:idx]
	}
	ct = strings.TrimSpace(ct)
	switch ct {
	case "application/json",
		"application/x-www-form-urlencoded",
		"text/plain",
		"text/html",
		"application/xml",
		"text/xml",
		"application/graphql":
		return true
	default:
		// Also match "text/*" and "application/*+json"
		return strings.HasPrefix(ct, "text/") ||
			strings.HasSuffix(ct, "+json") ||
			strings.HasSuffix(ct, "+xml")
	}
}

// truncate truncates a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// SpanFromContext returns the current span from context, or a no-op span.
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}
