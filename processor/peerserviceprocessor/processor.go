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

const (
	attrPeerService       = "peer.service"
	attrPeerServiceSource = "peer.service.source"
)

const (
	sourcePaired             = "paired"
	sourceDBAttribute        = "db_attribute"
	sourceMessagingAttribute = "messaging_attribute"
	sourceExpired            = "expired"
)

// ---------------------------------------------------------------------------
// Clock interface for testability
// ---------------------------------------------------------------------------

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// SpanHalf – stored while waiting for a pairing partner
// ---------------------------------------------------------------------------

// SpanHalf retains references to both the span and its resource so that
// resource-level attributes (e.g. app_id from tokenauth) are preserved
// when the span is forwarded after pairing/expiry.
type SpanHalf struct {
	ServiceName string           // service.name
	Resource    pcommon.Resource // preserves app_id etc.
	Span        ptrace.Span
}

// ---------------------------------------------------------------------------
// HalfEdge / PeerStore
// ---------------------------------------------------------------------------

type HalfEdge struct {
	Client, Server *SpanHalf
	ExpireAt       time.Time
	element        *list.Element
}

type entry struct {
	key      uint64
	expireAt time.Time
}

type PeerStore struct {
	mu          sync.Mutex
	items       map[uint64]*HalfEdge
	queue       *list.List
	maxItems    int
	ttl         time.Duration
	clock       Clock
	onSpanReady func([]*SpanHalf)

	matched       atomic.Int64
	expiredClient atomic.Int64
	expiredServer atomic.Int64
	evicted       atomic.Int64
	storeSize     atomic.Int64

	logger *zap.Logger
}

func NewPeerStore(maxItems int, ttl time.Duration, clock Clock, onSpanReady func([]*SpanHalf), logger *zap.Logger) *PeerStore {
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

func (s *PeerStore) Start(ctx context.Context)          { go s.expireLoop(ctx) }
func (s *PeerStore) Size() int64                         { return s.storeSize.Load() }
func (s *PeerStore) Matched() int64                      { return s.matched.Load() }
func (s *PeerStore) ExpiredClient() int64                { return s.expiredClient.Load() }
func (s *PeerStore) ExpiredServer() int64                { return s.expiredServer.Load() }
func (s *PeerStore) Evicted() int64                      { return s.evicted.Load() }

// Drain releases all pending spans with source=expired.
func (s *PeerStore) Drain() []*SpanHalf {
	s.mu.Lock()
	defer s.mu.Unlock()

	var halves []*SpanHalf
	for key, edge := range s.items {
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			halves = append(halves, edge.Client)
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			halves = append(halves, edge.Server)
		}
		delete(s.items, key)
	}
	s.queue.Init()
	s.storeSize.Store(0)
	return halves
}

func isDBSpan(span ptrace.Span) bool {
	// Check both new (db.system) and deprecated (db.type) conventions.
	if _, ok := span.Attributes().Get("db.system"); ok {
		return true
	}
	if _, ok := span.Attributes().Get("db.type"); ok {
		return true
	}
	return false
}

func isMessagingSpan(span ptrace.Span) bool {
	_, ok := span.Attributes().Get("messaging.system")
	return ok
}

