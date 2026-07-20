// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// sgEdgeKey is the composite key for a service graph edge.
type sgEdgeKey struct {
	client, server, connType string
	hash                     uint64
}

func newSGEdgeKey(client, server, connType string) sgEdgeKey {
	ds := newDimensionSet(map[string]string{
		"client":          client,
		"server":          server,
		"connection_type": connType,
	})
	return sgEdgeKey{client: client, server: server, connType: connType, hash: ds.hash}
}

// sgEdgeSeries holds the aggregated metrics for one service graph edge.
type sgEdgeSeries struct {
	key            sgEdgeKey
	appID          string
	requestTotal   counter
	failedTotal    counter
	clientSeconds  *histogram
	serverSeconds  *histogram
	msgSeconds     *histogram
	messageSize    *histogram
	overflow       atomic.Bool
}

// ServiceGraphGenerator processes spans and aggregates service graph metrics.
//
// Metrics produced (aligned with Tempo MetricGenerator naming):
//
//	traces_service_graph_request_total          — Counter (per edge)
//	traces_service_graph_request_failed_total   — Counter (per edge)
//	traces_service_graph_request_client_seconds — Histogram
//	traces_service_graph_request_server_seconds — Histogram
//	traces_service_graph_request_messaging_system_seconds — Histogram
//	traces_service_graph_request_message_size_bytes       — Histogram
//
// Counting semantics:
//   - request_total: only from Server/Consumer spans (avoid double-count)
//   - client_seconds: from Client/Producer spans
//   - server_seconds: from Server/Consumer spans (if peer.service present)
//   - messaging_system_seconds: from Producer/Consumer spans
//   - message_size_bytes: from Consumer spans with messaging.message.body.size
type ServiceGraphGenerator struct {
	config        *ServiceGraphConfig
	latencyBounds []float64
	sizeBounds    []float64
	mu            sync.RWMutex
	edges         map[uint64]*sgEdgeSeries
	dropped       atomic.Int64
}

// NewServiceGraphGenerator creates a new ServiceGraphGenerator.
func NewServiceGraphGenerator(config *ServiceGraphConfig) *ServiceGraphGenerator {
	lat := config.Histogram.Buckets
	if len(lat) == 0 {
		lat = DefaultServiceGraphLatencyBuckets()
	}
	sz := config.MessageSizeHisto.Buckets
	if len(sz) == 0 {
		sz = DefaultServiceGraphMessageSizeBuckets()
	}
	return &ServiceGraphGenerator{
		config:        config,
		latencyBounds: lat,
		sizeBounds:    sz,
		edges:         make(map[uint64]*sgEdgeSeries),
	}
}

// ProcessSpan aggregates a single span into service graph metrics.
func (g *ServiceGraphGenerator) ProcessSpan(svcName, appID string, resource pcommon.Resource, span ptrace.Span) {
	if !g.config.Enabled {
		return
	}

	peerSvc := extractPeerService(span.Attributes())
	if peerSvc == "" {
		return
	}

	connType := extractConnectionType(span, resource)

	client, server := inferClientServer(svcName, peerSvc, span.Kind())
	if client == "" || server == "" {
		return
	}

	edge := g.getOrCreateEdge(client, server, connType, appID)
	if edge.overflow.Load() {
		return
	}

	// spanDuration returns milliseconds; SG metrics are in seconds.
	durationSeconds := spanDuration(span) / 1000.0

	switch span.Kind() {
	case ptrace.SpanKindClient:
		// HTTP/gRPC Client: only record latency (Server end counts the request).
		edge.clientSeconds.Record(durationSeconds)

	case ptrace.SpanKindProducer:
		// Messaging Producer: count the request. Producer→Kafka and Kafka→Consumer
		// are distinct edges, so counting both won't double-count.
		edge.requestTotal.Add(1)
		if span.Status().Code() == ptrace.StatusCodeError {
			edge.failedTotal.Add(1)
		}
		edge.msgSeconds.Record(durationSeconds)
		if size, ok := span.Attributes().Get("messaging.message.body.size"); ok {
			edge.messageSize.Record(float64(size.Int()))
		}

	case ptrace.SpanKindServer, ptrace.SpanKindConsumer:
		edge.requestTotal.Add(1)
		if span.Status().Code() == ptrace.StatusCodeError {
			edge.failedTotal.Add(1)
		}
		if isMessaging(svcName, span, resource, connType) {
			edge.msgSeconds.Record(durationSeconds)
		} else {
			edge.serverSeconds.Record(durationSeconds)
		}
		if size, ok := span.Attributes().Get("messaging.message.body.size"); ok {
			edge.messageSize.Record(float64(size.Int()))
		}
	}
}

