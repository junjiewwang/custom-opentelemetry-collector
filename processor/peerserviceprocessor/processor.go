// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package peerserviceprocessor

import (
	"container/list"
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// Attribute keys written to spans by this processor.
const (
	attrPeerService       = "peer.service"
	attrPeerServiceSource = "peer.service.source"
)

// Values for peer.service.source attribute.
const (
	sourcePaired              = "paired"
	sourceDBAttribute         = "db_attribute"
	sourceMessagingAttribute  = "messaging_attribute"
	sourceExpired             = "expired"
)

// ---------------------------------------------------------------------------
// Clock interface for testability
// ---------------------------------------------------------------------------

// Clock provides time operations that can be mocked in tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// SpanHalf – minimal span info stored while waiting for a pairing partner
// ---------------------------------------------------------------------------

// SpanHalf holds the minimal information needed from a span while waiting
// for its pairing partner. It keeps a reference to the original span so that
// peer.service can be written back once the pair is complete.
type SpanHalf struct {
	ServiceName string      // service.name extracted from resource attributes
	Span        ptrace.Span // reference to the original span (value type, ref-counted internally)
}

// ---------------------------------------------------------------------------
// HalfEdge – a pairing slot in the store
// ---------------------------------------------------------------------------

// HalfEdge represents one pending pairing. Exactly one of Client or Server
// is non-nil when the first span arrives; the second arrival fills the other
// side, triggers completion, and the entry is removed from the store.
type HalfEdge struct {
	Client   *SpanHalf
	Server   *SpanHalf
	ExpireAt time.Time
	element  *list.Element // reference to the FIFO queue element
}

// ---------------------------------------------------------------------------
// PeerStore – the pairing store (map + FIFO queue)
// ---------------------------------------------------------------------------

// entry is the FIFO queue element payload.
type entry struct {
	key      uint64
	expireAt time.Time
}

// PeerStore manages pending Client↔Server and Producer↔Consumer pairings.
//
// Pairing key = Client's SpanID (for Client↔Server) or
//
//	Producer's SpanID (for Producer↔Consumer).
//
// The Client/Server or Producer/Consumer who arrives first is stored;
// the second arrival triggers completion and both spans are released.
type PeerStore struct {
	mu       sync.Mutex
	items    map[uint64]*HalfEdge // key → pending half-edge
	queue    *list.List           // FIFO expiry queue of *entry
	maxItems int
	ttl      time.Duration
	clock    Clock

	// Callbacks
	onSpanReady func([]ptrace.Span)

	// Metrics (atomic for lock-free read)
	matched       atomic.Int64
	expiredClient atomic.Int64
	expiredServer atomic.Int64
	evicted       atomic.Int64
	storeSize     atomic.Int64

	logger *zap.Logger
}

// NewPeerStore creates a new PeerStore.
func NewPeerStore(maxItems int, ttl time.Duration, clock Clock, onSpanReady func([]ptrace.Span), logger *zap.Logger) *PeerStore {
	if clock == nil {
		clock = realClock{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PeerStore{
		items:       make(map[uint64]*HalfEdge),
		queue:       list.New(),
		maxItems:    maxItems,
		ttl:         ttl,
		clock:       clock,
		onSpanReady: onSpanReady,
		logger:      logger,
	}
}

// Start begins the background expiry goroutine.
func (s *PeerStore) Start(ctx context.Context) {
	go s.expireLoop(ctx)
}

// Drain releases all pending spans with peer.service.source="expired".
// Called during graceful shutdown.
func (s *PeerStore) Drain() []ptrace.Span {
	s.mu.Lock()
	defer s.mu.Unlock()

	var spans []ptrace.Span
	for key, edge := range s.items {
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			spans = append(spans, edge.Client.Span)
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			spans = append(spans, edge.Server.Span)
		}
		delete(s.items, key)
	}
	s.queue.Init()
	s.storeSize.Store(0)
	return spans
}

// Size returns the current number of pending entries.
func (s *PeerStore) Size() int64 {
	return s.storeSize.Load()
}

// Metrics accessors for monitoring.
func (s *PeerStore) Matched() int64    { return s.matched.Load() }
func (s *PeerStore) ExpiredClient() int64 { return s.expiredClient.Load() }
func (s *PeerStore) ExpiredServer() int64 { return s.expiredServer.Load() }
func (s *PeerStore) Evicted() int64    { return s.evicted.Load() }

// isDBSpan checks whether a Client span is a database call.
func isDBSpan(span ptrace.Span) bool {
	_, ok := span.Attributes().Get("db.system")
	return ok
}

// isMessagingSpan checks whether a span is a messaging Producer call.
func isMessagingSpan(span ptrace.Span) bool {
	_, ok := span.Attributes().Get("messaging.system")
	return ok
}

// TryMatch attempts to pair an incoming span with its counterpart.
//
// Parameters:
//   - span:    the incoming span
//   - key:     the pairing key (Client SpanID for Client/Producer,
//     ParentSpanID for Server/Consumer)
//   - svcName: service.name extracted from resource
//   - half:    identifies which half this span fills
//
// Returns nil if the span was stored (waiting), or the completed pair if matched.
// The completed pair has had peer.service written to both spans.
func (s *PeerStore) TryMatch(span ptrace.Span, key uint64, svcName string, half spanRole) []ptrace.Span {
	s.mu.Lock()

	existing, ok := s.items[key]
	if ok {
		// ---- Pair matched! ----
		s.queue.Remove(existing.element)
		delete(s.items, key)
		s.storeSize.Store(int64(len(s.items)))
		s.mu.Unlock()

		s.matched.Add(1)
		return s.completePair(existing, span, svcName, half)
	}

	// ---- Not matched yet, store for later ----
	if len(s.items) >= s.maxItems {
		s.evictOldestLocked()
	}

	edge := &HalfEdge{ExpireAt: s.clock.Now().Add(s.ttl)}
	newHalf := &SpanHalf{ServiceName: svcName, Span: span}

	if half == roleClient {
		edge.Client = newHalf
	} else {
		edge.Server = newHalf
	}

	edge.element = s.queue.PushBack(&entry{key: key, expireAt: edge.ExpireAt})
	s.items[key] = edge
	s.storeSize.Store(int64(len(s.items)))
	s.mu.Unlock()

	return nil
}

// completePair writes peer.service to both spans and returns them for forwarding.
//
// Precondition: Producer spans already have peer.service set from messaging attributes
// before being stored (set by processSpan). This method explicitly skips re-writing
// Producer's peer.service to avoid a redundant extract-and-set cycle.
func (s *PeerStore) completePair(existing *HalfEdge, span ptrace.Span, svcName string, half spanRole) []ptrace.Span {
	var client, server *SpanHalf

	if half == roleClient {
		// Incoming is Client/Producer, existing held the Server/Consumer
		client = &SpanHalf{ServiceName: svcName, Span: span}
		server = existing.Server
	} else {
		// Incoming is Server/Consumer, existing held the Client/Producer
		client = existing.Client
		server = &SpanHalf{ServiceName: svcName, Span: span}
	}

	// Write peer.service to Client span.
	// Producer spans already have peer.service set by processSpan (messaging fast path);
	// non-Producer Client spans need peer.service from the paired Server.
	if !isMessagingSpan(client.Span) {
		client.Span.Attributes().PutStr(attrPeerService, server.ServiceName)
		client.Span.Attributes().PutStr(attrPeerServiceSource, sourcePaired)
	}
	// Producer: peer.service was already set by processSpan with messaging_attribute source,
	// no need to overwrite.

	// Write peer.service to Server/Consumer span
	server.Span.Attributes().PutStr(attrPeerService, client.ServiceName)
	server.Span.Attributes().PutStr(attrPeerServiceSource, sourcePaired)

	return []ptrace.Span{client.Span, server.Span}
}

// evictOldestLocked removes the oldest entry from the store.
// Must be called with s.mu held.
func (s *PeerStore) evictOldestLocked() {
	front := s.queue.Front()
	if front == nil {
		return
	}
	ent := front.Value.(*entry)
	if edge, ok := s.items[ent.key]; ok {
		// Mark source before releasing
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]ptrace.Span{edge.Client.Span})
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]ptrace.Span{edge.Server.Span})
		}
	}
	s.queue.Remove(front)
	delete(s.items, ent.key)
	s.evicted.Add(1)
	s.logger.Warn("peer store full, evicting oldest entry",
		zap.Int("store_size", len(s.items)),
		zap.Int("max_items", s.maxItems),
	)
}