// TryMatch attempts to pair an incoming span. resource is stored so that
// downstream components see the original resource attributes (e.g. app_id).
//
// Returns nil if stored; otherwise returns the completed pair.
func (s *PeerStore) TryMatch(span ptrace.Span, resource pcommon.Resource, key uint64, svcName string, half spanRole) []*SpanHalf {
	s.mu.Lock()

	existing, ok := s.items[key]
	if ok {
		s.queue.Remove(existing.element)
		delete(s.items, key)
		s.storeSize.Store(int64(len(s.items)))
		s.mu.Unlock()

		s.matched.Add(1)
		return s.completePair(existing, span, resource, svcName, half)
	}

	if len(s.items) >= s.maxItems {
		s.evictOldestLocked()
	}

	edge := &HalfEdge{ExpireAt: s.clock.Now().Add(s.ttl)}
	newHalf := &SpanHalf{ServiceName: svcName, Resource: resource, Span: span}
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

func (s *PeerStore) completePair(existing *HalfEdge, span ptrace.Span, resource pcommon.Resource, svcName string, half spanRole) []*SpanHalf {
	var client, server *SpanHalf

	if half == roleClient {
		client = &SpanHalf{ServiceName: svcName, Resource: resource, Span: span}
		server = existing.Server
	} else {
		client = existing.Client
		server = &SpanHalf{ServiceName: svcName, Resource: resource, Span: span}
	}

	if !isMessagingSpan(client.Span) {
		client.Span.Attributes().PutStr(attrPeerService, server.ServiceName)
		client.Span.Attributes().PutStr(attrPeerServiceSource, sourcePaired)
	}
	server.Span.Attributes().PutStr(attrPeerService, client.ServiceName)
	server.Span.Attributes().PutStr(attrPeerServiceSource, sourcePaired)

	return []*SpanHalf{client, server}
}

func (s *PeerStore) evictOldestLocked() {
	front := s.queue.Front()
	if front == nil {
		return
	}
	ent := front.Value.(*entry)
	if edge, ok := s.items[ent.key]; ok {
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]*SpanHalf{edge.Client})
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]*SpanHalf{edge.Server})
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
		if edge.Client != nil {
			edge.Client.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]*SpanHalf{edge.Client})
			s.expiredClient.Add(1)
		}
		if edge.Server != nil {
			edge.Server.Span.Attributes().PutStr(attrPeerServiceSource, sourceExpired)
			s.onSpanReady([]*SpanHalf{edge.Server})
			s.expiredServer.Add(1)
		}
	}
	s.storeSize.Store(int64(len(s.items)))
}

// ---------------------------------------------------------------------------
// Span role / helpers
// ---------------------------------------------------------------------------

type spanRole int

const (
	roleClient spanRole = iota
	roleServer
)

func spanIDToUint64(id pcommon.SpanID) uint64 { return binary.BigEndian.Uint64(id[:]) }
func isZeroSpanID(id pcommon.SpanID) bool      { return binary.BigEndian.Uint64(id[:]) == 0 }

func extractPeerFromPriority(span ptrace.Span, priority []string) string {
	attrs := span.Attributes()
	for _, attr := range priority {
		if v, ok := attrs.Get(attr); ok && v.Str() != "" {
			return v.Str()
		}
	}
	return "unknown"
}

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

	fastPathDB  atomic.Int64
	storeInsert atomic.Int64
}

func newProcessor(set processor.Settings, cfg *Config, nextConsumer consumer.Traces) (*peerServiceProcessor, error) {
	p := &peerServiceProcessor{config: cfg, nextConsumer: nextConsumer, logger: set.Logger}
	if !cfg.Enabled {
		return p, nil
	}
	p.store = NewPeerStore(cfg.Store.MaxItems, cfg.Store.TTL, nil, p.handleReadySpans, set.Logger)
	return p, nil
}

// handleReadySpans forwards released spans grouped by resource to preserve
// resource-level attributes (e.g. app_id from tokenauth).
func (p *peerServiceProcessor) handleReadySpans(halves []*SpanHalf) {
	if len(halves) == 0 {
		return
	}
	td := p.buildTraces(halves)
	if td.SpanCount() == 0 {
		return
	}
	if err := p.nextConsumer.ConsumeTraces(context.Background(), td); err != nil {
		p.logger.Error("failed to forward ready spans", zap.Error(err))
	}
}

