// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
)

// Tenant filtering can produce an empty span list; the adapter must not panic
// when building a public Trace from it.
func TestBuildTraceFromStoredSpansEmpty(t *testing.T) {
	assert.NotPanics(t, func() {
		trace := buildTraceFromStoredSpans(nil)
		assert.NotNil(t, trace)
		assert.Equal(t, 0, trace.SpanCount)
		assert.Empty(t, trace.Spans)
	})
}

type capturingSearcher struct {
	elasticsearch.Searcher
	lastReq *elasticsearch.SearchRequest
}

func (s *capturingSearcher) Search(_ context.Context, _ string, req *elasticsearch.SearchRequest) (*elasticsearch.SearchResponse, error) {
	s.lastReq = req
	return &elasticsearch.SearchResponse{}, nil
}

// The public query model carries TenantID; the abstraction layer must forward
// it to the backend request regardless of which storage backend is configured.
func TestMetricReaderAdapterPropagatesTenantID(t *testing.T) {
	searcher := &capturingSearcher{}
	adapter := &metricReaderAdapter{
		inner: elasticsearch.NewMetricReader(searcher, &elasticsearch.Config{}, zap.NewNop()),
	}

	_, err := adapter.Query(context.Background(), MetricQuery{
		TenantID:   "tn-a",
		MetricName: "m1",
		Time:       time.Now(),
	})
	require.NoError(t, err)
	require.NotNil(t, searcher.lastReq)
	assert.Equal(t, "tn-a", searcher.lastReq.TenantID)

	_, err = adapter.QueryRange(context.Background(), MetricRangeQuery{
		TenantID:   "tn-b",
		MetricName: "m1",
		TimeRange:  TimeRange{Start: time.Now().Add(-time.Hour), End: time.Now()},
		Step:       time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, searcher.lastReq)
	assert.Equal(t, "tn-b", searcher.lastReq.TenantID)
}
