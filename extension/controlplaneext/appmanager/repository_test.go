// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRepo is a factory that creates a fresh AppRepository for each test case.
type newRepo func(t *testing.T) AppRepository

// contractTest runs the standard AppRepository contract suite against any implementation.
// All AppRepository implementations must pass these tests.
func contractTest(t *testing.T, factory newRepo) {
	t.Helper()

	t.Run("InsertAndFindByID", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-001",
			Name:      "test-app",
			Token:     "token-abc",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Retention: RetentionPolicy{
				Trace:  720 * time.Hour,
				Metric: 168 * time.Hour,
			},
		}

		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		found, err := repo.FindByID(ctx, "app-001")
		require.NoError(t, err)
		assert.Equal(t, "test-app", found.Name)
		assert.Equal(t, "token-abc", found.Token)
		assert.Equal(t, "active", found.Status)
		assert.Equal(t, 720*time.Hour, found.Retention.Trace)
		assert.Equal(t, 168*time.Hour, found.Retention.Metric)
		assert.Equal(t, time.Duration(0), found.Retention.Log) // not set → zero
	})

	t.Run("FindByToken", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-002",
			Name:      "token-test-app",
			Token:     "tok-xyz",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		found, err := repo.FindByToken(ctx, "tok-xyz")
		require.NoError(t, err)
		assert.Equal(t, "app-002", found.ID)
		assert.Equal(t, "token-test-app", found.Name)
	})

	t.Run("FindByID_NotFound", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		_, err := repo.FindByID(ctx, "non-existent")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("FindByToken_NotFound", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		_, err := repo.FindByToken(ctx, "no-such-token")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Save_UpdatesExisting", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-003",
			Name:      "original",
			Token:     "tok-orig",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		// Read back, modify, save
		updated, err := repo.FindByID(ctx, "app-003")
		require.NoError(t, err)
		updated.Name = "updated-name"
		updated.Status = "disabled"
		updated.Description = "new desc"
		updated.Retention = RetentionPolicy{Trace: 30 * 24 * time.Hour} // 30d

		err = repo.Save(ctx, updated)
		require.NoError(t, err)

		found, err := repo.FindByID(ctx, "app-003")
		require.NoError(t, err)
		assert.Equal(t, "updated-name", found.Name)
		assert.Equal(t, "disabled", found.Status)
		assert.Equal(t, "new desc", found.Description)
		assert.Equal(t, 30*24*time.Hour, found.Retention.Trace)
		// Token should still work
		tokenFound, err := repo.FindByToken(ctx, "tok-orig")
		require.NoError(t, err)
		assert.Equal(t, "app-003", tokenFound.ID)
	})

	t.Run("Save_TokenChange", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-004",
			Name:      "token-swap",
			Token:     "old-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		updated, err := repo.FindByID(ctx, "app-004")
		require.NoError(t, err)
		updated.Token = "new-token"

		err = repo.Save(ctx, updated)
		require.NoError(t, err)

		// New token works
		found, err := repo.FindByToken(ctx, "new-token")
		require.NoError(t, err)
		assert.Equal(t, "app-004", found.ID)

		// Old token is invalid
		_, err = repo.FindByToken(ctx, "old-token")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Save_NotFound", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		err := repo.Save(ctx, &AppInfo{ID: "no-such-app", Name: "x", Token: "y"})
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Delete", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-005",
			Name:      "to-delete",
			Token:     "del-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		err = repo.Delete(ctx, "app-005")
		require.NoError(t, err)

		_, err = repo.FindByID(ctx, "app-005")
		assert.ErrorIs(t, err, ErrNotFound)

		_, err = repo.FindByToken(ctx, "del-token")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Delete_NotFound", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		err := repo.Delete(ctx, "no-such-app")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("List_Empty", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		apps, err := repo.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, apps)
	})

	t.Run("List_WithApps", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		for i, name := range []string{"app-a", "app-b", "app-c"} {
			err := repo.Insert(ctx, &AppInfo{
				ID:        "id-" + string(rune('a'+i)),
				Name:      name,
				Token:     "tok-" + name,
				Status:    "active",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			})
			require.NoError(t, err)
		}

		apps, err := repo.List(ctx)
		require.NoError(t, err)
		assert.Len(t, apps, 3)
	})

	t.Run("Retention_Roundtrip", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-retention",
			Name:      "retention-test",
			Token:     "ret-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Retention: RetentionPolicy{
				Trace:  30 * 24 * time.Hour,
				Metric: 14 * 24 * time.Hour,
				Log:    7 * 24 * time.Hour,
			},
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		found, err := repo.FindByID(ctx, "app-retention")
		require.NoError(t, err)
		assert.Equal(t, 30*24*time.Hour, found.Retention.Trace)
		assert.Equal(t, 14*24*time.Hour, found.Retention.Metric)
		assert.Equal(t, 7*24*time.Hour, found.Retention.Log)
	})

	t.Run("Retention_ZeroIsDefault", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "app-no-retention",
			Name:      "no-retention",
			Token:     "no-ret-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			// Retention is zero value → no overrides
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		found, err := repo.FindByID(ctx, "app-no-retention")
		require.NoError(t, err)
		assert.True(t, found.Retention.IsZero())
	})

	t.Run("Insert_DuplicateID", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app := &AppInfo{
			ID:        "dup-id",
			Name:      "first",
			Token:     "tok-a",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app)
		require.NoError(t, err)

		duplicate := &AppInfo{
			ID:        "dup-id",
			Name:      "second",
			Token:     "tok-b",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err = repo.Insert(ctx, duplicate)
		assert.Error(t, err)
	})

	t.Run("Insert_DuplicateToken", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()

		app1 := &AppInfo{
			ID:        "id-1",
			Name:      "app-1",
			Token:     "shared-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err := repo.Insert(ctx, app1)
		require.NoError(t, err)

		app2 := &AppInfo{
			ID:        "id-2",
			Name:      "app-2",
			Token:     "shared-token",
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		err = repo.Insert(ctx, app2)
		assert.Error(t, err)
	})
}

// ═══════════════════════════════════════════════════
// MemoryAppRepository tests
// ═══════════════════════════════════════════════════

func TestMemoryAppRepository(t *testing.T) {
	contractTest(t, func(t *testing.T) AppRepository {
		return NewMemoryAppRepository()
	})
}

// ═══════════════════════════════════════════════════
// RedisAppRepository tests (using miniredis)
// ═══════════════════════════════════════════════════

func newTestRedisClient(t *testing.T) redis.UniversalClient {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestRedisAppRepository(t *testing.T) {
	contractTest(t, func(t *testing.T) AppRepository {
		return NewRedisAppRepository(newTestRedisClient(t), "otel:test")
	})
}
