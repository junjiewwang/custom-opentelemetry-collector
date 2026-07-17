// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package peerserviceprocessor

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

// ---------------------------------------------------------------------------
// Mock clock
// ---------------------------------------------------------------------------

type mockClock struct{ t time.Time }

func (m *mockClock) Now() time.Time       { return m.t }
func (m *mockClock) Advance(d time.Duration) { m.t = m.t.Add(d) }
func newMockClock() *mockClock            { return &mockClock{t: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)} }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestTraces() ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	rs.ScopeSpans().AppendEmpty()
	return td
}

func addSpan(td ptrace.Traces, name string, traceID pcommon.TraceID, sid, psid pcommon.SpanID, kind ptrace.SpanKind) ptrace.Span {
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty()
	span.SetName(name)
	span.SetTraceID(traceID)
	span.SetSpanID(sid)
	span.SetParentSpanID(psid)
	span.SetKind(kind)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(10 * time.Millisecond)))
	return span
}

func spanID(id uint64) pcommon.SpanID {
	var s pcommon.SpanID; binary.BigEndian.PutUint64(s[:], id); return s
}
func traceID(hi, lo uint64) pcommon.TraceID {
	var t pcommon.TraceID; binary.BigEndian.PutUint64(t[:8], hi); binary.BigEndian.PutUint64(t[8:], lo); return t
}
func zeroSpanID() pcommon.SpanID { return pcommon.SpanID{} }

// noopReady is a no-op onSpanReady callback.
func noopReady() func([]*SpanHalf) { return func([]*SpanHalf) {} }

// halfForSpan extracts the SpanHalf that wraps the given span from a released slice.
func halfForSpan(halves []*SpanHalf, span ptrace.Span) *SpanHalf {
	for _, h := range halves {
		if h.Span == span {
			return h
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// PeerStore tests
// ---------------------------------------------------------------------------

func TestPeerStore_TryMatch_ClientArrivesFirst(t *testing.T) {
	td := newTestTraces()
	resource := td.ResourceSpans().At(0).Resource()
	clientSpan := addSpan(td, "client", traceID(1, 1), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	serverSpan := addSpan(td, "server", traceID(1, 1), spanID(200), spanID(100), ptrace.SpanKindServer)

	var forwarded []*SpanHalf
	store := NewPeerStore(100, 10*time.Second, newMockClock(), func(h []*SpanHalf) {
		forwarded = append(forwarded, h...)
	}, nil)

	// Client → stored
	result := store.TryMatch(clientSpan, resource, spanIDToUint64(clientSpan.SpanID()), "svc-a", roleClient)
	assert.Nil(t, result)
	assert.Equal(t, int64(1), store.Size())

	// Server → paired
	result = store.TryMatch(serverSpan, resource, spanIDToUint64(serverSpan.ParentSpanID()), "svc-b", roleServer)
	assert.Len(t, result, 2)
	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.Matched())

	v, _ := clientSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "svc-b", v.Str())
	v, _ = serverSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "svc-a", v.Str())
}

func TestPeerStore_TryMatch_ServerArrivesFirst(t *testing.T) {
	td := newTestTraces()
	resource := td.ResourceSpans().At(0).Resource()
	clientSpan := addSpan(td, "client", traceID(1, 2), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	serverSpan := addSpan(td, "server", traceID(1, 2), spanID(200), spanID(100), ptrace.SpanKindServer)

	store := NewPeerStore(100, 10*time.Second, newMockClock(), noopReady(), nil)

	result := store.TryMatch(serverSpan, resource, spanIDToUint64(serverSpan.ParentSpanID()), "svc-b", roleServer)
	assert.Nil(t, result)
	assert.Equal(t, int64(1), store.Size())

	result = store.TryMatch(clientSpan, resource, spanIDToUint64(clientSpan.SpanID()), "svc-a", roleClient)
	assert.Len(t, result, 2)
	assert.Equal(t, int64(0), store.Size())

	v, _ := clientSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "svc-b", v.Str())
	v, _ = serverSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "svc-a", v.Str())
}