// expireLoop periodically checks for expired entries.
func (s *PeerStore) expireLoop(ctx context.Context) {
	ticker := time.NewTicker(s.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expire()
		}
	}
}

// expire removes entries whose TTL has elapsed.
func (s *PeerStore) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	for s.queue.Len() > 0 {
		front := s.queue.Front()
		ent := front.Value.(*entry)
		if now.Before(ent.expireAt) {
			break
		}

		edge, ok := s.items[ent.key]
		s.queue.Remove(front)
		delete(s.items, ent.key)

		if !ok {
			continue
		}

		// Mark source="expired" before releasing
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]ptrace.Span{edge.Client.Span})
			s.expiredClient.Add(1)
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]ptrace.Span{edge.Server.Span})
			s.expiredServer.Add(1)
		}
	}
	s.storeSize.Store(int64(len(s.items)))
}

// ---------------------------------------------------------------------------
// Span role enum
// ---------------------------------------------------------------------------

type spanRole int

const (
	roleClient spanRole = iota // Client / Producer
	roleServer                 // Server / Consumer
)

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

// spanIDToUint64 converts a SpanID to uint64 for use as a map key.
func spanIDToUint64(id pcommon.SpanID) uint64 {
	return binary.BigEndian.Uint64(id[:])
}

// isZeroSpanID checks if the SpanID is all zeros (root span, no parent).
func isZeroSpanID(id pcommon.SpanID) bool {
	return binary.BigEndian.Uint64(id[:]) == 0
}

