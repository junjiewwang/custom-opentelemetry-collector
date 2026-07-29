// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// These tests prove the P1 refactoring for the servicemanager/store package:
// the shared redisclient.MockRedis can be injected into NewRedisServiceStore,
// and the serialization round-trip works without a real Redis instance.
//
// CreateIfAbsent is tested because the Lua script result is mockable via the
// Scripter methods (Eval/EvalSha) — set mock.ScriptResult. ListAll is skipped
// because the Scan iterator mock is complex to wire correctly (see comment
// below).

package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// ── Tests ───────────────────────────────────────────────────────────────

func newTestService(appID, name string) *ServiceInfo {
	now := time.Now()
	return &ServiceInfo{
		ID:          appID + ":" + name,
		AppID:       appID,
		ServiceName: name,
		Description: "desc-" + name,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func newMockRedisServiceStore(t *testing.T, mock *redisclient.MockRedis) *RedisServiceStore {
	t.Helper()
	s := NewRedisServiceStore(zap.NewNop(), mock, "otel:test")
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRedisServiceStore_CreateIfAbsent_Mock(t *testing.T) {
	mock := redisclient.NewMockRedis()
	s := newMockRedisServiceStore(t, mock)
	ctx := context.Background()

	svc := newTestService("app-1", "svc-1")
	data, _ := json.Marshal(svc)
	// Script returns {1, json} → created.
	mock.ScriptResult = []interface{}{int64(1), string(data)}

	created, existing, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotNil(t, existing)
	assert.Equal(t, "svc-1", existing.ServiceName)

	// Now simulate already-exists: script returns {0, existingJson}.
	mock.ScriptResult = []interface{}{int64(0), string(data)}
	created2, existing2, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)
	assert.False(t, created2)
	assert.NotNil(t, existing2)
	assert.Equal(t, "svc-1", existing2.ServiceName)
}

func TestRedisServiceStore_Get_Mock(t *testing.T) {
	mock := redisclient.NewMockRedis()
	s := newMockRedisServiceStore(t, mock)
	ctx := context.Background()

	svc := newTestService("app-2", "svc-2")
	data, _ := json.Marshal(svc)
	mock.Hashes["otel:test:app-2"] = map[string]string{"svc-2": string(data)}

	got, err := s.Get(ctx, "app-2", "svc-2")
	require.NoError(t, err)
	assert.Equal(t, "svc-2", got.ServiceName)
	assert.Equal(t, "app-2", got.AppID)

	// Not found.
	_, err = s.Get(ctx, "app-2", "nope")
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestRedisServiceStore_Delete_Mock(t *testing.T) {
	mock := redisclient.NewMockRedis()
	s := newMockRedisServiceStore(t, mock)
	ctx := context.Background()

	svc := newTestService("app-3", "svc-3")
	data, _ := json.Marshal(svc)
	mock.Hashes["otel:test:app-3"] = map[string]string{"svc-3": string(data)}
	mock.Hashes["otel:test:_id_index"] = map[string]string{svc.ID: "app-3:svc-3"}

	require.NoError(t, s.Delete(ctx, "app-3", "svc-3"))

	// Hash entry removed.
	_, err := s.Get(ctx, "app-3", "svc-3")
	assert.ErrorIs(t, err, ErrServiceNotFound)

	// Delete again → not found.
	assert.ErrorIs(t, s.Delete(ctx, "app-3", "svc-3"), ErrServiceNotFound)
}

func TestRedisServiceStore_ListByApp_Mock(t *testing.T) {
	mock := redisclient.NewMockRedis()
	s := newMockRedisServiceStore(t, mock)
	ctx := context.Background()

	s1 := newTestService("app-4", "alpha")
	s2 := newTestService("app-4", "beta")
	d1, _ := json.Marshal(s1)
	d2, _ := json.Marshal(s2)
	mock.Hashes["otel:test:app-4"] = map[string]string{
		"alpha": string(d1),
		"beta":  string(d2),
	}

	svcs, err := s.ListByApp(ctx, "app-4", ListServiceFilter{})
	require.NoError(t, err)
	assert.Len(t, svcs, 2)

	// Filter by name pattern.
	svcs, err = s.ListByApp(ctx, "app-4", ListServiceFilter{NamePattern: "alp"})
	require.NoError(t, err)
	assert.Len(t, svcs, 1)
	assert.Equal(t, "alpha", svcs[0].ServiceName)
}

// TestRedisServiceStore_ListAll_Mock is intentionally skipped: ListAll uses
// client.Scan(...).Iterator(), and the go-redis ScanIterator fetches
// subsequent pages by invoking an internal process callback that a mock
// cannot satisfy without a real connection. The Scan method on the mock
// returns an empty page (cursor 0), which yields an empty result — so the
// test would pass but exercise nothing useful. Full coverage of ListAll
// remains in redis_integration_test.go (miniredis-backed).
func TestRedisServiceStore_ListAll_Mock(t *testing.T) {
	t.Skip("ListAll relies on ScanIterator pagination that requires a real Redis connection; covered by integration tests")
}

func TestRedisServiceStore_GetByID_StaleIndex_Mock(t *testing.T) {
	mock := redisclient.NewMockRedis()
	s := newMockRedisServiceStore(t, mock)
	ctx := context.Background()

	// Index points to a record that no longer exists → stale index cleanup.
	mock.Hashes["otel:test:_id_index"] = map[string]string{"svc-id": "app-5:ghost"}

	_, err := s.GetByID(ctx, "svc-id")
	assert.ErrorIs(t, err, ErrServiceNotFound)

	// Stale index entry should have been cleaned up.
	mock.Lock()
	_, present := mock.Hashes["otel:test:_id_index"]["svc-id"]
	mock.Unlock()
	assert.False(t, present, "stale index entry should be removed")
}

func TestRedisCmdAdapter(t *testing.T) {
	assert.NotNil(t, NewRedisCmd)
	// Sanity: ensure the nil client error path works without a real client.
	s := &RedisServiceStore{logger: zap.NewNop()}
	_, err := s.getClient()
	assert.Error(t, err)
	_ = errors.Is // keep import if not otherwise used
	_ = redis.Nil // keep import if not otherwise used
}
