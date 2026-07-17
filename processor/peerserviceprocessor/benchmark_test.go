// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package peerserviceprocessor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

const benchKey uint64 = 0x42

// newBenchmarkTraces creates a Traces batch with spansPerBatch spans of the given kind.
func newBenchmarkTraces(spansPerBatch int, kind ptrace.SpanKind) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "bench-svc")
	ss := rs.ScopeSpans().AppendEmpty()

	for i := range spansPerBatch {
		span := ss.Spans().AppendEmpty()
		span.SetName(fmt.Sprintf("span-%d", i))
		span.SetTraceID(traceID(0x42, uint64(i)))
		span.SetSpanID(spanID(uint64(i*100 + 1)))
		if kind == ptrace.SpanKindServer || kind == ptrace.SpanKindConsumer {
			// Use a fixed ParentSpanID so it can match a pre-stored Client
			span.SetParentSpanID(spanID(benchKey))
		}
		span.SetKind(kind)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond)))
	}
	return td
}

func newBenchmarkDBTraces(spansPerBatch int) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "bench-svc")
	ss := rs.ScopeSpans().AppendEmpty()

	for i := range spansPerBatch {
		span := ss.Spans().AppendEmpty()
		span.SetName(fmt.Sprintf("db-%d", i))
		span.SetTraceID(traceID(0xDB, uint64(i)))
		span.SetSpanID(spanID(uint64(i*100 + 1)))
		span.SetKind(ptrace.SpanKindClient)
		span.Attributes().PutStr("db.system", "mysql")
		span.Attributes().PutStr("db.name", "benchdb")
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond)))
	}
	return td
}

// ---------------------------------------------------------------------------
// PeerStore benchmarks
// ---------------------------------------------------------------------------

func BenchmarkPeerStore_TryMatch_Hit(b *testing.B) {
	// Pre-store one Client so every Server immediately matches
	store := NewPeerStore(100000, 10*time.Second, realClock{}, func([]ptrace.Span) {}, nil)
	tdClient := ptrace.NewTraces()
	rs := tdClient.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "client-svc")
	ss := rs.ScopeSpans().AppendEmpty()
	clientSpan := ss.Spans().AppendEmpty()
	clientSpan.SetName("pre-client")
	clientSpan.SetTraceID(traceID(0x42, 0))
	clientSpan.SetSpanID(spanID(benchKey))
	clientSpan.SetKind(ptrace.SpanKindClient)
	clientSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	clientSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond)))
	store.TryMatch(clientSpan, benchKey, "client-svc", roleClient)

	b.ResetTimer()
	var matched int64
	for i := range b.N {
		td := newBenchmarkTraces(1, ptrace.SpanKindServer)
		span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		result := store.TryMatch(span, benchKey, "server-svc", roleServer)
		if result != nil {
			matched++
			// Re-preload a Client for next iteration
			reload := ptrace.NewTraces()
			rr := reload.ResourceSpans().AppendEmpty()
			rr.Resource().Attributes().PutStr("service.name", "client-svc")
			rss := rr.ScopeSpans().AppendEmpty()
			reloadSpan := rss.Spans().AppendEmpty()
			reloadSpan.SetName("reload-client")
			reloadSpan.SetTraceID(traceID(0x42, uint64(i+1)))
			reloadSpan.SetSpanID(spanID(benchKey))
			reloadSpan.SetKind(ptrace.SpanKindClient)
			reloadSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
			reloadSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond)))
			store.TryMatch(reloadSpan, benchKey, "client-svc", roleClient)
		}
		_ = i
	}
	b.ReportMetric(float64(matched), "matched")
}

func BenchmarkPeerStore_TryMatch_Miss(b *testing.B) {
	store := NewPeerStore(100000, 10*time.Second, realClock{}, func([]ptrace.Span) {}, nil)
	var counter uint64 = 10000000 // start far from any real spanID

	b.ResetTimer()
	for b.Loop() {
		counter++
		td := newBenchmarkTraces(1, ptrace.SpanKindClient)
		span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		store.TryMatch(span, counter, "client-svc", roleClient)
	}
	store.Drain()
}

