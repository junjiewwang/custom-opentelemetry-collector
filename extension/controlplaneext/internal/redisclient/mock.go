// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package redisclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// MockRedis is an in-process mock implementing ALL Redis methods used across
// controlplaneext. Each sub-package's test imports this mock and uses only
// the methods relevant to its interface — unused methods return zero values.
//
// The Strings and Hashes fields are exported so tests can seed/inspect state
// directly (matching the per-package mocks this replaces).
type MockRedis struct {
	mu      sync.Mutex
	Strings map[string]string
	Hashes  map[string]map[string]string

	// ScriptResult, if set, is returned by Eval/EvalSha to simulate a Lua
	// script (used by servicemanager/store's createIfAbsentScript).
	ScriptResult []interface{}
}

func NewMockRedis() *MockRedis {
	return &MockRedis{
		Strings: make(map[string]string),
		Hashes:  make(map[string]map[string]string),
	}
}

// ── Base RedisCmd methods ───────────────────────────────────────────────

func (m *MockRedis) Set(_ context.Context, key string, value interface{}, _ time.Duration) *redis.StatusCmd {
	m.mu.Lock()
	switch v := value.(type) {
	case string:
		m.Strings[key] = v
	case []byte:
		m.Strings[key] = string(v)
	default:
		m.Strings[key] = fmt.Sprintf("%v", v)
	}
	m.mu.Unlock()
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("OK")
	return cmd
}
func (m *MockRedis) Get(_ context.Context, key string) *redis.StringCmd {
	m.mu.Lock()
	v, ok := m.Strings[key]
	m.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	if ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}
func (m *MockRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	n := int64(0)
	for _, k := range keys {
		if _, ok := m.Strings[k]; ok {
			n++
			delete(m.Strings, k)
		}
	}
	m.mu.Unlock()
	return mockInt(n)
}
func (m *MockRedis) HSet(_ context.Context, key string, values ...interface{}) *redis.IntCmd {
	m.mu.Lock()
	if m.Hashes[key] == nil {
		m.Hashes[key] = make(map[string]string)
	}
	n := int64(0)
	for i := 0; i < len(values)-1; i += 2 {
		f, v := fmt.Sprint(values[i]), fmt.Sprint(values[i+1])
		if _, ok := m.Hashes[key][f]; !ok {
			n++
		}
		m.Hashes[key][f] = v
	}
	m.mu.Unlock()
	return mockInt(n)
}
func (m *MockRedis) HGet(_ context.Context, key, field string) *redis.StringCmd {
	m.mu.Lock()
	v, ok := m.Hashes[key][field]
	m.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	if ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}
func (m *MockRedis) HGetAll(_ context.Context, key string) *redis.MapStringStringCmd {
	m.mu.Lock()
	out := make(map[string]string, len(m.Hashes[key]))
	for k, v := range m.Hashes[key] {
		out[k] = v
	}
	m.mu.Unlock()
	cmd := redis.NewMapStringStringCmd(context.Background())
	cmd.SetVal(out)
	return cmd
}
func (m *MockRedis) HDel(_ context.Context, key string, fields ...string) *redis.IntCmd {
	m.mu.Lock()
	n := int64(0)
	if m.Hashes[key] != nil {
		for _, f := range fields {
			if _, ok := m.Hashes[key][f]; ok {
				n++
				delete(m.Hashes[key], f)
			}
		}
	}
	m.mu.Unlock()
	return mockInt(n)
}
func (m *MockRedis) Pipeline() PipelineCmd   { return &MockPipeline{m: m} }
func (m *MockRedis) TxPipeline() PipelineCmd { return &MockPipeline{m: m} }
func (m *MockRedis) Ping(_ context.Context) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("PONG")
	return cmd
}

// ── Extension methods (used by some packages, not all) ────────────────
// These are defined on MockRedis so any package's local extension interface
// is satisfied. Methods not relevant to a package simply return zero values.

