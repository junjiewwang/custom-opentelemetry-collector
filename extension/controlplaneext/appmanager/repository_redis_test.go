// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// These tests prove the P1 refactoring for the appmanager package: the shared
// redisclient.MockRedis (wrapped to satisfy appRedisCmd) can be injected into
// NewRedisAppRepository, and the serialization round-trip works without a real
// Redis instance.

package appmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/internal/redisclient"
)

// appMockRedis wraps the shared redisclient.MockRedis, overriding
// TxPipeline/Pipeline to return appPipelineCmd (with HSetNX). All data
// methods (HSet/HGet/HGetAll/HDel/HSetNX) come from the embedded MockRedis.
type appMockRedis struct {
	*redisclient.MockRedis
}

func (m appMockRedis) TxPipeline() appPipelineCmd {
	// MockRedis.TxPipeline returns redisclient.PipelineCmd; the underlying
	// *MockPipeline satisfies appPipelineCmd (it has HSetNX).
	return m.MockRedis.TxPipeline().(appPipelineCmd)
}

func (m appMockRedis) Pipeline() appPipelineCmd {
	return m.MockRedis.Pipeline().(appPipelineCmd)
}

func newAppMockRedis() *appMockRedis {
	return &appMockRedis{MockRedis: redisclient.NewMockRedis()}
}

// ── Tests ───────────────────────────────────────────────────────────────

func newTestApp(id, token string) *AppInfo {
	now := time.Now()
	return &AppInfo{
		ID:        id,
		Name:      "app-" + id,
		Token:     token,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestRedisAppRepository_SaveAndFindByID(t *testing.T) {
	mock := newAppMockRedis()
	repo := NewRedisAppRepository(mock, "otel:apps")

	ctx := context.Background()
	app := newTestApp("app-1", "token-1")
	require.NoError(t, repo.Insert(ctx, app))

	got, err := repo.FindByID(ctx, "app-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "app-1", got.ID)
	assert.Equal(t, "token-1", got.Token)
	assert.Equal(t, "active", got.Status)

	// Save (update) then re-read.
	app.Status = "disabled"
	require.NoError(t, repo.Save(ctx, app))
	got, err = repo.FindByID(ctx, "app-1")
	require.NoError(t, err)
	assert.Equal(t, "disabled", got.Status)
}

func TestRedisAppRepository_FindByToken(t *testing.T) {
	mock := newAppMockRedis()
	repo := NewRedisAppRepository(mock, "otel:apps")

	ctx := context.Background()
	require.NoError(t, repo.Insert(ctx, newTestApp("app-2", "token-2")))

	got, err := repo.FindByToken(ctx, "token-2")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "app-2", got.ID)

	// Unknown token → ErrNotFound.
	_, err = repo.FindByToken(ctx, "nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRedisAppRepository_Delete(t *testing.T) {
	mock := newAppMockRedis()
	repo := NewRedisAppRepository(mock, "otel:apps")

	ctx := context.Background()
	require.NoError(t, repo.Insert(ctx, newTestApp("app-3", "token-3")))

	require.NoError(t, repo.Delete(ctx, "app-3"))

	_, err := repo.FindByID(ctx, "app-3")
	assert.ErrorIs(t, err, ErrNotFound)

	// Deleting again → ErrNotFound.
	assert.ErrorIs(t, repo.Delete(ctx, "app-3"), ErrNotFound)
}

func TestRedisAppRepository_List(t *testing.T) {
	mock := newAppMockRedis()
	repo := NewRedisAppRepository(mock, "otel:apps")

	ctx := context.Background()
	require.NoError(t, repo.Insert(ctx, newTestApp("a", "ta")))
	require.NoError(t, repo.Insert(ctx, newTestApp("b", "tb")))

	apps, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, apps, 2)

	ids := map[string]bool{}
	for _, a := range apps {
		ids[a.ID] = true
	}
	assert.True(t, ids["a"])
	assert.True(t, ids["b"])
}

func TestRedisAppRepository_InsertDuplicateID(t *testing.T) {
	mock := newAppMockRedis()
	repo := NewRedisAppRepository(mock, "otel:apps")

	ctx := context.Background()
	require.NoError(t, repo.Insert(ctx, newTestApp("dup", "t1")))
	// Same ID again → ErrNotFound (HSetNX returns false).
	assert.ErrorIs(t, repo.Insert(ctx, newTestApp("dup", "t2")), ErrNotFound)
}

func TestRedisCmdAdapter(t *testing.T) {
	assert.NotNil(t, NewRedisCmd)
}
