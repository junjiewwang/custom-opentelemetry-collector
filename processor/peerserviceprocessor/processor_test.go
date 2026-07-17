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
// Mock clock for controlled time in tests
// ---------------------------------------------------------------------------

type mockClock struct{ t time.Time }

func (m *mockClock) Now() time.Time { return m.t }
func (m *mockClock) Advance(d time.Duration) { m.t = m.t.Add(d) }

func newMockClock() *mockClock {
	return &mockClock{t: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestTraces() ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	rs.ScopeSpans().AppendEmpty()
	return td
}

// addSpan appends a span to td's first ResourceSpans/ScopeSpans and returns it.
func addSpan(td ptrace.Traces, name string, traceID pcommon.TraceID, spanID, parentSpanID pcommon.SpanID, kind ptrace.SpanKind) ptrace.Span {
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty()
	span.SetName(name)
	span.SetTraceID(traceID)
	span.SetSpanID(spanID)
	span.SetParentSpanID(parentSpanID)
	span.SetKind(kind)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(10 * time.Millisecond)))
	return span
}

func spanID(id uint64) pcommon.SpanID { var s pcommon.SpanID; binary.BigEndian.PutUint64(s[:], id); return s }
func traceID(hi, lo uint64) pcommon.TraceID { var t pcommon.TraceID; binary.BigEndian.PutUint64(t[:8], hi); binary.BigEndian.PutUint64(t[8:], lo); return t }
func zeroSpanID() pcommon.SpanID { return pcommon.SpanID{} }

// ---------------------------------------------------------------------------
// Unit tests – PeerStore
// ---------------------------------------------------------------------------

func TestPeerStore_TryMatch_ClientArrivesFirst(t *testing.T) {
	td := newTestTraces()
	clientSpan := addSpan(td, "client-call", traceID(1, 1), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	serverSpan := addSpan(td, "server-handle", traceID(1, 1), spanID(200), spanID(100), ptrace.SpanKindServer)

	var forwarded []ptrace.Span
	store := NewPeerStore(100, 10*time.Second, newMockClock(), func(s []ptrace.Span) {
		forwarded = append(forwarded, s...)
	}, nil)

	// Client arrives first → stored
	key := spanIDToUint64(clientSpan.SpanID())
	result := store.TryMatch(clientSpan, key, "service-a", roleClient)
	assert.Nil(t, result, "client should be stored")
	assert.Equal(t, int64(1), store.Size())

	// Server arrives → paired
	key2 := spanIDToUint64(serverSpan.ParentSpanID())
	result = store.TryMatch(serverSpan, key2, "service-b", roleServer)
	assert.Len(t, result, 2, "both spans should be released")
	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.Matched())

	// Verify peer.service
	v, _ := clientSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "service-b", v.Str())
	v, _ = serverSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "service-a", v.Str())
}

func TestPeerStore_TryMatch_ServerArrivesFirst(t *testing.T) {
	td := newTestTraces()
	clientSpan := addSpan(td, "client-call", traceID(1, 2), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	serverSpan := addSpan(td, "server-handle", traceID(1, 2), spanID(200), spanID(100), ptrace.SpanKindServer)

	store := NewPeerStore(100, 10*time.Second, newMockClock(), func([]ptrace.Span) {}, nil)

	key := spanIDToUint64(serverSpan.ParentSpanID())
	result := store.TryMatch(serverSpan, key, "service-b", roleServer)
	assert.Nil(t, result)
	assert.Equal(t, int64(1), store.Size())

	key2 := spanIDToUint64(clientSpan.SpanID())
	result = store.TryMatch(clientSpan, key2, "service-a", roleClient)
	assert.Len(t, result, 2)
	assert.Equal(t, int64(0), store.Size())

	v, _ := clientSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "service-b", v.Str())
	v, _ = serverSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "service-a", v.Str())
}

