// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

func TestParseAppID_Basic(t *testing.T) {
	r := &UsageReporter{config: &Config{
		Traces:  IndexConfig{IndexPrefix: "otel-traces"},
		Metrics: IndexConfig{IndexPrefix: "otel-metrics"},
		Logs:    IndexConfig{IndexPrefix: "otel-logs"},
	}}

	tests := []struct {
		indexName string
		prefix    string
		wantAppID string
	}{
		// Standard format: {prefix}-{appID}-{date}
		{"otel-traces-myapp-2026.07.01", "otel-traces", "myapp"},
		{"otel-metrics-app001-2026.07.02", "otel-metrics", "app001"},
		{"otel-logs-test-2026.06.30", "otel-logs", "test"},
		// AppID with hyphens
		{"otel-traces-my-app-2026.07.01", "otel-traces", "my-app"},
		{"otel-traces-app-with-hyphens-2026.07.01", "otel-traces", "app-with-hyphens"},
		// No date suffix → return full rest
		{"otel-traces-myapp", "otel-traces", "myapp"},
		// Wrong prefix → empty
		{"other-index-2026.07.01", "otel-traces", ""},
		// Prefix not at start
		{"prefix-otel-traces-app-2026.07.01", "otel-traces", ""},
	}

	for _, tc := range tests {
		t.Run(tc.indexName, func(t *testing.T) {
			got := r.parseAppID(tc.indexName, tc.prefix)
			if got != tc.wantAppID {
				t.Errorf("parseAppID(%q, %q) = %q, want %q", tc.indexName, tc.prefix, got, tc.wantAppID)
			}
		})
	}
}

func TestParseAppID_EmptyPrefix(t *testing.T) {
	r := &UsageReporter{}
	got := r.parseAppID("otel-traces-app-2026.07.01", "")
	if got != "" {
		t.Errorf("expected empty for empty prefix, got %q", got)
	}
}

func TestParseAppID_IndexShorterThanPrefix(t *testing.T) {
	r := &UsageReporter{}
	got := r.parseAppID("abc", "otel-traces")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSignalPrefix(t *testing.T) {
	r := &UsageReporter{config: &Config{
		Traces:  IndexConfig{IndexPrefix: "otel-traces"},
		Metrics: IndexConfig{IndexPrefix: "otel-metrics"},
		Logs:    IndexConfig{IndexPrefix: "otel-logs"},
	}}

	if got := r.signalPrefix(lifecycle.SignalTrace); got != "otel-traces" {
		t.Errorf("unexpected trace prefix: %q", got)
	}
	if got := r.signalPrefix(lifecycle.SignalMetric); got != "otel-metrics" {
		t.Errorf("unexpected metric prefix: %q", got)
	}
	if got := r.signalPrefix(lifecycle.SignalLog); got != "otel-logs" {
		t.Errorf("unexpected log prefix: %q", got)
	}
	if got := r.signalPrefix("unknown"); got != "" {
		t.Errorf("unexpected unknown prefix: %q", got)
	}
}