func TestPeerStore_Expire_ClientOnly(t *testing.T) {
	clock := newMockClock()
	var forwarded []*SpanHalf
	store := NewPeerStore(100, 10*time.Second, clock, func(h []*SpanHalf) {
		forwarded = append(forwarded, h...)
	}, nil)

	td := newTestTraces()
	resource := td.ResourceSpans().At(0).Resource()
	span := addSpan(td, "client", traceID(1, 4), spanID(100), zeroSpanID(), ptrace.SpanKindClient)

	store.TryMatch(span, resource, spanIDToUint64(span.SpanID()), "svc-a", roleClient)
	assert.Equal(t, int64(1), store.Size())

	clock.Advance(11 * time.Second)
	store.expire()

	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.ExpiredClient())
	assert.Len(t, forwarded, 1)
	assert.Equal(t, span, forwarded[0].Span)
	_, ok := span.Attributes().Get(attrPeerService)
	assert.False(t, ok)
	v, ok := span.Attributes().Get(attrPeerServiceSource)
	assert.True(t, ok)
	assert.Equal(t, sourceExpired, v.Str())
}

func TestPeerStore_Expire_ServerOnly(t *testing.T) {
	clock := newMockClock()
	var forwarded []*SpanHalf
	store := NewPeerStore(100, 10*time.Second, clock, func(h []*SpanHalf) {
		forwarded = append(forwarded, h...)
	}, nil)

	td := newTestTraces()
	resource := td.ResourceSpans().At(0).Resource()
	span := addSpan(td, "server", traceID(1, 5), spanID(200), spanID(100), ptrace.SpanKindServer)

	store.TryMatch(span, resource, spanIDToUint64(span.ParentSpanID()), "svc-b", roleServer)
	assert.Equal(t, int64(1), store.Size())

	clock.Advance(11 * time.Second)
	store.expire()

	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.ExpiredServer())
	assert.Len(t, forwarded, 1)
	assert.Equal(t, sourceExpired, v(t, forwarded[0].Span, attrPeerServiceSource))
}

func TestPeerStore_MaxItems_Eviction(t *testing.T) {
	var forwarded []*SpanHalf
	store := NewPeerStore(2, 10*time.Second, newMockClock(), func(h []*SpanHalf) {
		forwarded = append(forwarded, h...)
	}, nil)

	for i := uint64(0); i < 3; i++ {
		td := newTestTraces()
		resource := td.ResourceSpans().At(0).Resource()
		span := addSpan(td, "call", traceID(1, i), spanID(100+i), zeroSpanID(), ptrace.SpanKindClient)
		store.TryMatch(span, resource, spanIDToUint64(span.SpanID()), "svc", roleClient)
	}

	assert.Equal(t, int64(2), store.Size())
	assert.Equal(t, int64(1), store.Evicted())
	assert.Len(t, forwarded, 1)
	assert.Equal(t, sourceExpired, v(t, forwarded[0].Span, attrPeerServiceSource))
}

func TestPeerStore_Drain(t *testing.T) {
	store := NewPeerStore(100, 10*time.Second, newMockClock(), noopReady(), nil)

	td := newTestTraces()
	resource := td.ResourceSpans().At(0).Resource()
	span := addSpan(td, "client", traceID(1, 6), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	store.TryMatch(span, resource, spanIDToUint64(span.SpanID()), "svc", roleClient)
	assert.Equal(t, int64(1), store.Size())

	halves := store.Drain()
	assert.Len(t, halves, 1)
	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, span, halves[0].Span)
	assert.Equal(t, sourceExpired, v(t, halves[0].Span, attrPeerServiceSource))
}

func TestPeerStore_MultipleTraces(t *testing.T) {
	store := NewPeerStore(100, 10*time.Second, newMockClock(), noopReady(), nil)

	td1 := newTestTraces()
	r1 := td1.ResourceSpans().At(0).Resource()
	c1 := addSpan(td1, "c1", traceID(1, 10), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	s1 := addSpan(td1, "s1", traceID(1, 10), spanID(200), spanID(100), ptrace.SpanKindServer)

	td2 := newTestTraces()
	r2 := td2.ResourceSpans().At(0).Resource()
	c2 := addSpan(td2, "c2", traceID(1, 11), spanID(300), zeroSpanID(), ptrace.SpanKindClient)
	s2 := addSpan(td2, "s2", traceID(1, 11), spanID(400), spanID(300), ptrace.SpanKindServer)

	store.TryMatch(c1, r1, spanIDToUint64(c1.SpanID()), "a", roleClient)
	store.TryMatch(c2, r2, spanIDToUint64(c2.SpanID()), "c", roleClient)
	assert.Equal(t, int64(2), store.Size())

	store.TryMatch(s1, r1, spanIDToUint64(s1.ParentSpanID()), "b", roleServer)
	assert.Equal(t, int64(1), store.Size())

	store.TryMatch(s2, r2, spanIDToUint64(s2.ParentSpanID()), "d", roleServer)
	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(2), store.Matched())
}

