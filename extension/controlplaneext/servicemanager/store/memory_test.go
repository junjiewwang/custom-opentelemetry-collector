// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestMemoryServiceStore(t *testing.T) *MemoryServiceStore {
	t.Helper()
	s := NewMemoryServiceStore(zap.NewNop())
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makeServiceInfo(appID, serviceName, id string) *ServiceInfo {
	now := time.Now()
	return &ServiceInfo{
		ID:          id,
		AppID:       appID,
		ServiceName: serviceName,
		Description: "test service",
		Tags:        map[string]string{"env": "test"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ============================================================================
// Basic CRUD Tests
// ============================================================================

func TestMemoryServiceStore_CreateIfAbsent_NewService(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	created, result, err := s.CreateIfAbsent(ctx, svc)

	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, result)
	assert.Equal(t, "id-001", result.ID)
	assert.Equal(t, "app1", result.AppID)
	assert.Equal(t, "svc-a", result.ServiceName)
}

func TestMemoryServiceStore_CreateIfAbsent_AlreadyExists(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc1 := makeServiceInfo("app1", "svc-a", "id-001")
	created1, _, err := s.CreateIfAbsent(ctx, svc1)
	require.NoError(t, err)
	assert.True(t, created1)

	// Second create with different ID should return existing
	svc2 := makeServiceInfo("app1", "svc-a", "id-002")
	created2, existing, err := s.CreateIfAbsent(ctx, svc2)
	require.NoError(t, err)
	assert.False(t, created2)
	assert.Equal(t, "id-001", existing.ID) // Original ID preserved
}

func TestMemoryServiceStore_Get(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	got, err := s.Get(ctx, "app1", "svc-a")
	require.NoError(t, err)
	assert.Equal(t, "id-001", got.ID)
	assert.Equal(t, "test service", got.Description)
}

func TestMemoryServiceStore_Get_NotFound(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "app1", "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestMemoryServiceStore_GetByID(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	got, err := s.GetByID(ctx, "id-001")
	require.NoError(t, err)
	assert.Equal(t, "app1", got.AppID)
	assert.Equal(t, "svc-a", got.ServiceName)
}

func TestMemoryServiceStore_GetByID_NotFound(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	_, err := s.GetByID(ctx, "nonexistent-id")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestMemoryServiceStore_Update(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	// Modify and update
	got, _ := s.Get(ctx, "app1", "svc-a")
	got.Description = "updated description"
	got.Tags = map[string]string{"env": "prod"}
	got.UpdatedAt = time.Now()
	err = s.Update(ctx, got)
	require.NoError(t, err)

	// Verify update persisted
	updated, _ := s.Get(ctx, "app1", "svc-a")
	assert.Equal(t, "updated description", updated.Description)
	assert.Equal(t, "prod", updated.Tags["env"])
}

func TestMemoryServiceStore_Update_NotFound(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	err := s.Update(ctx, svc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestMemoryServiceStore_ListByApp(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	// Create services across two apps
	for i := 0; i < 3; i++ {
		svc := makeServiceInfo("app1", fmt.Sprintf("svc-%d", i), fmt.Sprintf("id-a%d", i))
		_, _, err := s.CreateIfAbsent(ctx, svc)
		require.NoError(t, err)
	}
	svcOther := makeServiceInfo("app2", "svc-other", "id-b0")
	_, _, err := s.CreateIfAbsent(ctx, svcOther)
	require.NoError(t, err)

	list, err := s.ListByApp(ctx, "app1", ListServiceFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 3)

	// With name filter
	filtered, err := s.ListByApp(ctx, "app1", ListServiceFilter{NamePattern: "svc-1"})
	require.NoError(t, err)
	assert.Len(t, filtered, 1)
}

func TestMemoryServiceStore_ListAll(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc1 := makeServiceInfo("app1", "svc-a", "id-001")
	svc2 := makeServiceInfo("app2", "svc-b", "id-002")
	_, _, _ = s.CreateIfAbsent(ctx, svc1)
	_, _, _ = s.CreateIfAbsent(ctx, svc2)

	all, err := s.ListAll(ctx, ListServiceFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// ============================================================================
// Deep Copy Isolation Test
// ============================================================================

func TestMemoryServiceStore_DeepCopyIsolation(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, result, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	// Mutate the returned result
	result.Description = "mutated"
	result.Tags["env"] = "mutated"

	// The store's internal copy must remain unchanged
	got, err := s.Get(ctx, "app1", "svc-a")
	require.NoError(t, err)
	assert.Equal(t, "test service", got.Description)
	assert.Equal(t, "test", got.Tags["env"])
}

// ============================================================================
// Delete Tests — Atomicity of main record + ID index removal
// ============================================================================

func TestMemoryServiceStore_Delete(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	// Verify exists via both keys
	_, err = s.Get(ctx, "app1", "svc-a")
	require.NoError(t, err)
	_, err = s.GetByID(ctx, "id-001")
	require.NoError(t, err)

	// Delete
	err = s.Delete(ctx, "app1", "svc-a")
	require.NoError(t, err)

	// Both main record and ID index must be gone
	_, err = s.Get(ctx, "app1", "svc-a")
	assert.ErrorIs(t, err, ErrServiceNotFound, "main record should be deleted")

	_, err = s.GetByID(ctx, "id-001")
	assert.ErrorIs(t, err, ErrServiceNotFound, "ID index should be deleted")

	// List should be empty
	list, err := s.ListByApp(ctx, "app1", ListServiceFilter{})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestMemoryServiceStore_Delete_NotFound(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	err := s.Delete(ctx, "app1", "nonexistent")
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestMemoryServiceStore_Delete_ThenRecreate(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	// Delete
	err = s.Delete(ctx, "app1", "svc-a")
	require.NoError(t, err)

	// Recreate with a new ID (simulates "probe re-register after deletion")
	svc2 := makeServiceInfo("app1", "svc-a", "id-002")
	created, result, err := s.CreateIfAbsent(ctx, svc2)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "id-002", result.ID)

	// Old ID index must not exist
	_, err = s.GetByID(ctx, "id-001")
	assert.ErrorIs(t, err, ErrServiceNotFound)

	// New ID index works
	got, err := s.GetByID(ctx, "id-002")
	require.NoError(t, err)
	assert.Equal(t, "svc-a", got.ServiceName)
}

// ============================================================================
// Concurrent Idempotency Tests
// ============================================================================

// TestMemoryServiceStore_ConcurrentCreateIfAbsent_100 verifies that 100 concurrent
// CreateIfAbsent calls for the same (appID, serviceName) result in exactly 1 record.
func TestMemoryServiceStore_ConcurrentCreateIfAbsent_100(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	const concurrency = 100
	var (
		wg           sync.WaitGroup
		createdCount atomic.Int32
		errorCount   atomic.Int32
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			svc := makeServiceInfo("app1", "svc-concurrent", fmt.Sprintf("id-%03d", idx))
			created, _, err := s.CreateIfAbsent(ctx, svc)
			if err != nil {
				errorCount.Add(1)
				return
			}
			if created {
				createdCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(0), errorCount.Load(), "no errors expected")
	assert.Equal(t, int32(1), createdCount.Load(), "exactly 1 goroutine should have created the record")

	// Verify only 1 record exists
	list, err := s.ListByApp(ctx, "app1", ListServiceFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "svc-concurrent", list[0].ServiceName)
}

// TestMemoryServiceStore_ConcurrentCreateIfAbsent_MultipleServices verifies that
// concurrent creation of different services all succeeds.
func TestMemoryServiceStore_ConcurrentCreateIfAbsent_MultipleServices(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	const numServices = 50
	var wg sync.WaitGroup

	wg.Add(numServices)
	for i := 0; i < numServices; i++ {
		go func(idx int) {
			defer wg.Done()
			svc := makeServiceInfo("app1", fmt.Sprintf("svc-%d", idx), fmt.Sprintf("id-%d", idx))
			created, _, err := s.CreateIfAbsent(ctx, svc)
			assert.NoError(t, err)
			assert.True(t, created)
		}(i)
	}
	wg.Wait()

	list, err := s.ListAll(ctx, ListServiceFilter{})
	require.NoError(t, err)
	assert.Len(t, list, numServices)
}

// TestMemoryServiceStore_ConcurrentDelete verifies that concurrent deletes
// of the same service are safe — exactly one succeeds, the rest get ErrServiceNotFound.
func TestMemoryServiceStore_ConcurrentDelete(t *testing.T) {
	s := newTestMemoryServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-del", "id-del")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	const concurrency = 20
	var (
		wg           sync.WaitGroup
		deleteCount  atomic.Int32
		notFoundErr  atomic.Int32
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			err := s.Delete(ctx, "app1", "svc-del")
			if err == nil {
				deleteCount.Add(1)
			} else if assert.ErrorIs(t, err, ErrServiceNotFound) {
				notFoundErr.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), deleteCount.Load(), "exactly 1 goroutine should delete successfully")
	assert.Equal(t, int32(concurrency-1), notFoundErr.Load(), "remaining goroutines should get not-found")

	// Both main record and ID index must be gone
	_, err = s.Get(ctx, "app1", "svc-del")
	assert.ErrorIs(t, err, ErrServiceNotFound)
	_, err = s.GetByID(ctx, "id-del")
	assert.ErrorIs(t, err, ErrServiceNotFound)
}