func (g *ServiceGraphGenerator) getOrCreateEdge(client, server, connType, appID string) *sgEdgeSeries {
	key := newSGEdgeKey(client, server, connType)

	g.mu.RLock()
	e, ok := g.edges[key.hash]
	g.mu.RUnlock()
	if ok && e.key.hash == key.hash && e.appID == appID {
		return e
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if e, ok = g.edges[key.hash]; ok && e.key.hash == key.hash && e.appID == appID {
		return e
	}

	e = &sgEdgeSeries{
		key:           key,
		appID:         appID,
		clientSeconds: newHistogram(g.latencyBounds),
		serverSeconds: newHistogram(g.latencyBounds),
		msgSeconds:    newHistogram(g.latencyBounds),
		messageSize:   newHistogram(g.sizeBounds),
	}
	g.edges[key.hash] = e
	return e
}

// Cardinality returns the current number of active edges.
func (g *ServiceGraphGenerator) Cardinality() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges)
}

// Collect drains all aggregated edges and returns them for metric emission.
func (g *ServiceGraphGenerator) Collect() []*sgEdgeSeries {
	g.mu.Lock()
	old := g.edges
	g.edges = make(map[uint64]*sgEdgeSeries)
	g.mu.Unlock()

	result := make([]*sgEdgeSeries, 0, len(old))
	for _, e := range old {
		if e.overflow.Load() {
			continue
		}
		result = append(result, e)
	}
	return result
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	attrPeerService       = "peer.service"
	attrMessagingSystem   = "messaging.system"
	attrConnectionType    = "connection_type"
	connTypeMessaging     = "messaging_system"
	connTypeDatabase      = "database"
	connTypeUnset         = "unset"
)

// extractPeerService reads the peer.service attribute from span attributes.
func extractPeerService(attrs pcommon.Map) string {
	if v, ok := attrs.Get(attrPeerService); ok && v.Str() != "" {
		return v.Str()
	}
	return ""
}

// inferClientServer determines the client and server roles from span info.
func inferClientServer(svcName, peerSvc string, kind ptrace.SpanKind) (client, server string) {
	switch kind {
	case ptrace.SpanKindClient, ptrace.SpanKindProducer:
		return svcName, peerSvc
	case ptrace.SpanKindServer, ptrace.SpanKindConsumer:
		return peerSvc, svcName
	default:
		return "", ""
	}
}

// extractConnectionType determines the connection type for the edge.
func extractConnectionType(span ptrace.Span, resource pcommon.Resource) string {
	attrs := span.Attributes()

	// Check messaging first.
	if v, ok := attrs.Get(attrMessagingSystem); ok && v.Str() != "" {
		return connTypeMessaging
	}

	// Check database.
	dbs := []string{"db.system", "db.name", "db.redis.database_index"}
	for _, key := range dbs {
		if v, ok := attrs.Get(key); ok && v.Str() != "" {
			return connTypeDatabase
		}
	}
	if v, ok := resource.Attributes().Get("db.system"); ok && v.Str() != "" {
		return connTypeDatabase
	}

	// Check RPC.
	if v, ok := attrs.Get("rpc.system"); ok && v.Str() != "" {
		return v.Str()
	}

	// unset is default for non-messaging/non-database connections.
	return connTypeUnset
}

// isMessaging returns true if the span represents a messaging (Producer/Consumer) interaction.
func isMessaging(_ string, span ptrace.Span, _ pcommon.Resource, connType string) bool {
	if connType == connTypeMessaging {
		return true
	}
	// Direct attribute check as fallback.
	if v, ok := span.Attributes().Get(attrMessagingSystem); ok && v.Str() != "" {
		return true
	}
	return false
}
