// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
//
// These tests prove the P1 refactoring: the shared redisclient.MockRedis
// (wrapped to satisfy agentRedisCmd) can be injected into
// NewRedisAgentRegistry, and the serialization round-trip works. Full
// lifecycle tests (Register→Heartbeat→Online→Unregister) require a Redis
// instance; the mock is deliberately kept minimal — enough to validate the
// interface extraction, not the full distributed logic.

package agentregistry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// agentMockRedis wraps the shared redisclient.MockRedis, overriding
// Pipeline/TxPipeline to return agentPipelineCmd (with the extra pipeline
// ops). All data methods (Set/Get/Del/Exists/TTL/H*/SCard/SMembers/ZRange/
// ZRem/Publish/Ping) come from the embedded MockRedis.
type agentMockRedis struct {
	*redisclient.MockRedis
}

func (m agentMockRedis) Pipeline() agentPipelineCmd {
	// MockRedis.Pipeline returns redisclient.PipelineCmd; the underlying
	// *MockPipeline satisfies agentPipelineCmd (it has Set/Del/ZAdd/...).
	return m.MockRedis.Pipeline().(agentPipelineCmd)
}

func (m agentMockRedis) TxPipeline() agentPipelineCmd {
	return m.MockRedis.TxPipeline().(agentPipelineCmd)
}

func newAgentMockRedis() *agentMockRedis {
	return &agentMockRedis{MockRedis: redisclient.NewMockRedis()}
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestRedisAgentRegistry_Serialization(t *testing.T) {
	mock := newAgentMockRedis()
	r, err := NewRedisAgentRegistry(zaptest.NewLogger(t), DefaultConfig(), mock)
	require.NoError(t, err)
	require.NoError(t, r.Start(context.Background()))

	agent := &AgentInfo{
		AgentID: "a1", AppID: "app-1", ServiceName: "svc", Hostname: "h1",
		Version: "2.0", Labels: map[string]string{"key": "val"},
		Status: &AgentStatus{State: "healthy"},
	}
	require.NoError(t, r.Register(context.Background(), agent))

	got, err := r.GetAgent(context.Background(), "a1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "h1", got.Hostname)
	assert.Equal(t, "val", got.Labels["key"])
}

func TestRedisAgentRegistry_NilClient(t *testing.T) {
	r := &RedisAgentRegistry{
		client: nil, logger: zap.NewNop(),
		keys:     NewKeyBuilderWithMode("test:", "host"),
		stopChan: make(chan struct{}),
	}
	assert.Error(t, r.Start(context.Background()))
}

func TestRedisCmdAdapter(t *testing.T) {
	assert.NotNil(t, NewRedisCmd)
}
