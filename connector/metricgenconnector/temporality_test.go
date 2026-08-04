// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestHistogram_CumulativeVsDelta proves the two snapshot methods behave
// differently: Snapshot (delta) resets state, SnapshotCumulative preserves it.
func TestHistogram_CumulativeVsDelta(t *testing.T) {
	h := newHistogram([]float64{1, 5, 10})
	h.Record(0.5)
	h.Record(2)
	h.Record(8)

	// Delta: Snapshot resets.
	b1, _, sum1, count1 := h.Snapshot()
	require.Equal(t, uint64(3), count1)
	assert.Equal(t, []uint64{1, 1, 1}, b1)
	assert.NotZero(t, sum1)

	// After delta snapshot, state is gone.
	_, _, sumAfter, countAfter := h.Snapshot()
	assert.Equal(t, uint64(0), countAfter)
	assert.Zero(t, sumAfter)

	// Cumulative: SnapshotCumulative does NOT reset.
	h2 := newHistogram([]float64{1, 5, 10})
	h2.Record(0.5)
	h2.Record(2)
	h2.Record(8)

	b2, _, sum2, count2 := h2.SnapshotCumulative()
	require.Equal(t, uint64(3), count2)
	assert.Equal(t, []uint64{1, 1, 1}, b2)
	assert.NotZero(t, sum2)

	// State still present after cumulative snapshot.
	_, _, sumStill, countStill := h2.SnapshotCumulative()
	assert.Equal(t, uint64(3), countStill, "cumulative snapshot must not reset state")
	assert.Equal(t, sum2, sumStill)
}

// TestCounter_CumulativeVsDelta proves Load (cumulative) vs Swap (delta).
func TestCounter_CumulativeVsDelta(t *testing.T) {
	var c counter
	c.Add(5)
	c.Add(3)

	// Cumulative: Load does not reset.
	require.Equal(t, int64(8), c.Load())
	require.Equal(t, int64(8), c.Load(), "Load must not reset")

	// Delta: Swap resets.
	require.Equal(t, int64(8), c.Swap())
	require.Equal(t, int64(0), c.Swap(), "Swap must reset to 0")
}

// TestREDGenerator_CumulativePreservesState proves that in cumulative mode,
// CollectCumulative returns the same series across flushes without losing data,
// while delta-mode Collect drains.
func TestREDGenerator_CumulativePreservesState(t *testing.T) {
	g := NewREDGenerator(&REDConfig{Enabled: true, Dimensions: []string{}}, 100)

	// Feed one span (Producer kind so it counts via RED ProcessSpan — RED counts all spans).
	span, res := makeSGSpan("GET", ptrace.SpanKindServer, 10, "svc", "", nil)
	g.ProcessSpan("svc", "app", res, span)

	// Delta mode: Collect drains.
	s1 := g.Collect()
	require.Len(t, s1, 1)
	s2 := g.Collect()
	assert.Empty(t, s2, "delta Collect must drain state")

	// Cumulative mode: CollectCumulative returns series repeatedly.
	g2 := NewREDGenerator(&REDConfig{Enabled: true, Dimensions: []string{}}, 100)
	span2, res2 := makeSGSpan("GET", ptrace.SpanKindServer, 10, "svc", "", nil)
	g2.ProcessSpan("svc", "app", res2, span2)

	g2.cycle.Add(1)
	c1 := g2.CollectCumulative(5)
	require.Len(t, c1, 1)
	require.Equal(t, int64(1), c1[0].calls.Load())

	// Feed another span → cumulative count becomes 2.
	span3, res3 := makeSGSpan("GET", ptrace.SpanKindServer, 10, "svc", "", nil)
	g2.ProcessSpan("svc", "app", res3, span3)
	g2.cycle.Add(1)
	c2 := g2.CollectCumulative(5)
	require.Len(t, c2, 1)
	assert.Equal(t, int64(2), c2[0].calls.Load(), "cumulative must keep accumulating")
}

// TestREDGenerator_CumulativeEvictsStale proves stale series are evicted.
func TestREDGenerator_CumulativeEvictsStale(t *testing.T) {
	g := NewREDGenerator(&REDConfig{Enabled: true, Dimensions: []string{}}, 100)
	span, res := makeSGSpan("GET", ptrace.SpanKindServer, 10, "svc", "", nil)
	g.ProcessSpan("svc", "app", res, span)

	// Advance many cycles without feeding data → series should be evicted.
	for i := 0; i < 10; i++ {
		g.cycle.Add(1)
	}
	c := g.CollectCumulative(5)
	assert.Empty(t, c, "stale series (10 cycles > threshold 5) must be evicted")
}

// TestREDGenerator_CumulativeKeepsActive proves active series survive.
func TestREDGenerator_CumulativeKeepsActive(t *testing.T) {
	g := NewREDGenerator(&REDConfig{Enabled: true, Dimensions: []string{}}, 100)
	span, res := makeSGSpan("GET", ptrace.SpanKindServer, 10, "svc", "", nil)
	g.ProcessSpan("svc", "app", res, span)

	// A few cycles pass but under the threshold.
	for i := 0; i < 3; i++ {
		g.cycle.Add(1)
	}
	c := g.CollectCumulative(5)
	require.Len(t, c, 1, "active series (3 cycles < threshold 5) must survive")
}

// TestServiceGraphGenerator_CumulativePreservesState mirrors the RED test for SG.
func TestServiceGraphGenerator_CumulativePreservesState(t *testing.T) {
	g := NewServiceGraphGenerator(&ServiceGraphConfig{Enabled: true, Dimensions: []string{}})

	// Feed a producer span (has peer.service).
	span, res := makeSGSpan("publish", ptrace.SpanKindProducer, 5, "svc", "peer", nil)
	g.ProcessSpan("svc", "app", res, span)

	// Delta drains.
	d1 := g.Collect()
	require.Len(t, d1, 1)
	d2 := g.Collect()
	assert.Empty(t, d2, "delta Collect must drain")

	// Cumulative preserves.
	g2 := NewServiceGraphGenerator(&ServiceGraphConfig{Enabled: true, Dimensions: []string{}})
	span2, res2 := makeSGSpan("publish", ptrace.SpanKindProducer, 5, "svc", "peer", nil)
	g2.ProcessSpan("svc", "app", res2, span2)
	g2.cycle.Add(1)
	c1 := g2.CollectCumulative(5)
	require.Len(t, c1, 1)
	require.Equal(t, int64(1), c1[0].requestTotal.Load())

	span3, res3 := makeSGSpan("publish", ptrace.SpanKindProducer, 5, "svc", "peer", nil)
	g2.ProcessSpan("svc", "app", res3, span3)
	g2.cycle.Add(1)
	c2 := g2.CollectCumulative(5)
	require.Len(t, c2, 1)
	assert.Equal(t, int64(2), c2[0].requestTotal.Load(), "cumulative must accumulate")
}