func TestPeerStore_Expire_ClientOnly(t *testing.T) {
	clock := newMockClock()
	var forwarded []ptrace.Span
	store := NewPeerStore(100, 10*time.Second, clock, func(s []ptrace.Span) {
		forwarded = append(forwarded, s...)
	}, nil)

	td := newTestTraces()
	clientSpan := addSpan(td, "client-call", traceID(1, 4), spanID(100), zeroSpanID(), ptrace.SpanKindClient)

	key := spanIDToUint64(clientSpan.SpanID())
	store.TryMatch(clientSpan, key, "service-a", roleClient)
	assert.Equal(t, int64(1), store.Size())

	clock.Advance(11 * time.Second)
	store.expire()

	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.ExpiredClient())
	assert.Len(t, forwarded, 1)
	_, ok := clientSpan.Attributes().Get(attrPeerService)
	assert.False(t, ok, "expired span should not have peer.service")
	// But should record the reason
	v, ok := clientSpan.Attributes().Get(attrPeerServiceSource)
	assert.True(t, ok)
	assert.Equal(t, sourceExpired, v.Str())
}

func TestPeerStore_Expire_ServerOnly(t *testing.T) {
	clock := newMockClock()
	var forwarded []ptrace.Span
	store := NewPeerStore(100, 10*time.Second, clock, func(s []ptrace.Span) {
		forwarded = append(forwarded, s...)
	}, nil)

	td := newTestTraces()
	serverSpan := addSpan(td, "server-handle", traceID(1, 5), spanID(200), spanID(100), ptrace.SpanKindServer)

	key := spanIDToUint64(serverSpan.ParentSpanID())
	store.TryMatch(serverSpan, key, "service-b", roleServer)
	assert.Equal(t, int64(1), store.Size())

	clock.Advance(11 * time.Second)
	store.expire()

	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(1), store.ExpiredServer())
	assert.Len(t, forwarded, 1)
	v, _ := forwarded[0].Attributes().Get(attrPeerServiceSource)
	assert.Equal(t, sourceExpired, v.Str())
}

func TestPeerStore_MaxItems_Eviction(t *testing.T) {
	var forwarded []ptrace.Span
	store := NewPeerStore(2, 10*time.Second, newMockClock(), func(s []ptrace.Span) {
		forwarded = append(forwarded, s...)
	}, nil)

	for i := uint64(0); i < 3; i++ {
		td := newTestTraces()
		span := addSpan(td, "call", traceID(1, i), spanID(100+i), zeroSpanID(), ptrace.SpanKindClient)
		store.TryMatch(span, spanIDToUint64(span.SpanID()), "svc", roleClient)
	}

	assert.Equal(t, int64(2), store.Size())
	assert.Equal(t, int64(1), store.Evicted())
	assert.Len(t, forwarded, 1)
	// Evicted span should have source=sourceExpired
	v, ok := forwarded[0].Attributes().Get(attrPeerServiceSource)
	assert.True(t, ok)
	assert.Equal(t, sourceExpired, v.Str())
}

