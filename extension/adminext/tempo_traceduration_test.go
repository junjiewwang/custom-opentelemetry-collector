// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/adminext/traceql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// fakeTraceDurationReader is a minimal TraceReader fake for testing
// filterByTraceDuration. Only QueryTraceDurations is overridden; the nil
// interface embed panics if other methods are called.
type fakeTraceDurationReader struct {
	observabilitystorageext.TraceReader
	durations map[string]int64
	err       error
}

func (f *fakeTraceDurationReader) QueryTraceDurations(_ context.Context, _ []string, _ observabilitystorageext.TraceQuery) (map[string]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.durations, nil
}

// TestFilterByTraceDuration verifies the traceDuration post-filter logic:
// - traces below TraceMinDuration are filtered out
// - traces above TraceMaxDuration are filtered out
// - traces not in the duration map are kept (can't measure, don't drop)
// - QueryTraceDurations error → return unfiltered (robustness)
func TestFilterByTraceDuration(t *testing.T) {
	summaries := []observabilitystorageext.TraceSummary{
		{TraceID: "trace-short"},   // 100ms
		{TraceID: "trace-medium"},  // 1.5s
		{TraceID: "trace-long"},    // 5s
		{TraceID: "trace-unknown"}, // not in duration map
	}
	durations := map[string]int64{
		"trace-short":  100 * 1_000_000,  // 100ms in nanos
		"trace-medium": 1500 * 1_000_000, // 1.5s
		"trace-long":   5000 * 1_000_000, // 5s
	}

	t.Run("TraceMinDuration filters short traces", func(t *testing.T) {
		h := &tempoHandlers{
			traceReader: &fakeTraceDurationReader{durations: durations},
			logger:      zap.NewNop(),
		}
		plan := &traceql.ExecutionPlan{TraceMinDuration: 1_000_000_000} // 1s
		result := h.filterByTraceDuration(context.Background(), summaries, plan, observabilitystorageext.TraceQuery{})
		// trace-short (100ms < 1s) filtered; trace-medium (1.5s) kept; trace-long (5s) kept; trace-unknown kept.
		assert.Len(t, result, 3)
		ids := traceIDs(result)
		assert.Contains(t, ids, "trace-medium")
		assert.Contains(t, ids, "trace-long")
		assert.Contains(t, ids, "trace-unknown")
		assert.NotContains(t, ids, "trace-short")
	})

	t.Run("TraceMaxDuration filters long traces", func(t *testing.T) {
		h := &tempoHandlers{
			traceReader: &fakeTraceDurationReader{durations: durations},
			logger:      zap.NewNop(),
		}
		plan := &traceql.ExecutionPlan{TraceMaxDuration: 2_000_000_000} // 2s
		result := h.filterByTraceDuration(context.Background(), summaries, plan, observabilitystorageext.TraceQuery{})
		// trace-long (5s > 2s) filtered; others kept.
		assert.Len(t, result, 3)
		ids := traceIDs(result)
		assert.NotContains(t, ids, "trace-long")
	})

	t.Run("Both min and max", func(t *testing.T) {
		h := &tempoHandlers{
			traceReader: &fakeTraceDurationReader{durations: durations},
			logger:      zap.NewNop(),
		}
		plan := &traceql.ExecutionPlan{
			TraceMinDuration: 1_000_000_000, // 1s
			TraceMaxDuration: 2_000_000_000, // 2s
		}
		result := h.filterByTraceDuration(context.Background(), summaries, plan, observabilitystorageext.TraceQuery{})
		// Only trace-medium (1.5s) in [1s, 2s]; trace-unknown kept.
		assert.Len(t, result, 2)
		ids := traceIDs(result)
		assert.Contains(t, ids, "trace-medium")
		assert.Contains(t, ids, "trace-unknown")
	})

	t.Run("Error returns unfiltered", func(t *testing.T) {
		h := &tempoHandlers{
			traceReader: &fakeTraceDurationReader{err: errors.New("ES down")},
			logger:      zap.NewNop(),
		}
		plan := &traceql.ExecutionPlan{TraceMinDuration: 1_000_000_000}
		result := h.filterByTraceDuration(context.Background(), summaries, plan, observabilitystorageext.TraceQuery{})
		assert.Len(t, result, 4) // all returned unfiltered
	})

	t.Run("Empty summaries", func(t *testing.T) {
		h := &tempoHandlers{
			traceReader: &fakeTraceDurationReader{durations: durations},
			logger:      zap.NewNop(),
		}
		plan := &traceql.ExecutionPlan{TraceMinDuration: 1_000_000_000}
		result := h.filterByTraceDuration(context.Background(), nil, plan, observabilitystorageext.TraceQuery{})
		assert.Empty(t, result)
	})
}

func traceIDs(summaries []observabilitystorageext.TraceSummary) []string {
	ids := make([]string, 0, len(summaries))
	for _, s := range summaries {
		ids = append(ids, s.TraceID)
	}
	return ids
}