// ---------------------------------------------------------------------------
// Fast path tests
// ---------------------------------------------------------------------------

func TestIsDBSpan(t *testing.T) {
	td := newTestTraces()
	dbSpan := addSpan(td, "db", traceID(1, 20), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	dbSpan.Attributes().PutStr("db.system", "mysql")
	assert.True(t, isDBSpan(dbSpan))

	// Old convention: db.type (deprecated, but still in use)
	oldSpan := addSpan(td, "old-db", traceID(1, 20), spanID(101), zeroSpanID(), ptrace.SpanKindClient)
	oldSpan.Attributes().PutStr("db.type", "mysql")
	assert.True(t, isDBSpan(oldSpan))

	httpSpan := addSpan(td, "http", traceID(1, 21), spanID(200), zeroSpanID(), ptrace.SpanKindClient)
	httpSpan.Attributes().PutStr("http.method", "GET")
	assert.False(t, isDBSpan(httpSpan))
}

func TestExtractPeerFromPriority(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	td := newTestTraces()
	s := addSpan(td, "db", traceID(1, 22), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	s.Attributes().PutStr("db.system", "postgresql")
	s.Attributes().PutStr("db.name", "orders")
	assert.Equal(t, "orders", extractPeerFromPriority(s, cfg.DBPeerPriority))
}

// ---------------------------------------------------------------------------
// Processor integration tests
// ---------------------------------------------------------------------------

func TestProcessor_Disabled(t *testing.T) {
	cfg := &Config{Enabled: false}
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	require.NotNil(t, p)
	assert.Nil(t, p.store)

	td := newTestTraces()
	addSpan(td, "s", traceID(1, 30), spanID(100), zeroSpanID(), ptrace.SpanKindInternal)
	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
}

func TestProcessor_InternalSpansPassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "internal", traceID(1, 31), spanID(100), zeroSpanID(), ptrace.SpanKindInternal)
	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
}

func TestProcessor_DBFastPath(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "db", traceID(1, 32), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).
		Attributes().PutStr("db.system", "mysql")
	td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).
		Attributes().PutStr("db.name", "mydb")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())

	consumedSpan := sink.AllTraces()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	assert.Equal(t, "mydb", v(t, consumedSpan, attrPeerService))
	assert.Equal(t, sourceDBAttribute, v(t, consumedSpan, attrPeerServiceSource))
	assert.Equal(t, int64(1), p.fastPathDB.Load())
}

func TestProcessor_ClientServerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Client first
	td1 := newTestTraces()
	addSpan(td1, "client", traceID(1, 33), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount())

	// Server second
	td2 := newTestTraces()
	addSpan(td2, "server", traceID(1, 33), spanID(200), spanID(100), ptrace.SpanKindServer)
	err = p.ConsumeTraces(context.Background(), td2)
	require.NoError(t, err)
	assert.Equal(t, 2, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Matched())
}

func TestProcessor_ContextCancelDoesNotBlockConsume(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "internal", traceID(1, 34), spanID(100), zeroSpanID(), ptrace.SpanKindInternal)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.ConsumeTraces(ctx, td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
}

// ---------------------------------------------------------------------------
// Sprint 2 – same batch / edge cases
// ---------------------------------------------------------------------------

func TestProcessor_SameBatchClientServerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "client", traceID(1, 35), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	addSpan(td, "server", traceID(1, 35), spanID(200), spanID(100), ptrace.SpanKindServer)

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 2, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Matched())
}

func TestProcessor_RootServerSpan_PassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "root", traceID(1, 36), spanID(100), zeroSpanID(), ptrace.SpanKindServer)
	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
	assert.Equal(t, int64(0), p.store.Size())
}

func TestProcessor_RootConsumerSpan_PassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "root", traceID(1, 37), spanID(100), zeroSpanID(), ptrace.SpanKindConsumer)
	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
}

func TestProcessor_UnspecifiedSpanKind_PassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "unspec", traceID(1, 38), spanID(100), zeroSpanID(), ptrace.SpanKindUnspecified)
	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
}

// ---------------------------------------------------------------------------
// Sprint 2 – Producer/Consumer
// ---------------------------------------------------------------------------

