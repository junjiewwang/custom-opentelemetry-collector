// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// makeBenchSpan creates a span with the given parameters for benchmarking.
func makeBenchSpan(name string, kind ptrace.SpanKind, svcName, peerSvc string) (ptrace.Span, pcommon.Resource) {
	span := ptrace.NewSpan()
	span.SetName(name)
	span.SetKind(kind)
	now := pcommon.NewTimestampFromTime(time.Now())
	span.SetStartTimestamp(now)
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(10 * time.Millisecond)))
	span.Status().SetCode(ptrace.StatusCodeOk)
	if peerSvc != "" {
		span.Attributes().PutStr("peer.service", peerSvc)
	}
	span.Attributes().PutStr("http.method", "GET")
	span.Attributes().PutStr("http.status_code", "200")

	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", svcName)
	res.Attributes().PutStr("app_id", "test-app")
	return span, res
}

// BenchmarkREDGenerator_SingleSeries: same dimensions, high throughput.
func BenchmarkREDGenerator_SingleSeries(b *testing.B) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{"http.method", "http.status_code"},
		Histogram:  HistogramConfig{Buckets: defaultHistogramBuckets},
	}, 2000)

	span, res := makeBenchSpan("GET /api", ptrace.SpanKindServer, "test-svc", "peer-svc")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gen.ProcessSpan("test-svc", "test-app", res, span)
	}
}

// BenchmarkREDGenerator_UniqueSeries: each span creates a new series.
func BenchmarkREDGenerator_UniqueSeries(b *testing.B) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{"http.method"},
		Histogram:  HistogramConfig{Buckets: defaultHistogramBuckets},
	}, 200000)

	b.ResetTimer()
	for i := 0; i < b.N && i < 200000; i++ {
		span := ptrace.NewSpan()
		span.SetName("GET /api")
		span.SetKind(ptrace.SpanKindServer)
		now := pcommon.NewTimestampFromTime(time.Now())
		span.SetStartTimestamp(now)
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(5 * time.Millisecond)))
		span.Status().SetCode(ptrace.StatusCodeOk)
		span.Attributes().PutStr("http.method", string(rune(i%5)))

		res := pcommon.NewResource()
		res.Attributes().PutStr("service.name", "test-svc")

		gen.ProcessSpan("test-svc", "test-app", res, span)
	}
}

// BenchmarkREDGenerator_Collect: benchmark collect + reset cycle.
func BenchmarkREDGenerator_Collect(b *testing.B) {
	gen := NewREDGenerator(&REDConfig{
		Enabled:    true,
		Dimensions: []string{},
		Histogram:  HistogramConfig{Buckets: defaultHistogramBuckets},
	}, 2000)

	span, res := makeBenchSpan("GET /api", ptrace.SpanKindServer, "test-svc", "")

	// Pre-fill with 1000 spans.
	for j := 0; j < 1000; j++ {
		gen.ProcessSpan("test-svc", "test-app", res, span)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gen.Collect()
		gen.ProcessSpan("test-svc", "test-app", res, span)
	}
}

// BenchmarkServiceGraph_ClientServer: client+server pair.
func BenchmarkServiceGraph_ClientServer(b *testing.B) {
	gen := NewServiceGraphGenerator(defaultSGConfig())
	cSpan, cRes := makeBenchSpan("GET /data", ptrace.SpanKindClient, "tapm-api", "tapm-db")
	sSpan, sRes := makeBenchSpan("query", ptrace.SpanKindServer, "tapm-db", "tapm-api")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gen.ProcessSpan("tapm-api", "test-app", cRes, cSpan)
		gen.ProcessSpan("tapm-db", "test-app", sRes, sSpan)
	}
}

// BenchmarkServiceGraph_Messaging: producer+consumer pair.
func BenchmarkServiceGraph_Messaging(b *testing.B) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	pSpan, pRes := makeBenchSpan("publish", ptrace.SpanKindProducer, "tapm-api", "kafka/orders")
	cSpan, cRes := makeBenchSpan("process", ptrace.SpanKindConsumer, "tapm-worker", "kafka/orders")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gen.ProcessSpan("tapm-api", "test-app", pRes, pSpan)
		gen.ProcessSpan("tapm-worker", "test-app", cRes, cSpan)
	}
}

// BenchmarkConnector_ConsumeTraces: simulate full trace batch.
func BenchmarkConnector_ConsumeTraces(b *testing.B) {
	cfg := CreateDefaultConfig().(*Config)
	cfg.RED.Enabled = true
	cfg.ServiceGraph.Enabled = false

	conn := &metricGenConnector{
		config: cfg,
		redGen: NewREDGenerator(cfg.RED, cfg.CardinalityLimit),
	}

	// Build a traces payload with 100 spans.
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	res := rs.Resource()
	res.Attributes().PutStr("service.name", "test-svc")
	res.Attributes().PutStr("app_id", "test-app")
	ss := rs.ScopeSpans().AppendEmpty()

	for j := 0; j < 100; j++ {
		span := ss.Spans().AppendEmpty()
		span.SetName("GET /api")
		span.SetKind(ptrace.SpanKindServer)
		now := pcommon.NewTimestampFromTime(time.Now())
		span.SetStartTimestamp(now)
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(10 * time.Millisecond)))
		span.Status().SetCode(ptrace.StatusCodeOk)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.ConsumeTraces(nil, td)
	}
}