// buildTraces groups SpanHalf entries by their original resource and
// constructs a ptrace.Traces with proper ResourceSpans (preserving app_id etc.).
func (p *peerServiceProcessor) buildTraces(halves []*SpanHalf) ptrace.Traces {
	td := ptrace.NewTraces()

	type resKey struct{ svc, appID string }
	resourceIndex := make(map[resKey]int)

	for _, h := range halves {
		appID := ""
		if v, ok := h.Resource.Attributes().Get("app_id"); ok {
			appID = v.Str()
		}
		rk := resKey{svc: h.ServiceName, appID: appID}

		idx, ok := resourceIndex[rk]
		if !ok {
			idx = td.ResourceSpans().Len()
			rms := td.ResourceSpans().AppendEmpty()
			h.Resource.CopyTo(rms.Resource())
			rms.ScopeSpans().AppendEmpty()
			resourceIndex[rk] = idx
		}
		h.Span.CopyTo(td.ResourceSpans().At(idx).ScopeSpans().At(0).Spans().AppendEmpty())
	}
	return td
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
		p.handleReadySpans(p.store.Drain())
	}
	return nil
}

// ConsumeTraces implements processor.Traces.
// Spans that are "ready now" are forwarded grouped by their original resource.
// Spans that need pairing are stored and forwarded later via handleReadySpans.
func (p *peerServiceProcessor) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if !p.config.Enabled || p.store == nil {
		return p.nextConsumer.ConsumeTraces(ctx, td)
	}

	// Collect ready spans per resource.
	type readyGroup struct {
		resource pcommon.Resource
		spans    []ptrace.Span
	}
	var groups []readyGroup

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource()
		svcName := extractServiceName(resource)

		var ready []ptrace.Span
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if p.processSpan(span, resource, svcName) {
					ready = append(ready, span)
				}
			}
		}
		if len(ready) > 0 {
			groups = append(groups, readyGroup{resource: resource, spans: ready})
		}
	}

	if len(groups) == 0 {
		return nil
	}

	// Build output with proper resource attributes.
	output := ptrace.NewTraces()
	for _, g := range groups {
		rms := output.ResourceSpans().AppendEmpty()
		g.resource.CopyTo(rms.Resource())
		ss := rms.ScopeSpans().AppendEmpty()
		for _, span := range g.spans {
			span.CopyTo(ss.Spans().AppendEmpty())
		}
	}
	return p.nextConsumer.ConsumeTraces(ctx, output)
}

// processSpan handles a single span. Returns true if it can be forwarded now.
func (p *peerServiceProcessor) processSpan(span ptrace.Span, resource pcommon.Resource, svcName string) bool {
	switch span.Kind() {
	case ptrace.SpanKindInternal:
		return true

	case ptrace.SpanKindClient:
		if isDBSpan(span) {
			peer := extractPeerFromPriority(span, p.config.DBPeerPriority)
			span.Attributes().PutStr(attrPeerService, peer)
			span.Attributes().PutStr(attrPeerServiceSource, sourceDBAttribute)
			p.fastPathDB.Add(1)
			return true
		}
		key := spanIDToUint64(span.SpanID())
		released := p.store.TryMatch(span, resource, key, svcName, roleClient)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindServer:
		if isZeroSpanID(span.ParentSpanID()) {
			return true
		}
		key := spanIDToUint64(span.ParentSpanID())
		released := p.store.TryMatch(span, resource, key, svcName, roleServer)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindProducer:
		peer := extractPeerFromPriority(span, p.config.MessagingPeerPriority)
		span.Attributes().PutStr(attrPeerService, peer)
		span.Attributes().PutStr(attrPeerServiceSource, sourceMessagingAttribute)
		p.fastPathDB.Add(1)

		key := spanIDToUint64(span.SpanID())
		released := p.store.TryMatch(span, resource, key, svcName, roleClient)
		if released != nil {
			p.handleReadySpans(released)
		} else {
			p.storeInsert.Add(1)
		}
		return false

	case ptrace.SpanKindConsumer:
		if isZeroSpanID(span.ParentSpanID()) {
			return true
		}
		key := spanIDToUint64(span.ParentSpanID())
		released := p.store.TryMatch(span, resource, key, svcName, roleServer)
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
