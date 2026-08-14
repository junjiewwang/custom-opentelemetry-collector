// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// truncatingFlatReader is a fake MetricReader whose QueryFlat returns a
// truncated result whenever the requested span exceeds `truncateAbove`. This
// simulates a high-cardinality metric where a wide slice exceeds the
// adaptiveFlatMaxDocs cap (Hits.Total.Relation == "gte"), but a narrow enough
// slice fits. It lets us verify bisectFlatQuery's divide-on-truncation
// behavior without a live ES cluster.
type truncatingFlatReader struct {
	observabilitystorageext.MetricReader
	truncateAbove time.Duration
}

func (r *truncatingFlatReader) QueryFlat(ctx context.Context, query observabilitystorageext.MetricFlatQuery) (*observabilitystorageext.MetricFlatResult, error) {
	span := query.TimeRange.End.Sub(query.TimeRange.Start)
	truncated := span > r.truncateAbove

	// Emit one sample per 15m of span (deterministic, monotonically increasing).
	step := 15 * time.Minute
	var samples []observabilitystorageext.MetricSample
	for t := query.TimeRange.Start; t.Before(query.TimeRange.End); t = t.Add(step) {
		samples = append(samples, observabilitystorageext.MetricSample{
			TimestampMs: t.UnixMilli(),
			Value:       float64(len(samples)),
			Labels:      map[string]string{"service_name": "svc"},
		})
	}

	total := int64(len(samples))
	if truncated {
		// Simulate ES reporting more docs matched than returned.
		total = int64(len(samples))*2 + 1
	}
	return &observabilitystorageext.MetricFlatResult{
		Samples:   samples,
		Total:     total,
		Truncated: truncated,
	}, nil
}

func TestBisectFlatQuery_NotTruncatedReturnsAsIs(t *testing.T) {
	r := &truncatingFlatReader{truncateAbove: 2 * time.Hour}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(1 * time.Hour) // below truncateAbove
	q := observabilitystorageext.MetricFlatQuery{
		AppID:      "app",
		MetricName: "traces_spanmetrics_calls_total",
		TimeRange:  observabilitystorageext.TimeRange{Start: start, End: end},
	}

	res := h.bisectFlatQuery(context.Background(), q, start, end, h.logger)

	assert.False(t, res.truncated, "non-truncated slice must not bisect")
	// 1h span → 4 samples (every 15m), no truncation-driven duplication.
	assert.Len(t, res.samples, 4)
	assert.Equal(t, int64(4), res.total)
}

func TestBisectFlatQuery_TruncatedBisectsAndMerges(t *testing.T) {
	r := &truncatingFlatReader{truncateAbove: time.Hour}
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(4 * time.Hour) // 4h > 1h truncateAbove
	q := observabilitystorageext.MetricFlatQuery{
		AppID:      "app",
		MetricName: "traces_spanmetrics_calls_total",
		TimeRange:  observabilitystorageext.TimeRange{Start: start, End: end},
	}

	res := h.bisectFlatQuery(context.Background(), q, start, end, h.logger)

	// After bisection, every leaf slice is ≤1h (not truncated), so the flag clears.
	assert.False(t, res.truncated, "bisection should clear truncation")

	// Total sample count must equal the full 4h span at 15m granularity = 16,
	// regardless of how many bisection rounds occurred. This is the key
	// correctness property: no series is silently dropped.
	assert.Len(t, res.samples, 16)
}

func TestBisectFlatQuery_TruncatedAtFloorKeepsFlag(t *testing.T) {
	r := &truncatingFlatReader{truncateAbove: 0} // always truncated, even at floor
	h := &promHandlers{metricReader: r, logger: zap.NewNop()}

	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(30 * time.Second) // below floor (1m)
	q := observabilitystorageext.MetricFlatQuery{
		AppID:      "app",
		MetricName: "traces_spanmetrics_calls_total",
		TimeRange:  observabilitystorageext.TimeRange{Start: start, End: end},
	}

	res := h.bisectFlatQuery(context.Background(), q, start, end, h.logger)

	// Cannot split below the floor — must surface the truncation so the caller warns.
	assert.True(t, res.truncated, "floor-clamped truncation must stay flagged")
}

func TestMergeFlatResults_PreservesTruncatedOR(t *testing.T) {
	results := []flatSliceResult{
		{samples: []observabilitystorageext.MetricSample{{TimestampMs: 1}}, total: 1, truncated: false},
		{samples: []observabilitystorageext.MetricSample{{TimestampMs: 2}}, total: 1, truncated: true},
		{samples: []observabilitystorageext.MetricSample{{TimestampMs: 3}}, total: 1, truncated: false},
	}

	merged, err := mergeFlatResults(results, len(results))
	assert.NoError(t, err)
	assert.True(t, merged.Truncated, "any truncated slice must OR-propagate")
	assert.Len(t, merged.Samples, 3)
	assert.Equal(t, int64(3), merged.Total)
}

func TestMergeFlatResults_AllEmptyReturnsError(t *testing.T) {
	merged, err := mergeFlatResults([]flatSliceResult{{}, {}}, 2)
	assert.Error(t, err)
	assert.Nil(t, merged)
}
