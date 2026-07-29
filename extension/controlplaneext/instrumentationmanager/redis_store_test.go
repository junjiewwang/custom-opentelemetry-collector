// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// These tests prove the P1 refactoring for the instrumentationmanager
// package: the shared redisclient.MockRedis (wrapped to satisfy instrRedisCmd)
// can be injected into NewRedisRuleStore, and the read/delete/list
// serialization round-trip works without a real Redis instance.
//
// SaveRule and SaveTargetStatuses are NOT mock-tested here: they rely on
// client.Watch with a func(*redis.Tx) callback, and *redis.Tx is a concrete
// go-redis struct with unexported fields that a mock cannot construct. Those
// code paths remain covered by redis_store_integration_test.go
// (miniredis-backed). PhysicalDeleteRule, GetRule, ListRules, and
// validateStoredData use only simple commands + Pipeline() and are fully
// mock-tested below.

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// instrMockRedis wraps the shared redisclient.MockRedis, overriding
// Pipeline to return instrPipelineCmd (with Del/Set). All data methods
// (Set/SetNX/Get/Del/PExpire/H*/Scan/Watch/Subscribe/Publish/Ping) come from
// the embedded MockRedis.
type instrMockRedis struct {
	*redisclient.MockRedis
}

func (m instrMockRedis) Pipeline() instrPipelineCmd {
	// MockRedis.Pipeline returns redisclient.PipelineCmd; the underlying
	// *MockPipeline satisfies instrPipelineCmd (it has Del + Set).
	return m.MockRedis.Pipeline().(instrPipelineCmd)
}

func newInstrMockRedis() *instrMockRedis {
	return &instrMockRedis{MockRedis: redisclient.NewMockRedis()}
}

// ── Tests ───────────────────────────────────────────────────────────────

func newMockTestRule(id string) *Rule {
	return &Rule{
		ID:          id,
		Name:        "rule-" + id,
		AppID:       "app-1",
		ServiceName: "svc-1",
		ClassName:   "com.example.Foo",
		MethodName:  "bar",
	}
}

func newMockRedisRuleStore(t *testing.T, mock *instrMockRedis) *RedisRuleStore {
	t.Helper()
	s := NewRedisRuleStore(zap.NewNop(), mock, "otel:test")
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRedisRuleStore_GetRule(t *testing.T) {
	mock := newInstrMockRedis()
	s := newMockRedisRuleStore(t, mock)
	ctx := context.Background()

	rule := newMockTestRule("rule-1")
	data, _ := json.Marshal(rule)
	mock.Hashes["otel:test:rules"] = map[string]string{"rule-1": string(data)}

	got, err := s.GetRule(ctx, "rule-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "rule-1", got.ID)
	assert.Equal(t, "rule-rule-1", got.Name)
	assert.Equal(t, "app-1", got.AppID)

	// Not found.
	_, err = s.GetRule(ctx, "nope")
	assert.ErrorIs(t, err, ErrRuleNotFound)
}

func TestRedisRuleStore_ListRules(t *testing.T) {
	mock := newInstrMockRedis()
	s := newMockRedisRuleStore(t, mock)
	ctx := context.Background()

	r1 := newMockTestRule("r1")
	r2 := newMockTestRule("r2")
	r2.AppID = "app-2"
	d1, _ := json.Marshal(r1)
	d2, _ := json.Marshal(r2)
	mock.Hashes["otel:test:rules"] = map[string]string{
		"r1": string(d1),
		"r2": string(d2),
	}

	rules, err := s.ListRules(ctx, ListRulesQuery{})
	require.NoError(t, err)
	assert.Len(t, rules, 2)

	// Filter by AppID.
	rules, err = s.ListRules(ctx, ListRulesQuery{AppID: "app-2"})
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, "r2", rules[0].ID)
}

func TestRedisRuleStore_PhysicalDeleteRule(t *testing.T) {
	mock := newInstrMockRedis()
	s := newMockRedisRuleStore(t, mock)
	ctx := context.Background()

	rule := newMockTestRule("rule-del")
	data, _ := json.Marshal(rule)
	mock.Hashes["otel:test:rules"] = map[string]string{"rule-del": string(data)}
	mock.Strings["otel:test:targets:rule-del"] = "[]"

	require.NoError(t, s.PhysicalDeleteRule(ctx, "rule-del"))

	// Rule hash entry removed.
	_, err := s.GetRule(ctx, "rule-del")
	assert.ErrorIs(t, err, ErrRuleNotFound)

	// Target key removed.
	mock.Lock()
	_, present := mock.Strings["otel:test:targets:rule-del"]
	mock.Unlock()
	assert.False(t, present)
}

func TestRedisRuleStore_ListTargetStatuses(t *testing.T) {
	mock := newInstrMockRedis()
	s := newMockRedisRuleStore(t, mock)
	ctx := context.Background()

	rule := newMockTestRule("rule-ts")
	data, _ := json.Marshal(rule)
	mock.Hashes["otel:test:rules"] = map[string]string{"rule-ts": string(data)}

	// No target statuses stored yet → empty list.
	targets, err := s.ListTargetStatuses(ctx, "rule-ts")
	require.NoError(t, err)
	assert.Empty(t, targets)

	// Store a target blob and re-read.
	ts := []*RuleTargetStatus{{RuleID: "rule-ts", AgentID: "agent-1", State: TargetStateApplied}}
	tsData, _ := json.Marshal(ts)
	mock.Strings["otel:test:targets:rule-ts"] = string(tsData)

	targets, err = s.ListTargetStatuses(ctx, "rule-ts")
	require.NoError(t, err)
	assert.Len(t, targets, 1)
	assert.Equal(t, "agent-1", targets[0].AgentID)
}

func TestRedisRuleStore_StartupValidation_RemovesInvalidRule(t *testing.T) {
	mock := newInstrMockRedis()
	// Seed an invalid rule entry (broken JSON) before Start.
	mock.Hashes["otel:test:rules"] = map[string]string{"broken": "{not-json"}
	mock.Strings["otel:test:targets:broken"] = "[]"

	s := NewRedisRuleStore(zap.NewNop(), mock, "otel:test")
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Close() })

	// validateStoredData should have removed the broken rule + its targets.
	mock.Lock()
	_, rulePresent := mock.Hashes["otel:test:rules"]["broken"]
	_, tgtPresent := mock.Strings["otel:test:targets:broken"]
	mock.Unlock()
	assert.False(t, rulePresent, "invalid rule should be removed during startup validation")
	assert.False(t, tgtPresent, "orphan target key should be removed during startup validation")
}

func TestRedisCmdAdapter(t *testing.T) {
	assert.NotNil(t, NewRedisCmd)
	// Sanity: ensure the nil client error path works without a real client.
	s := &RedisRuleStore{logger: zap.NewNop()}
	_, err := s.getClient()
	assert.Error(t, err)
	_ = redis.Nil // keep import if not otherwise used
}
