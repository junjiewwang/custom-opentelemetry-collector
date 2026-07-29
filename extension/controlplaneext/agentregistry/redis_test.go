// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
//
// These tests prove the P1 refactoring: a mock RedisCmd can be injected
// into NewRedisAgentRegistry, and the serialization round-trip works.
// Full lifecycle tests (Register→Heartbeat→Online→Unregister) require
// a Redis instance; the mock is deliberately kept minimal — enough to
// validate the interface extraction, not the full distributed logic.

package agentregistry

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ── mockRedisCmd (minimal for serialization test) ──────────────────────

type mockRedisCmd struct {
	mu      sync.Mutex
	strings map[string]string
	hashes  map[string]map[string]string
}

func newMockRedisCmd() *mockRedisCmd {
	return &mockRedisCmd{
		strings: make(map[string]string),
		hashes:  make(map[string]map[string]string),
	}
}

func (m *mockRedisCmd) Set(_ context.Context, key string, value interface{}, exp time.Duration) *redis.StatusCmd {
	m.mu.Lock()
	switch v := value.(type) {
	case string:
		m.strings[key] = v
	case []byte:
		m.strings[key] = string(v)
	default:
		m.strings[key] = fmt.Sprintf("%v", v)
	}
	m.mu.Unlock()
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("OK")
	return cmd
}
func (m *mockRedisCmd) Get(_ context.Context, key string) *redis.StringCmd {
	m.mu.Lock()
	v, ok := m.strings[key]
	m.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	if ok {
		cmd.SetVal(v)
	}
	return cmd
}
func (m *mockRedisCmd) Del(_ context.Context, keys ...string) *redis.IntCmd    { return redisIntOK(0) }
func (m *mockRedisCmd) Exists(_ context.Context, keys ...string) *redis.IntCmd { return redisIntOK(0) }
func (m *mockRedisCmd) TTL(_ context.Context, key string) *redis.DurationCmd {
	cmd := redis.NewDurationCmd(context.Background(), 0)
	cmd.SetVal(-2 * time.Second)
	return cmd
}
func (m *mockRedisCmd) HSet(_ context.Context, key string, values ...interface{}) *redis.IntCmd {
	m.mu.Lock()
	if m.hashes[key] == nil {
		m.hashes[key] = make(map[string]string)
	}
	for i := 0; i < len(values)-1; i += 2 {
		m.hashes[key][fmt.Sprint(values[i])] = fmt.Sprint(values[i+1])
	}
	n := int64(len(values) / 2)
	m.mu.Unlock()
	return redisIntOK(n)
}
func (m *mockRedisCmd) HGet(_ context.Context, key, field string) *redis.StringCmd {
	m.mu.Lock()
	v := m.hashes[key][field]
	m.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	if v != "" {
		cmd.SetVal(v)
	}
	return cmd
}
func (m *mockRedisCmd) HGetAll(_ context.Context, key string) *redis.MapStringStringCmd {
	m.mu.Lock()
	out := map[string]string{}
	for k, v := range m.hashes[key] {
		out[k] = v
	}
	m.mu.Unlock()
	cmd := redis.NewMapStringStringCmd(context.Background())
	cmd.SetVal(out)
	return cmd
}
func (m *mockRedisCmd) HDel(_ context.Context, key string, fields ...string) *redis.IntCmd {
	return redisIntOK(0)
}
func (m *mockRedisCmd) SCard(_ context.Context, key string) *redis.IntCmd { return redisIntOK(0) }
func (m *mockRedisCmd) SMembers(_ context.Context, key string) *redis.StringSliceCmd {
	return redisStringSliceOK(nil)
}
func (m *mockRedisCmd) ZRange(_ context.Context, key string, start, stop int64) *redis.StringSliceCmd {
	return redisStringSliceOK(nil)
}
func (m *mockRedisCmd) ZRem(_ context.Context, key string, members ...interface{}) *redis.IntCmd {
	return redisIntOK(0)
}
func (m *mockRedisCmd) Pipeline() PipelineCmd   { return newMockPipeliner(m) }
func (m *mockRedisCmd) TxPipeline() PipelineCmd { return newMockPipeliner(m) }
func (m *mockRedisCmd) Publish(_ context.Context, ch string, msg interface{}) *redis.IntCmd {
	return redisIntOK(1)
}
func (m *mockRedisCmd) Ping(_ context.Context) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("PONG")
	return cmd
}

// ── mockPipeliner (records & executes against mock) ────────────────────

type mockPipeliner struct {
	m    *mockRedisCmd
	cmds []redis.Cmder
}

func newMockPipeliner(m *mockRedisCmd) *mockPipeliner                  { return &mockPipeliner{m: m} }
func (p *mockPipeliner) Exec(_ context.Context) ([]redis.Cmder, error) { return p.cmds, nil }
func (p *mockPipeliner) Discard()                                      { p.cmds = nil }
func (p *mockPipeliner) Close() error                                  { return nil }
func (p *mockPipeliner) Len() int                                      { return len(p.cmds) }

func (p *mockPipeliner) Set(_ context.Context, key string, value interface{}, exp time.Duration) *redis.StatusCmd {
	cmd := p.m.Set(context.Background(), key, value, exp)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *mockPipeliner) Del(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := p.m.Del(context.Background(), keys...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *mockPipeliner) HSet(_ context.Context, key string, values ...interface{}) *redis.IntCmd {
	cmd := p.m.HSet(context.Background(), key, values...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *mockPipeliner) HDel(_ context.Context, key string, fields ...string) *redis.IntCmd {
	cmd := p.m.HDel(context.Background(), key, fields...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *mockPipeliner) SRem(_ context.Context, key string, members ...interface{}) *redis.IntCmd {
	return redisIntOK(0)
}
func (p *mockPipeliner) Publish(_ context.Context, ch string, msg interface{}) *redis.IntCmd {
	return redisIntOK(1)
}
func (p *mockPipeliner) ZRem(_ context.Context, key string, members ...interface{}) *redis.IntCmd {
	cmd := p.m.ZRem(context.Background(), key, members...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *mockPipeliner) ZAdd(_ context.Context, key string, members ...redis.Z) *redis.IntCmd {
	return redisIntOK(int64(len(members)))
}
func (p *mockPipeliner) SAdd(_ context.Context, key string, members ...interface{}) *redis.IntCmd {
	return redisIntOK(int64(len(members)))
}
func (p *mockPipeliner) Exists(_ context.Context, keys ...string) *redis.IntCmd { return redisIntOK(0) }
func (p *mockPipeliner) Get(_ context.Context, key string) *redis.StringCmd     { return nil }

// ── helpers ─────────────────────────────────────────────────────────────

func redisIntOK(n int64) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(n)
	return cmd
}
func redisStringSliceOK(vals []string) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(vals)
	return cmd
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestRedisAgentRegistry_Serialization(t *testing.T) {
	mock := newMockRedisCmd()
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