func TestProcessor_ProducerImmediatePeerService(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	span := addSpan(td, "msg", traceID(1, 39), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	span.Attributes().PutStr("messaging.system", "kafka")
	span.Attributes().PutStr("messaging.destination.name", "orders-topic")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount(), "producer stored, not forwarded")

	// peer.service is set immediately (before storing)
	assert.Equal(t, "orders-topic", v(t, span, attrPeerService))
	assert.Equal(t, sourceMessagingAttribute, v(t, span, attrPeerServiceSource))
}

func TestProcessor_ProducerConsumerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td1 := newTestTraces()
	prodSpan := addSpan(td1, "msg", traceID(1, 40), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	prodSpan.Attributes().PutStr("messaging.system", "kafka")
	prodSpan.Attributes().PutStr("messaging.destination.name", "orders-topic")
	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount())

	td2 := newTestTraces()
	addSpan(td2, "recv", traceID(1, 40), spanID(200), spanID(100), ptrace.SpanKindConsumer)
	err = p.ConsumeTraces(context.Background(), td2)
	require.NoError(t, err)
	assert.Equal(t, 2, sink.SpanCount())
}

func TestProcessor_ConsumerArrivesBeforeProducer(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td1 := newTestTraces()
	addSpan(td1, "recv", traceID(1, 41), spanID(200), spanID(100), ptrace.SpanKindConsumer)
	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount())

	td2 := newTestTraces()
	prodSpan := addSpan(td2, "msg", traceID(1, 41), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	prodSpan.Attributes().PutStr("messaging.system", "kafka")
	prodSpan.Attributes().PutStr("messaging.destination.name", "orders-topic")
	err = p.ConsumeTraces(context.Background(), td2)
	require.NoError(t, err)
	assert.Equal(t, 2, sink.SpanCount())
}

func TestProcessor_ProducerExpiresWithPeerService(t *testing.T) {
	clock := newMockClock()
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	p.store.clock = clock

	td := newTestTraces()
	span := addSpan(td, "msg", traceID(1, 42), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	span.Attributes().PutStr("messaging.system", "kafka")
	span.Attributes().PutStr("messaging.destination.name", "orders-topic")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, "orders-topic", v(t, span, attrPeerService))

	clock.Advance(11 * time.Second)
	p.store.expire()

	assert.Equal(t, 1, sink.SpanCount())
	assert.Equal(t, "orders-topic", v(t, span, attrPeerService))
	assert.Equal(t, sourceExpired, v(t, span, attrPeerServiceSource))
}

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, 10000, cfg.Store.MaxItems)
	assert.Equal(t, 10*time.Second, cfg.Store.TTL)
	assert.Equal(t, []string{"db.name", "db.instance", "db.system", "db.type", "server.address"}, cfg.DBPeerPriority)
}

func TestConfig_Validate(t *testing.T) {
	assert.NoError(t, createDefaultConfig().(*Config).Validate())
	cfg := createDefaultConfig().(*Config)
	cfg.Store.MaxItems = 0
	assert.Error(t, cfg.Validate())
	cfg2 := createDefaultConfig().(*Config)
	cfg2.Store.TTL = 0
	assert.Error(t, cfg2.Validate())
}

// ---------------------------------------------------------------------------
// Helper tests
// ---------------------------------------------------------------------------

func TestExtractServiceName(t *testing.T) {
	td := newTestTraces()
	td.ResourceSpans().At(0).Resource().Attributes().PutStr("service.name", "my-svc")
	assert.Equal(t, "my-svc", extractServiceName(td.ResourceSpans().At(0).Resource()))

	td2 := ptrace.NewTraces()
	assert.Equal(t, "unknown_service", extractServiceName(td2.ResourceSpans().AppendEmpty().Resource()))
}

func TestSpanIDToUint64_Roundtrip(t *testing.T) {
	sid := spanID(0x123456789ABCDEF0)
	var sid2 pcommon.SpanID
	binary.BigEndian.PutUint64(sid2[:], spanIDToUint64(sid))
	assert.Equal(t, sid, sid2)
}

func TestIsZeroSpanID(t *testing.T) {
	assert.True(t, isZeroSpanID(zeroSpanID()))
	assert.False(t, isZeroSpanID(spanID(1)))
}

// ---------------------------------------------------------------------------
// v() is a test helper to extract an attribute value from a span.
// ---------------------------------------------------------------------------

func v(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	val, ok := span.Attributes().Get(key)
	if !ok {
		return ""
	}
	return val.Str()
}
