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

func defaultSGConfig() *ServiceGraphConfig {
	return &ServiceGraphConfig{
		Enabled:          true,
		Histogram:        HistogramConfig{Buckets: []float64{0.05, 0.1}},
		MessageSizeHisto: HistogramConfig{Buckets: []float64{128}},
	}
}

func makeSGSpan(name string, kind ptrace.SpanKind, durMs float64, svcName, peerSvc string, attrs map[string]string) (ptrace.Span, pcommon.Resource) {
	span := ptrace.NewSpan()
	span.SetName(name)
	span.SetKind(kind)
	now := pcommon.NewTimestampFromTime(time.Now())
	span.SetStartTimestamp(now)
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.AsTime().Add(time.Duration(durMs) * time.Millisecond)))
	span.Status().SetCode(ptrace.StatusCodeOk)
	if peerSvc != "" {
		span.Attributes().PutStr(attrPeerService, peerSvc)
	}
	for k, v := range attrs {
		span.Attributes().PutStr(k, v)
	}
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", svcName)
	return span, resource
}

func TestServiceGraph_ClientServer(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	cSpan, cRes := makeSGSpan("GET /data", ptrace.SpanKindClient, 50, "tapm-api", "tapm-db", nil)
	gen.ProcessSpan("tapm-api", "test-app", cRes, cSpan)

	sSpan, sRes := makeSGSpan("query", ptrace.SpanKindServer, 30, "tapm-db", "tapm-api", nil)
	gen.ProcessSpan("tapm-db", "test-app", sRes, sSpan)

	edges := gen.Collect()
	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "tapm-api", e.key.client)
	assert.Equal(t, "tapm-db", e.key.server)
	assert.Equal(t, int64(1), e.requestTotal.Load())
	assert.Equal(t, int64(0), e.failedTotal.Load())
}

func TestServiceGraph_ClientOnly(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	cSpan, cRes := makeSGSpan("GET /api", ptrace.SpanKindClient, 25, "tapm-api", "tapm-db", nil)
	gen.ProcessSpan("tapm-api", "test-app", cRes, cSpan)

	edges := gen.Collect()
	require.Len(t, edges, 1)
	assert.Equal(t, int64(0), edges[0].requestTotal.Load())
	b, _, _, c := edges[0].clientSeconds.Snapshot()
	assert.Equal(t, uint64(1), c)
	assert.True(t, b[0] > 0, "should hit first bucket")
}

func TestServiceGraph_ServerOnly(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	sSpan, sRes := makeSGSpan("query", ptrace.SpanKindServer, 30, "tapm-db", "tapm-api", nil)
	gen.ProcessSpan("tapm-db", "test-app", sRes, sSpan)

	edges := gen.Collect()
	require.Len(t, edges, 1)
	assert.Equal(t, int64(1), edges[0].requestTotal.Load())
	b, _, _, c := edges[0].serverSeconds.Snapshot()
	assert.Equal(t, uint64(1), c)
	assert.True(t, b[0] > 0)
}

func TestServiceGraph_Failed(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	sSpan, sRes := makeSGSpan("query", ptrace.SpanKindServer, 10, "tapm-db", "tapm-api", nil)
	sSpan.Status().SetCode(ptrace.StatusCodeError)
	gen.ProcessSpan("tapm-db", "test-app", sRes, sSpan)

	edges := gen.Collect()
	require.Len(t, edges, 1)
	assert.Equal(t, int64(1), edges[0].requestTotal.Load())
	assert.Equal(t, int64(1), edges[0].failedTotal.Load())
}

func TestServiceGraph_Messaging(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	pSpan, pRes := makeSGSpan("publish", ptrace.SpanKindProducer, 5, "tapm-api", "kafka/orders-topic",
		map[string]string{"messaging.system": "kafka"})
	gen.ProcessSpan("tapm-api", "test-app", pRes, pSpan)

	cSpan, cRes := makeSGSpan("process", ptrace.SpanKindConsumer, 20, "tapm-worker", "kafka/orders-topic",
		map[string]string{"messaging.system": "kafka", "messaging.message.body.size": "200"})
	gen.ProcessSpan("tapm-worker", "test-app", cRes, cSpan)

	edges := gen.Collect()
	require.Len(t, edges, 2, "Producer→Kafka and Kafka→Consumer are two edges")

	// Find the Producer edge (server="kafka/orders-topic")
	var producerEdge *sgEdgeSeries
	for _, e := range edges {
		if e.key.server == "kafka/orders-topic" && e.key.connType == connTypeMessaging {
			producerEdge = e
		}
	}
	require.NotNil(t, producerEdge, "should have Order→Kafka edge")
	assert.Equal(t, "tapm-api", producerEdge.key.client)
	assert.Equal(t, int64(1), producerEdge.requestTotal.Load(), "producer counts the request")

	// Find the Consumer edge (server="tapm-worker")
	var consumerEdge *sgEdgeSeries
	for _, e := range edges {
		if e.key.server == "tapm-worker" {
			consumerEdge = e
		}
	}
	require.NotNil(t, consumerEdge, "should have Kafka→Worker edge")
	assert.Equal(t, "kafka/orders-topic", consumerEdge.key.client)
	assert.Equal(t, connTypeMessaging, consumerEdge.key.connType)
	assert.Equal(t, int64(1), consumerEdge.requestTotal.Load(), "consumer counts the request")
}

func TestServiceGraph_NoPeerService(t *testing.T) {
	gen := NewServiceGraphGenerator(defaultSGConfig())

	span, res := makeSGSpan("no-peer", ptrace.SpanKindClient, 10, "tapm-api", "", nil)
	gen.ProcessSpan("tapm-api", "test-app", res, span)

	assert.Equal(t, 0, gen.Cardinality())
	assert.Empty(t, gen.Collect())
}

func TestServiceGraph_Disabled(t *testing.T) {
	cfg := defaultSGConfig()
	cfg.Enabled = false
	gen := NewServiceGraphGenerator(cfg)

	span, res := makeSGSpan("x", ptrace.SpanKindServer, 10, "tapm-api", "tapm-db", nil)
	gen.ProcessSpan("tapm-api", "test-app", res, span)

	assert.Equal(t, 0, gen.Cardinality())
}

func TestInferClientServer(t *testing.T) {
	c, s := inferClientServer("svc-a", "svc-b", ptrace.SpanKindClient)
	assert.Equal(t, "svc-a", c)
	assert.Equal(t, "svc-b", s)

	c, s = inferClientServer("svc-b", "svc-a", ptrace.SpanKindServer)
	assert.Equal(t, "svc-a", c)
	assert.Equal(t, "svc-b", s)

	c, s = inferClientServer("svc-a", "svc-b", ptrace.SpanKindInternal)
	assert.Equal(t, "", c)
	assert.Equal(t, "", s)
}

func TestExtractConnectionType(t *testing.T) {
	span := ptrace.NewSpan()
	res := pcommon.NewResource()

	assert.Equal(t, connTypeUnset, extractConnectionType(span, res))

	span.Attributes().PutStr("messaging.system", "kafka")
	assert.Equal(t, connTypeMessaging, extractConnectionType(span, res))

	span = ptrace.NewSpan()
	span.Attributes().PutStr("db.system", "redis")
	assert.Equal(t, connTypeDatabase, extractConnectionType(span, res))

	span = ptrace.NewSpan()
	span.Attributes().PutStr("rpc.system", "grpc")
	assert.Equal(t, "grpc", extractConnectionType(span, res))
}