func TestPeerStore_Drain(t *testing.T) {
	store := NewPeerStore(100, 10*time.Second, newMockClock(), func([]ptrace.Span) {}, nil)

	td := newTestTraces()
	clientSpan := addSpan(td, "client", traceID(1, 6), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	store.TryMatch(clientSpan, spanIDToUint64(clientSpan.SpanID()), "svc", roleClient)
	assert.Equal(t, int64(1), store.Size())

	spans := store.Drain()
	assert.Len(t, spans, 1)
	assert.Equal(t, int64(0), store.Size())
	// Drained span should have source=sourceExpired
	v, ok := spans[0].Attributes().Get(attrPeerServiceSource)
	assert.True(t, ok)
	assert.Equal(t, sourceExpired, v.Str())
}

func TestPeerStore_MultipleTraces(t *testing.T) {
	store := NewPeerStore(100, 10*time.Second, newMockClock(), func([]ptrace.Span) {}, nil)

	td1 := newTestTraces()
	c1 := addSpan(td1, "call-1", traceID(1, 10), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	s1 := addSpan(td1, "handle-1", traceID(1, 10), spanID(200), spanID(100), ptrace.SpanKindServer)

	td2 := newTestTraces()
	c2 := addSpan(td2, "call-2", traceID(1, 11), spanID(300), zeroSpanID(), ptrace.SpanKindClient)
	s2 := addSpan(td2, "handle-2", traceID(1, 11), spanID(400), spanID(300), ptrace.SpanKindServer)

	store.TryMatch(c1, spanIDToUint64(c1.SpanID()), "svc-a", roleClient)
	store.TryMatch(c2, spanIDToUint64(c2.SpanID()), "svc-c", roleClient)
	assert.Equal(t, int64(2), store.Size())

	store.TryMatch(s1, spanIDToUint64(s1.ParentSpanID()), "svc-b", roleServer)
	assert.Equal(t, int64(1), store.Size())

	store.TryMatch(s2, spanIDToUint64(s2.ParentSpanID()), "svc-d", roleServer)
	assert.Equal(t, int64(0), store.Size())
	assert.Equal(t, int64(2), store.Matched())
}

// ---------------------------------------------------------------------------
// Unit tests – Fast path (database)
// ---------------------------------------------------------------------------

func TestIsDBSpan(t *testing.T) {
	td := newTestTraces()
	dbSpan := addSpan(td, "db-query", traceID(1, 20), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	dbSpan.Attributes().PutStr("db.system", "mysql")
	dbSpan.Attributes().PutStr("db.name", "mydb")
	assert.True(t, isDBSpan(dbSpan))

	httpSpan := addSpan(td, "http-call", traceID(1, 21), spanID(200), zeroSpanID(), ptrace.SpanKindClient)
	httpSpan.Attributes().PutStr("http.method", "GET")
	assert.False(t, isDBSpan(httpSpan))
}

func TestExtractPeerFromPriority(t *testing.T) {
	td := newTestTraces()
	span := addSpan(td, "db", traceID(1, 22), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	span.Attributes().PutStr("db.system", "postgresql")
	span.Attributes().PutStr("db.name", "orders")
	span.Attributes().PutStr("server.address", "10.0.1.5")

	assert.Equal(t, "orders", extractPeerFromPriority(span, defaultDBPeerPriority))

	td2 := newTestTraces()
	span2 := addSpan(td2, "db", traceID(1, 23), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	span2.Attributes().PutStr("db.system", "redis")
	assert.Equal(t, "redis", extractPeerFromPriority(span2, defaultDBPeerPriority))

	td3 := newTestTraces()
	span3 := addSpan(td3, "unknown", traceID(1, 24), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	assert.Equal(t, "unknown", extractPeerFromPriority(span3, defaultDBPeerPriority))
}

// ---------------------------------------------------------------------------
// Unit tests – Processor integration
// ---------------------------------------------------------------------------

func TestProcessor_Disabled(t *testing.T) {
	cfg := &Config{Enabled: false}
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)
	require.NotNil(t, p)
	assert.Nil(t, p.store)

	td := newTestTraces()
	addSpan(td, "test", traceID(1, 30), spanID(100), zeroSpanID(), ptrace.SpanKindInternal)
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
	span := addSpan(td, "db-query", traceID(1, 32), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	span.Attributes().PutStr("db.system", "mysql")
	span.Attributes().PutStr("db.name", "mydb")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())

	allTraces := sink.AllTraces()
	consumedSpan := allTraces[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	v, _ := consumedSpan.Attributes().Get(attrPeerService)
	assert.Equal(t, "mydb", v.Str())
	v, _ = consumedSpan.Attributes().Get(attrPeerServiceSource)
	assert.Equal(t, sourceDBAttribute, v.Str())
	assert.Equal(t, int64(1), p.fastPathDB.Load())
}

func TestProcessor_ClientServerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Send Client first
	td1 := newTestTraces()
	addSpan(td1, "client-call", traceID(1, 33), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount(), "Client should be stored")

	// Send Server
	td2 := newTestTraces()
	addSpan(td2, "server-handle", traceID(1, 33), spanID(200), spanID(100), ptrace.SpanKindServer)
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
// Sprint 2 – Same batch Client↔Server pairing
// ---------------------------------------------------------------------------

func TestProcessor_SameBatchClientServerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Both Client and Server in the SAME batch
	td := newTestTraces()
	addSpan(td, "client-call", traceID(1, 35), spanID(100), zeroSpanID(), ptrace.SpanKindClient)
	addSpan(td, "server-handle", traceID(1, 35), spanID(200), spanID(100), ptrace.SpanKindServer)

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)

	// Both spans should be forwarded in one batch via handleReadySpans
	assert.Equal(t, 2, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Matched())
	assert.Equal(t, int64(0), p.store.Size())
}

// ---------------------------------------------------------------------------
// Sprint 2 – Root span (empty ParentSpanID) edge cases
// ---------------------------------------------------------------------------

func TestProcessor_RootServerSpan_PassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Root Server span (no parent) – should pass through, not be stored
	td := newTestTraces()
	addSpan(td, "root-server", traceID(1, 36), spanID(100), zeroSpanID(), ptrace.SpanKindServer)

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
	addSpan(td, "root-consumer", traceID(1, 37), spanID(100), zeroSpanID(), ptrace.SpanKindConsumer)

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount(), "root consumer should pass through")
	assert.Equal(t, int64(0), p.store.Size())
}

// ---------------------------------------------------------------------------
// Sprint 2 – Unspecified SpanKind
// ---------------------------------------------------------------------------

func TestProcessor_UnspecifiedSpanKind_PassThrough(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	td := newTestTraces()
	addSpan(td, "unspecified", traceID(1, 38), spanID(100), zeroSpanID(), ptrace.SpanKindUnspecified)

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, 1, sink.SpanCount())
	assert.Equal(t, int64(0), p.store.Size())
}

// ---------------------------------------------------------------------------
// Sprint 2 – Producer/Consumer (via Processor, messaging fast path)
// ---------------------------------------------------------------------------

func TestProcessor_ProducerImmediatePeerService(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Send Producer WITHOUT a Consumer – peer.service should be set immediately
	td := newTestTraces()
	span := addSpan(td, "msg-send", traceID(1, 39), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	span.Attributes().PutStr("messaging.system", "kafka")
	span.Attributes().PutStr("messaging.destination.name", "orders-topic")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	// Producer is stored for Consumer pairing, so NOT forwarded immediately
	assert.Equal(t, 0, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Size())

	// But peer.service should already be on the Producer span
	v, _ := span.Attributes().Get(attrPeerService)
	assert.Equal(t, "orders-topic", v.Str())
	v, _ = span.Attributes().Get(attrPeerServiceSource)
	assert.Equal(t, sourceMessagingAttribute, v.Str())
}

func TestProcessor_ProducerConsumerPairing(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Producer first
	td1 := newTestTraces()
	prodSpan := addSpan(td1, "msg-send", traceID(1, 40), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	prodSpan.Attributes().PutStr("messaging.system", "kafka")
	prodSpan.Attributes().PutStr("messaging.destination.name", "orders-topic")

	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Size())

	// Consumer arrives
	td2 := newTestTraces()
	addSpan(td2, "msg-recv", traceID(1, 40), spanID(200), spanID(100), ptrace.SpanKindConsumer)

	err = p.ConsumeTraces(context.Background(), td2)
	require.NoError(t, err)
	assert.Equal(t, 2, sink.SpanCount(), "both spans should be forwarded")

	// Check Producer peer.service was preserved
	allTraces := sink.AllTraces()
	for _, trace := range allTraces {
		spans := trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
		for i := 0; i < spans.Len(); i++ {
			s := spans.At(i)
			v, ok := s.Attributes().Get(attrPeerService)
			assert.True(t, ok, "all spans should have peer.service")
			assert.NotEmpty(t, v.Str())
		}
	}
}

func TestProcessor_ConsumerArrivesBeforeProducer(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sink := &consumertest.TracesSink{}
	p, _ := newProcessor(processortest.NewNopSettings(), cfg, sink)

	// Consumer first (odd but possible)
	td1 := newTestTraces()
	addSpan(td1, "msg-recv", traceID(1, 41), spanID(200), spanID(100), ptrace.SpanKindConsumer)

	err := p.ConsumeTraces(context.Background(), td1)
	require.NoError(t, err)
	assert.Equal(t, 0, sink.SpanCount())
	assert.Equal(t, int64(1), p.store.Size())

	// Producer arrives later
	td2 := newTestTraces()
	prodSpan := addSpan(td2, "msg-send", traceID(1, 41), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
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

	// Producer stored (no Consumer)
	td := newTestTraces()
	span := addSpan(td, "msg-send", traceID(1, 42), spanID(100), zeroSpanID(), ptrace.SpanKindProducer)
	span.Attributes().PutStr("messaging.system", "kafka")
	span.Attributes().PutStr("messaging.destination.name", "orders-topic")

	err := p.ConsumeTraces(context.Background(), td)
	require.NoError(t, err)
	assert.Equal(t, int64(1), p.store.Size())

	// Check peer.service is set on stored span
	v, _ := span.Attributes().Get(attrPeerService)
	assert.Equal(t, "orders-topic", v.Str())

	// Advance past TTL → expired
	clock.Advance(11 * time.Second)
	p.store.expire()

	// Span should be forwarded even though expired, still carrying peer.service
	assert.Equal(t, 1, sink.SpanCount())
	assert.Equal(t, int64(0), p.store.Size())
	// peer.service should still be the messaging destination
	v, _ = span.Attributes().Get(attrPeerService)
	assert.Equal(t, "orders-topic", v.Str())
	// source should be updated to sourceExpired
	v, _ = span.Attributes().Get(attrPeerServiceSource)
	assert.Equal(t, sourceExpired, v.Str())
}

// ---------------------------------------------------------------------------
// Unit tests – Config
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, 10000, cfg.Store.MaxItems)
	assert.Equal(t, 10*time.Second, cfg.Store.TTL)
	assert.Equal(t, []string{"db.name", "db.system", "server.address"}, cfg.DBPeerPriority)
	assert.Equal(t, []string{"messaging.destination.name", "messaging.destination", "messaging.system"}, cfg.MessagingPeerPriority)
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
// Unit tests – Helpers
// ---------------------------------------------------------------------------

func TestExtractServiceName(t *testing.T) {
	td := newTestTraces()
	td.ResourceSpans().At(0).Resource().Attributes().PutStr("service.name", "my-service")
	assert.Equal(t, "my-service", extractServiceName(td.ResourceSpans().At(0).Resource()))

	td2 := ptrace.NewTraces()
	rs := td2.ResourceSpans().AppendEmpty()
	assert.Equal(t, "unknown_service", extractServiceName(rs.Resource()))
}

func TestSpanIDToUint64_Roundtrip(t *testing.T) {
	sid := spanID(0x123456789ABCDEF0)
	u := spanIDToUint64(sid)
	var sid2 pcommon.SpanID
	binary.BigEndian.PutUint64(sid2[:], u)
	assert.Equal(t, sid, sid2)
}

func TestIsZeroSpanID(t *testing.T) {
	assert.True(t, isZeroSpanID(zeroSpanID()))
	assert.False(t, isZeroSpanID(spanID(1)))
	assert.False(t, isZeroSpanID(spanID(0xFFFFFFFFFFFFFFFF)))
}
