// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// stubTypeReader mocks ListMetricTypes to return a canned type map.
type stubTypeReader struct {
	observabilitystorageext.MetricReader
	types map[string]storedmodel.MetricMeta
}

func (r *stubTypeReader) ListMetricTypes(_ context.Context, _ observabilitystorageext.TimeRange) (map[string]storedmodel.MetricMeta, error) {
	return r.types, nil
}

// TestIsHistogramBaseMetric_DisambiguatesCountSuffix verifies the core fix: a
// histogram base name resolves to true, a gauge name ending in _count resolves
// to false (so it is NOT stripped as a histogram sub-series).
func TestIsHistogramBaseMetric_DisambiguatesCountSuffix(t *testing.T) {
	h := &promHandlers{
		metricReader: &stubTypeReader{types: map[string]storedmodel.MetricMeta{
			// Histogram base name (already underscored in storage).
			"traces_spanmetrics_latency": {Type: "histogram"},
			// Gauge whose full underscored name ends in _count, but whose
			// stripped base (jvm_thread) is NOT a storage name.
			"jvm.thread.count": {Type: "gauge"},
		}},
		logger: zap.NewNop(),
	}
	tr := observabilitystorageext.TimeRange{}

	// The histogram base is stored as a histogram → true.
	assert.True(t, h.isHistogramBaseMetric(context.Background(), "traces_spanmetrics_latency", tr))

	// The stripped gauge base (jvm_thread) is NOT a storage name → false.
	assert.False(t, h.isHistogramBaseMetric(context.Background(), "jvm_thread", tr))

	// A gauge whose stripped base happens to also not exist → false.
	assert.False(t, h.isHistogramBaseMetric(context.Background(), "jvm_class", tr))
}

// TestIsHistogramBaseMetric_NilReader verifies the nil-reader guard returns false
// (never treat an unknown metric as a histogram sub-series).
func TestIsHistogramBaseMetric_NilReader(t *testing.T) {
	h := &promHandlers{metricReader: nil, logger: zap.NewNop()}
	assert.False(t, h.isHistogramBaseMetric(context.Background(), "anything", observabilitystorageext.TimeRange{}))
}