// ---------------------------------------------------------------------------
// Peer attribute extraction for fast path and messaging
// ---------------------------------------------------------------------------

var defaultDBPeerPriority = []string{"db.name", "db.system", "server.address"}
var defaultMessagingPriority = []string{"messaging.destination.name", "messaging.destination", "messaging.system"}

// extractPeerFromPriority iterates over attribute names and returns the first
// non-empty value found.
func extractPeerFromPriority(span ptrace.Span, priority []string) string {
	attrs := span.Attributes()
	for _, attr := range priority {
		if v, ok := attrs.Get(attr); ok && v.Str() != "" {
			return v.Str()
		}
	}
	return "unknown"
}

// extractServiceName extracts service.name from resource attributes.
func extractServiceName(resource pcommon.Resource) string {
	if v, ok := resource.Attributes().Get("service.name"); ok {
		return v.Str()
	}
	return "unknown_service"
}

// ---------------------------------------------------------------------------
// Processor
// ---------------------------------------------------------------------------

type peerServiceProcessor struct {
	config       *Config
	nextConsumer consumer.Traces
	store        *PeerStore
	logger       *zap.Logger

	// Metrics
	fastPathDB  atomic.Int64
	storeInsert atomic.Int64
}

func newProcessor(set processor.Settings, cfg *Config, nextConsumer consumer.Traces) (*peerServiceProcessor, error) {
	p := &peerServiceProcessor{
		config:       cfg,
		nextConsumer: nextConsumer,
		logger:       set.Logger,
	}

	if !cfg.Enabled {
		return p, nil
	}

	// Use configured priorities or defaults
	dbPriority := cfg.DBPeerPriority
	if len(dbPriority) == 0 {
		dbPriority = defaultDBPeerPriority
	}
	msgPriority := cfg.MessagingPeerPriority
	if len(msgPriority) == 0 {
		msgPriority = defaultMessagingPriority
	}

	p.store = NewPeerStore(
		cfg.Store.MaxItems,
		cfg.Store.TTL,
		nil, // real clock
		p.handleReadySpans,
		set.Logger,
	)

	return p, nil
}

// handleReadySpans is called by the store when spans are ready for forwarding
// (matched or expired). It builds a new Traces payload and sends to the next consumer.
func (p *peerServiceProcessor) handleReadySpans(spans []ptrace.Span) {
	if len(spans) == 0 {
		return
	}

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()

	for _, span := range spans {
		span.CopyTo(ss.Spans().AppendEmpty())
	}

	// Forward eagerly – these spans were already delayed
	if err := p.nextConsumer.ConsumeTraces(context.Background(), td); err != nil {
		p.logger.Error("failed to forward ready spans", zap.Error(err))
	}
}