func BenchmarkPeerStore_Expire(b *testing.B) {
	store := NewPeerStore(100000, 10*time.Second, realClock{}, func([]ptrace.Span) {}, nil)
	// Pre-populate with 10k unique keys
	for i := range 10000 {
		td := newBenchmarkTraces(1, ptrace.SpanKindClient)
		span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		store.TryMatch(span, uint64(i+100000), "svc", roleClient)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		store.expire()
	}
}

// ---------------------------------------------------------------------------
// Processor benchmarks
// ---------------------------------------------------------------------------

func BenchmarkProcessor_DBFastPath(b *testing.B) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	ctx := context.Background()
	spansPerBatch := 100

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		td := newBenchmarkDBTraces(spansPerBatch)
		_ = p.ConsumeTraces(ctx, td)
	}
	b.ReportMetric(float64(spansPerBatch*b.N), "spans")
}

func BenchmarkProcessor_ClientOnly_NoMatch(b *testing.B) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	ctx := context.Background()
	spansPerBatch := 100

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		td := newBenchmarkTraces(spansPerBatch, ptrace.SpanKindClient)
		_ = p.ConsumeTraces(ctx, td)
		p.store.Drain() // Drain after each batch to avoid collisions
	}
	b.ReportMetric(float64(spansPerBatch*b.N), "spans")
}

func BenchmarkProcessor_Internal_PassThrough(b *testing.B) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	ctx := context.Background()
	spansPerBatch := 100

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		td := newBenchmarkTraces(spansPerBatch, ptrace.SpanKindInternal)
		_ = p.ConsumeTraces(ctx, td)
	}
	b.ReportMetric(float64(spansPerBatch*b.N), "spans")
}

func BenchmarkProcessor_AllKinds(b *testing.B) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	ctx := context.Background()
	spansPerKind := 20

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		td := ptrace.NewTraces()
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "bench-svc")
		ss := rs.ScopeSpans().AppendEmpty()

		kinds := []ptrace.SpanKind{ptrace.SpanKindInternal, ptrace.SpanKindClient, ptrace.SpanKindServer, ptrace.SpanKindProducer, ptrace.SpanKindConsumer}
		for ki, kind := range kinds {
			for j := range spansPerKind {
				span := ss.Spans().AppendEmpty()
				span.SetName(fmt.Sprintf("s-%d-%d", ki, j))
				span.SetTraceID(traceID(uint64(ki), uint64(j)))
				span.SetSpanID(spanID(uint64(ki*100 + j + 1)))
				span.SetKind(kind)
				if kind == ptrace.SpanKindClient {
					span.Attributes().PutStr("http.method", "GET")
				}
				if kind == ptrace.SpanKindServer || kind == ptrace.SpanKindConsumer {
					span.SetParentSpanID(spanID(uint64((ki-1)*100 + j + 1)))
				}
				span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
				span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond)))
			}
		}
		_ = p.ConsumeTraces(ctx, td)
		p.store.Drain()
	}
}

// ---------------------------------------------------------------------------
// Helper benchmarks
// ---------------------------------------------------------------------------

func BenchmarkIsDBSpan(b *testing.B) {
	td := newBenchmarkDBTraces(1)
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	b.ResetTimer()
	for b.Loop() {
		_ = isDBSpan(span)
	}
}

func BenchmarkSpanIDToUint64(b *testing.B) {
	sid := spanID(0x123456789ABCDEF0)
	b.ResetTimer()
	for b.Loop() {
		_ = spanIDToUint64(sid)
	}
}

func BenchmarkExtractServiceName(b *testing.B) {
	td := newBenchmarkTraces(1, ptrace.SpanKindInternal)
	resource := td.ResourceSpans().At(0).Resource()
	b.ResetTimer()
	for b.Loop() {
		_ = extractServiceName(resource)
	}
}
