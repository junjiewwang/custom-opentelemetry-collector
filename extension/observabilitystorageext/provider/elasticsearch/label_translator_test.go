// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranslateLabelKey(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
	}{
		// OTel standard attributes → all dots replaced with underscores (SanitizeMetricKey)
		{"span_kind", "span_kind", "span_kind"},
		{"span_name", "span_name", "span_name"},
		{"service_name", "service_name", "service_name"},
		{"status_code", "status_code", "status_code"},
		{"peer_service", "peer_service", "peer_service"},
		{"http_method", "http_method", "http_method"},
		{"http_status_code", "http_status_code", "http_status_code"},
		{"rpc_method", "rpc_method", "rpc_method"},
		{"rpc_service", "rpc_service", "rpc_service"},
		{"rpc_system", "rpc_system", "rpc_system"},
		{"rpc_grpc_status_code", "rpc_grpc_status_code", "rpc_grpc_status_code"},
		{"db_system", "db_system", "db_system"},
		{"db_name", "db_name", "db_name"},
		{"net_peer_name", "net_peer_name", "net_peer_name"},
		{"exception_type", "exception_type", "exception_type"},
		{"thread_name", "thread_name", "thread_name"},
		{"code_function", "code_function", "code_function"},
		// Custom labels → pass through unchanged
		{"client", "client", "client"},
		{"server", "server", "server"},
		{"connection_type", "connection_type", "connection_type"},
		{"custom_label", "custom_label", "custom_label"},
		{"my_metric_tag", "my_metric_tag", "my_metric_tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translateLabelKey(tt.input))
		})
	}
}

func TestTranslateLabelValue(t *testing.T) {
	tests := []struct {
		name  string
		esKey string
		value string
		want  string
	}{
		// Span kind → ES short form
		{"span_kind server long", "span.kind", "SPAN_KIND_SERVER", "Server"},
		{"span_kind server short", "span.kind", "Server", "Server"},
		{"span_kind server lowercase", "span.kind", "server", "Server"},
		{"span_kind client long", "span.kind", "SPAN_KIND_CLIENT", "Client"},
		{"span_kind client short", "span.kind", "Client", "Client"},
		{"span_kind internal", "span.kind", "SPAN_KIND_INTERNAL", "Internal"},
		{"span_kind unknown", "span.kind", "xxx", "Unspecified"},
		// Status code → ES short form
		{"status code ok long", "status.code", "STATUS_CODE_OK", "Ok"},
		{"status code ok short", "status.code", "Ok", "Ok"},
		{"status code error long", "status.code", "STATUS_CODE_ERROR", "Error"},
		{"status code error lowercase", "status.code", "error", "Error"},
		// Unknown key → passthrough
		{"custom key", "custom.label", "anything", "anything"},
		{"client value", "client", "test-java-gateway-service", "test-java-gateway-service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translateLabelValue(tt.esKey, tt.value))
		})
	}
}

func TestNormalizeMetricQueryLabels(t *testing.T) {
	t.Run("exact match labels translated", func(t *testing.T) {
		labels, _ := normalizeMetricQueryLabels(
			map[string]string{
				"span_kind":   "SPAN_KIND_SERVER",
				"span_name":   "GET /users",
				"client":      "test-java-gateway-service",
				"server":      "test-java-market-service",
				"status_code": "STATUS_CODE_OK",
			},
			nil,
		)
		// Key translated via SanitizeMetricKey (all dots → underscores)
		assert.Equal(t, "Server", labels["span_kind"])
		assert.Equal(t, "GET /users", labels["span_name"])
		assert.Equal(t, "test-java-gateway-service", labels["client"])
		assert.Equal(t, "test-java-market-service", labels["server"])
		assert.Equal(t, "Ok", labels["status_code"])
		assert.NotContains(t, labels, "span.kind")
	})

	t.Run("regex match labels key-only translated", func(t *testing.T) {
		_, match := normalizeMetricQueryLabels(nil,
			map[string]string{
				"span_kind":  "Server",
				"span_name":  ".*",
				"client":     "test.*",
			},
		)
		assert.Equal(t, "Server", match["span_kind"])
		assert.Equal(t, ".*", match["span_name"])
		assert.Equal(t, "test.*", match["client"])
		assert.NotContains(t, match, "span.kind")
	})

	t.Run("empty inputs", func(t *testing.T) {
		labels, match := normalizeMetricQueryLabels(nil, nil)
		assert.NotNil(t, labels)
		assert.NotNil(t, match)
		assert.Empty(t, labels)
		assert.Empty(t, match)
	})

	t.Run("span.kind value normalized", func(t *testing.T) {
		labels, _ := normalizeMetricQueryLabels(
			map[string]string{"span_kind": "SPAN_KIND_SERVER"},
			nil,
		)
		// "SPAN_KIND_SERVER" should be normalized to "Server" (ES Storage format)
		assert.Equal(t, "Server", labels["span_kind"])
	})
}

func TestResolveLogLabelESField(t *testing.T) {
	r := &LogReader{}

	tests := []struct {
		label string
		want  string
	}{
		// Loki standard labels → severityText
		{"level", FieldLogSeverityText},
		{"detected_level", FieldLogSeverityText},
		// Loki label names with underscore → ES camelCase
		{"service_name", FieldServiceName},
		{"appId", FieldAppID},
		{"severity", FieldLogSeverityText},
		{"traceID", FieldTraceID},
		{"spanID", FieldSpanID},
		// Unknown labels → resource.<label>
		{"custom_label", "resource.custom_label"},
		{"unknown", "resource.unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			assert.Equal(t, tt.want, r.resolveLogLabelESField(tt.label))
		})
	}
}
