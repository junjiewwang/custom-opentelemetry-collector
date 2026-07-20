// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func newTestSpan(name string, kind ptrace.SpanKind, durationMs float64) (ptrace.Span, pcommon.Resource) {
	span := ptrace.NewSpan()
	span.SetName(name)
	span.SetKind(kind)
	now := pcommon.NewTimestampFromTime(time.Now())
	span.SetStartTimestamp(now)
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(time.Duration(durationMs) * time.Millisecond)))
	span.Status().SetCode(ptrace.StatusCodeOk)

	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "test-service")

	return span, resource
}

func newTestSPANWithAttrs(name string, kind ptrace.SpanKind, durMs float64, attrs map[string]string) (ptrace.Span, pcommon.Resource) {
	span, resource := newTestSpan(name, kind, durMs)
	for k, v := range attrs {
		span.Attributes().PutStr(k, v)
	}
	return span, resource
}

func TestREDGenerator_ProcessSpan(t *testing.T) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{"http.method", "peer.service"},
		Histogram:  HistogramConfig{Buckets: []float64{10, 50, 100}},
	}, 100)

	span, resource := newTestSPANWithAttrs("GET /api", ptrace.SpanKindServer, 25, map[string]string{
		"http.method":  "GET",
		"peer.service": "tapm-db",
	})

	gen.ProcessSpan("test-service", "test-app", resource, span)
	assert.Equal(t, 1, gen.Cardinality())
}

func TestREDGenerator_CollectAndReset(t *testing.T) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{},
		Histogram:  HistogramConfig{Buckets: []float64{10}},
	}, 100)

	span, resource := newTestSpan("test", ptrace.SpanKindServer, 5)
	gen.ProcessSpan("test-service", "test-app", resource, span)
	assert.Equal(t, 1, gen.Cardinality())

	series := gen.Collect()
	require.Len(t, series, 1)

	assert.Equal(t, int64(1), series[0].calls.Load())
	assert.Equal(t, 0, gen.Cardinality(), "collect should reset")
}

func TestREDGenerator_CardinalityLimit(t *testing.T) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{"tag"},
		Histogram:  HistogramConfig{Buckets: []float64{10}},
	}, 2)

	// Create 3 different series.
	for i, tag := range []string{"a", "b", "c"} {
		span, resource := newTestSPANWithAttrs("test", ptrace.SpanKindServer, 5, map[string]string{
			"tag": tag,
		})
		gen.ProcessSpan("test-service", "test-app", resource, span)
		if i == 2 {
			assert.Equal(t, int64(1), gen.Dropped(), "third series should be dropped")
		}
	}

	assert.Equal(t, 2, gen.Cardinality(), "should cap at limit")
}

func TestREDGenerator_Disabled(t *testing.T) {
	gen := NewREDGenerator(&REDConfig{Enabled: false}, 100)

	span, resource := newTestSpan("test", ptrace.SpanKindServer, 5)
	gen.ProcessSpan("test-service", "test-app", resource, span)
	assert.Equal(t, 0, gen.Cardinality())
}

func TestREDGenerator_InternalSpan(t *testing.T) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{},
		Histogram:  HistogramConfig{Buckets: []float64{10}},
	}, 100)

	span, resource := newTestSpan("internal-op", ptrace.SpanKindInternal, 3)
	gen.ProcessSpan("test-service", "test-app", resource, span)

	assert.Equal(t, 1, gen.Cardinality())
	series := gen.Collect()
	require.Len(t, series, 1)
	assert.Equal(t, int64(1), series[0].calls.Load())
}

func TestExtractServiceName(t *testing.T) {
	res := pcommon.NewResource()
	assert.Equal(t, "", extractServiceName(res))

	res.Attributes().PutStr("service.name", "my-svc")
	assert.Equal(t, "my-svc", extractServiceName(res))
}

func TestSpanDuration(t *testing.T) {
	span := ptrace.NewSpan()
	now := pcommon.NewTimestampFromTime(time.Now())
	span.SetStartTimestamp(now)
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(50 * time.Millisecond)))

	dur := spanDuration(span)
	assert.InDelta(t, 50.0, dur, 1.0)
}