func (m *MockRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	n := int64(0)
	for _, k := range keys {
		if _, ok := m.Strings[k]; ok || len(m.Hashes[k]) > 0 {
			n++
		}
	}
	m.mu.Unlock()
	return mockInt(n)
}
func (m *MockRedis) TTL(_ context.Context, key string) *redis.DurationCmd {
	cmd := redis.NewDurationCmd(context.Background(), 0)
	cmd.SetVal(-2 * time.Second)
	return cmd
}
func (m *MockRedis) SCard(_ context.Context, key string) *redis.IntCmd { return mockInt(0) }
func (m *MockRedis) SMembers(_ context.Context, key string) *redis.StringSliceCmd {
	return mockSlice(nil)
}
func (m *MockRedis) ZRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	return mockSlice(nil)
}
func (m *MockRedis) ZRem(_ context.Context, key string, _ ...interface{}) *redis.IntCmd {
	return mockInt(0)
}
func (m *MockRedis) Publish(_ context.Context, _ string, _ interface{}) *redis.IntCmd {
	return mockInt(1)
}
func (m *MockRedis) HSetNX(_ context.Context, key, field string, value interface{}) *redis.BoolCmd {
	m.mu.Lock()
	if m.Hashes[key] == nil {
		m.Hashes[key] = make(map[string]string)
	}
	_, exists := m.Hashes[key][field]
	if !exists {
		m.Hashes[key][field] = fmt.Sprint(value)
	}
	m.mu.Unlock()
	cmd := redis.NewBoolCmd(context.Background())
	cmd.SetVal(!exists)
	return cmd
}
func (m *MockRedis) HExists(_ context.Context, key, field string) *redis.BoolCmd {
	m.mu.Lock()
	_, ok := m.Hashes[key][field]
	m.mu.Unlock()
	cmd := redis.NewBoolCmd(context.Background())
	cmd.SetVal(ok)
	return cmd
}
func (m *MockRedis) SetNX(_ context.Context, key string, value interface{}, _ time.Duration) *redis.BoolCmd {
	m.mu.Lock()
	_, exists := m.Strings[key]
	if !exists {
		m.Strings[key] = fmt.Sprint(value)
	}
	m.mu.Unlock()
	cmd := redis.NewBoolCmd(context.Background())
	cmd.SetVal(!exists)
	return cmd
}
func (m *MockRedis) PExpire(_ context.Context, key string, _ time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(context.Background())
	cmd.SetVal(true)
	return cmd
}
func (m *MockRedis) Scan(_ context.Context, cursor uint64, _ string, _ int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(context.Background(), nil, cursor, "", 0)
	cmd.SetVal(nil, 0)
	return cmd
}
func (m *MockRedis) Watch(_ context.Context, fn func(*redis.Tx) error, _ ...string) error {
	return fn(&redis.Tx{})
}
func (m *MockRedis) Subscribe(_ context.Context, _ ...string) *redis.PubSub {
	// Cannot construct a real *redis.PubSub; return nil. Callers of Subscribe
	// are in the runtime snapshot store, not unit-tested with this mock.
	return nil
}

// ── Scripter methods (required by servicemanager/store's redis.Script.Run) ─
// ScriptResult, if set, is returned by Eval/EvalSha to simulate a Lua script.
// Tests set m.ScriptResult = []interface{}{int64(1), jsonString} etc.
func (m *MockRedis) Eval(_ context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	return m.scriptCmd()
}
func (m *MockRedis) EvalSha(_ context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	return m.scriptCmd()
}
func (m *MockRedis) EvalRO(_ context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	return m.scriptCmd()
}
func (m *MockRedis) EvalShaRO(_ context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	return m.scriptCmd()
}
func (m *MockRedis) ScriptExists(_ context.Context, _ ...string) *redis.BoolSliceCmd {
	cmd := redis.NewBoolSliceCmd(context.Background())
	cmd.SetVal([]bool{true})
	return cmd
}
func (m *MockRedis) ScriptLoad(_ context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	cmd.SetVal("dummy-sha")
	return cmd
}
func (m *MockRedis) scriptCmd() *redis.Cmd {
	cmd := redis.NewCmd(context.Background())
	if m.ScriptResult != nil {
		cmd.SetVal(m.ScriptResult)
	}
	return cmd
}

// Lock/Unlock expose the internal mutex so tests can inspect Strings/Hashes
// atomically (matching the per-package mocks this replaces, which locked
// before asserting on internal state).
func (m *MockRedis) Lock()   { m.mu.Lock() }
func (m *MockRedis) Unlock() { m.mu.Unlock() }

// ── MockPipeline (implements all pipeline methods) ─────────────────────

type MockPipeline struct {
	m    *MockRedis
	cmds []redis.Cmder
}

func (p *MockPipeline) Exec(_ context.Context) ([]redis.Cmder, error) { return p.cmds, nil }

// Base PipelineCmd methods
func (p *MockPipeline) HSet(ctx context.Context, key string, values ...interface{}) *redis.IntCmd {
	cmd := p.m.HSet(ctx, key, values...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	cmd := p.m.HDel(ctx, key, fields...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}

// Extension pipeline methods (return zero values, just record the cmd)
func (p *MockPipeline) Set(_ context.Context, key string, value interface{}, _ time.Duration) *redis.StatusCmd {
	cmd := p.m.Set(context.Background(), key, value, 0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	cmd := p.m.Del(ctx, keys...)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) HSetNX(_ context.Context, key, field string, value interface{}) *redis.BoolCmd {
	cmd := p.m.HSetNX(context.Background(), key, field, value)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) ZAdd(_ context.Context, _ string, _ ...redis.Z) *redis.IntCmd {
	cmd := mockInt(0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) ZRem(_ context.Context, _ string, _ ...interface{}) *redis.IntCmd {
	cmd := mockInt(0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) SAdd(_ context.Context, _ string, _ ...interface{}) *redis.IntCmd {
	cmd := mockInt(0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) SRem(_ context.Context, _ string, _ ...interface{}) *redis.IntCmd {
	cmd := mockInt(0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) Exists(_ context.Context, _ ...string) *redis.IntCmd {
	cmd := mockInt(0)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) Publish(_ context.Context, _ string, _ interface{}) *redis.IntCmd {
	cmd := mockInt(1)
	p.cmds = append(p.cmds, cmd)
	return cmd
}
func (p *MockPipeline) Get(_ context.Context, key string) *redis.StringCmd {
	return p.m.Get(context.Background(), key)
}

// ── helpers ─────────────────────────────────────────────────────────────

func mockInt(n int64) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(n)
	return cmd
}
func mockSlice(vals []string) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(vals)
	return cmd
}