// Capabilities implements consumer.Capabilities.
func (p *peerServiceProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// Start implements component.Component.
func (p *peerServiceProcessor) Start(ctx context.Context, _ component.Host) error {
	if p.store != nil {
		p.store.Start(ctx)
	}
	return nil
}

// Shutdown implements component.Component.
func (p *peerServiceProcessor) Shutdown(_ context.Context) error {
	if p.store != nil {
		spans := p.store.Drain()
		p.handleReadySpans(spans)
	}
	return nil
}

// ConsumeTraces implements processor.Traces.
//
// For each span in the batch:
//   - INTERNAL spans: pass through immediately.
//   - CLIENT with db.system: fast path – set peer.service from db attributes, pass through.
//   - CLIENT / SERVER / PRODUCER / CONSUMER: attempt pairing in PeerStore.
//     Stored spans are held and forwarded later via handleReadySpans.
//
// Only spans that are "ready now" (fast-path or no pairing needed) are forwarded
// in the current batch. Stored spans are forwarded asynchronously when paired or expired.
func (p *peerServiceProcessor) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	// If disabled, pass through unchanged
	if !p.config.Enabled || p.store == nil {
		return p.nextConsumer.ConsumeTraces(ctx, td)
	}

	// Build a new Traces payload containing only spans that should be forwarded now.
	// Stored spans are held by the store and will be forwarded later.
	output := ptrace.NewTraces()
	rss := td.ResourceSpans()

	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource()
		serviceName := extractServiceName(resource)

		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)

				ready := p.processSpan(span, serviceName)
				if ready {
					// Fast-path or no pairing needed – add to output
					span.CopyTo(output.ResourceSpans().AppendEmpty().
						ScopeSpans().AppendEmpty().
						Spans().AppendEmpty())
				}
				// else: span was stored in PeerStore, will be forwarded later
			}
		}
	}

	// Forward ready spans
	if output.SpanCount() > 0 {
		return p.nextConsumer.ConsumeTraces(ctx, output)
	}
	return nil
}

// processSpan handles a single span. Returns true if the span should be
// forwarded immediately (ready), false if it was stored for later pairing.
func (p *peerServiceProcessor) processSpan(span ptrace.Span, serviceName string) bool {
	switch span.Kind() {
	case ptrace.SpanKindInternal:
		return true

	case ptrace.SpanKindClient:
		if isDBSpan(span) {
			// Fast path: database call – no Server Span to pair with
			peer := extractPeerFromPriority(span, p.config.DBPeerPriority)
			span.Attributes().PutStr(attrPeerService, peer)
			span.Attributes().PutStr(attrPeerServiceSource, sourceDBAttribute)
			p.fastPathDB.Add(1)
			return true
		}
		// Needs pairing with Server Span
		key := spanIDToUint64(span.SpanID())
		released := p.store.TryMatch(span, key, serviceName, roleClient)
		if released != nil {
			// Matched immediately (partner was already waiting)
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindServer:
		// Root span (no parent) – cannot pair, pass through
		if isZeroSpanID(span.ParentSpanID()) {
			return true
		}
		key := spanIDToUint64(span.ParentSpanID())
		released := p.store.TryMatch(span, key, serviceName, roleServer)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindProducer:
		// Producer: set peer.service from messaging attributes immediately,
		// but still store for Consumer pairing (Consumer needs Producer's serviceName).
		peer := extractPeerFromPriority(span, p.config.MessagingPeerPriority)
		span.Attributes().PutStr(attrPeerService, peer)
		span.Attributes().PutStr(attrPeerServiceSource, sourceMessagingAttribute)
		p.fastPathDB.Add(1)

		key := spanIDToUint64(span.SpanID())
		released := p.store.TryMatch(span, key, serviceName, roleClient)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindConsumer:
		// Root consumer (no parent Producer) – cannot pair, pass through
		if isZeroSpanID(span.ParentSpanID()) {
			return true
		}
		key := spanIDToUint64(span.ParentSpanID())
		released := p.store.TryMatch(span, key, serviceName, roleServer)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	default:
		return true
	}
}
