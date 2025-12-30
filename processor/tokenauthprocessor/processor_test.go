// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenauthprocessor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

func TestExtractTokenFromAttributes(t *testing.T) {
	tests := []struct {
		name     string
		attrs    map[string]string
		key      string
		expected string
	}{
		{
			name:     "token found",
			attrs:    map[string]string{"token": "abc123"},
			key:      "token",
			expected: "abc123",
		},
		{
			name:     "token not found",
			attrs:    map[string]string{"other": "value"},
			key:      "token",
			expected: "",
		},
		{
			name:     "custom key",
			attrs:    map[string]string{"auth_token": "xyz789"},
			key:      "auth_token",
			expected: "xyz789",
		},
		{
			name:     "empty attributes",
			attrs:    map[string]string{},
			key:      "token",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := pcommon.NewMap()
			for k, v := range tt.attrs {
				attrs.PutStr(k, v)
			}
			result := extractTokenFromAttributes(attrs, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProcessorPassAction(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "pass",
	}

	// Create processor with pass action
	tracesConsumer := &consumertest.TracesSink{}
	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		tracesConsumer,
		nil,
		nil,
	)
	require.NoError(t, err)

	// Create test traces without token
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Spans().AppendEmpty().SetName("test-span")

	// Process traces - should pass through without validation
	err = p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)

	// Verify traces were passed through
	assert.Equal(t, 1, tracesConsumer.SpanCount())
}

func TestProcessorDropActionWithoutControlPlane(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "drop",
		LogDropped:   true,
	}

	// Create processor without control plane
	tracesConsumer := &consumertest.TracesSink{}
	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		tracesConsumer,
		nil,
		nil,
	)
	require.NoError(t, err)

	// Create test traces with token but no control plane to validate
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("token", "test-token")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Spans().AppendEmpty().SetName("test-span")

	// Process traces - should be dropped (no control plane)
	err = p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)

	// Verify traces were dropped
	assert.Equal(t, 0, tracesConsumer.SpanCount())
}

func TestProcessorDropActionWithoutToken(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "drop",
		LogDropped:   true,
	}

	// Create processor
	tracesConsumer := &consumertest.TracesSink{}
	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		tracesConsumer,
		nil,
		nil,
	)
	require.NoError(t, err)

	// Create test traces without token
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Spans().AppendEmpty().SetName("test-span")

	// Process traces - should be dropped (no token)
	err = p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)

	// Verify traces were dropped
	assert.Equal(t, 0, tracesConsumer.SpanCount())
}

func TestProcessorMetrics(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "pass",
	}

	// Create processor with pass action
	metricsConsumer := &consumertest.MetricsSink{}
	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		nil,
		metricsConsumer,
		nil,
	)
	require.NoError(t, err)

	// Create test metrics
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "test-service")
	sm := rm.ScopeMetrics().AppendEmpty()
	m := sm.Metrics().AppendEmpty()
	m.SetName("test-metric")
	m.SetEmptyGauge().DataPoints().AppendEmpty().SetIntValue(100)

	// Process metrics - should pass through
	err = p.ConsumeMetrics(context.Background(), md)
	require.NoError(t, err)

	// Verify metrics were passed through
	assert.Equal(t, 1, metricsConsumer.DataPointCount())
}

func TestProcessorLogs(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "pass",
	}

	// Create processor with pass action
	logsConsumer := &consumertest.LogsSink{}
	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		nil,
		nil,
		logsConsumer,
	)
	require.NoError(t, err)

	// Create test logs
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "test-service")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.LogRecords().AppendEmpty().Body().SetStr("test log")

	// Process logs - should pass through
	err = p.ConsumeLogs(context.Background(), ld)
	require.NoError(t, err)

	// Verify logs were passed through
	assert.Equal(t, 1, logsConsumer.LogRecordCount())
}

func TestCacheOperations(t *testing.T) {
	cfg := &Config{
		AttributeKey: "token",
		Action:       "drop",
		Cache: CacheConfig{
			Enabled:         true,
			ValidTTL:        300,
			InvalidTTL:      30,
			MaxSize:         10000,
			CleanupInterval: 60,
		},
	}

	p, err := newTokenAuthProcessor(
		processortest.NewNopSettings(),
		cfg,
		&consumertest.TracesSink{},
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, p.cache, "cache should be enabled")

	// Add entry to cache
	p.cache.set("test-token", true, "app-1", "Test App")

	// Verify cache entry
	entry, ok := p.cache.get("test-token")
	assert.True(t, ok)
	assert.True(t, entry.valid)

	// Invalidate token
	p.InvalidateToken("test-token")

	// Verify cache entry removed
	_, ok = p.cache.get("test-token")
	assert.False(t, ok)

	// Add more entries
	p.cache.set("token-1", true, "", "")
	p.cache.set("token-2", false, "", "")

	// Clear cache
	p.ClearCache()

	// Verify all entries removed
	_, _, _, size := p.cache.stats()
	assert.Equal(t, 0, size)
}
