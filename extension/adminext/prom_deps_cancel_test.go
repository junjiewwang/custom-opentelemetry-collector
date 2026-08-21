// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractFirstMetricName verifies the best-effort metric extraction used
// to annotate engine-routed spans: self-monitoring traces must answer "which
// metric was this query about" without falling back to log grep.
func TestExtractFirstMetricName(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"nested rate", "sum by (le) (rate(traces_spanmetrics_calls_total[4m0s]))", "traces_spanmetrics_calls_total"},
		{"bare selector", "jvm_memory_used", "jvm_memory_used"},
		{"scalar probe", "1+1", ""},
		{"unparseable", "sum by(", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, extractFirstMetricName(c.query))
		})
	}
}

// blockingQuerier implements storage.Querier with a Select that blocks until
// unblocked or the passed ctx is canceled — reproducing the live failure
// shape where ES slices hang and the engine dies on context cancellation.
type blockingQuerier struct {
	unblock chan struct{}
}

func (b *blockingQuerier) Select(ctx context.Context, _ bool, _ *storage.SelectHints, _ ...*labels.Matcher) storage.SeriesSet {
	select {
	case <-b.unblock:
	case <-ctx.Done():
	}
	return storage.EmptySeriesSet()
}

func (b *blockingQuerier) LabelValues(context.Context, string, *storage.LabelHints, ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (b *blockingQuerier) LabelNames(context.Context, *storage.LabelHints, ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (b *blockingQuerier) Close() error { return nil }

// fakeQueryable returns a fixed querier from Querier().
type fakeQueryable struct {
	querier storage.Querier
}

func (f *fakeQueryable) Querier(int64, int64) (storage.Querier, error) { return f.querier, nil }

// TestEngineCanceledCtx_IsCancellation verifies the core contract the
// fail-fast handler logic depends on: a promql engine whose Select blocks
// until the request context is canceled surfaces res.Err as a context
// cancellation (so isContextCancellation → failReason "canceled" → the
// handler returns 422 instead of re-running the query via the subset parser).
func TestEngineCanceledCtx_IsCancellation(t *testing.T) {
	unblock := make(chan struct{})
	engine := promql.NewEngine(promql.EngineOpts{
		MaxSamples:           1_000_000,
		Timeout:              10 * time.Second,
		LookbackDelta:        5 * time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	q, err := engine.NewRangeQuery(ctx, &fakeQueryable{querier: &blockingQuerier{unblock: unblock}}, nil,
		"rate(some_metric_total[4m])", time.Now().Add(-time.Hour), time.Now(), time.Minute)
	require.NoError(t, err)
	defer q.Close()
	res := q.Exec(ctx)
	close(unblock)

	require.Error(t, res.Err, "engine must surface an error on canceled ctx")
	assert.True(t, isContextCancellation(res.Err),
		"engine error must be recognizable as context cancellation, got: %v", res.Err)
}
