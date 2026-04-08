// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ============================================================================
// Redis Test Helpers
// ============================================================================

func newTestRedisServiceClient(t *testing.T) *redis.Client {
	t.Helper()

	_, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server not found in PATH, skipping Redis integration test")
	}

	port := reserveLocalServicePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var serverOutput bytes.Buffer
	cmd := exec.Command(
		"redis-server",
		"--save", "",
		"--appendonly", "no",
		"--bind", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--dir", t.TempDir(),
		"--loglevel", "warning",
	)
	cmd.Stdout = &serverOutput
	cmd.Stderr = &serverOutput
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = client.Close()
	})

	require.Eventually(t, func() bool {
		return client.Ping(context.Background()).Err() == nil
	}, 5*time.Second, 50*time.Millisecond, "redis-server did not start successfully: %s", serverOutput.String())

	return client
}

func newTestRedisServiceStore(t *testing.T) *RedisServiceStore {
	t.Helper()

	client := newTestRedisServiceClient(t)
	store := NewRedisServiceStore(
		zap.NewNop(),
		client,
		fmt.Sprintf("otel:test:%s", sanitizeServiceKeyPart(t.Name())),
	)
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

func reserveLocalServicePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}

func sanitizeServiceKeyPart(name string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", ":", "-")
	return replacer.Replace(name)
}

// ============================================================================
// Basic CRUD Tests (Redis)
// ============================================================================

func TestRedisServiceStore_CreateIfAbsent_NewService(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

func TestRedisServiceStore_CreateIfAbsent_AlreadyExists(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

func TestRedisServiceStore_Get(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	got, err := s.Get(ctx, "app1", "svc-a")
	require.NoError(t, err)
	assert.Equal(t, "id-001", got.ID)
	assert.Equal(t, "test service", got.Description)
}

func TestRedisServiceStore_Get_NotFound(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "app1", "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestRedisServiceStore_GetByID(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	got, err := s.GetByID(ctx, "id-001")
	require.NoError(t, err)
	assert.Equal(t, "app1", got.AppID)
	assert.Equal(t, "svc-a", got.ServiceName)
}

func TestRedisServiceStore_GetByID_NotFound(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	_, err := s.GetByID(ctx, "nonexistent-id")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestRedisServiceStore_Update(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

func TestRedisServiceStore_Update_NotFound(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-a", "id-001")
	err := s.Update(ctx, svc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestRedisServiceStore_ListByApp(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

func TestRedisServiceStore_ListAll(t *testing.T) {
	s := newTestRedisServiceStore(t)
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
// Delete Tests — Atomicity of main record + ID index removal (Redis)
// ============================================================================

func TestRedisServiceStore_Delete(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

func TestRedisServiceStore_Delete_NotFound(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	err := s.Delete(ctx, "app1", "nonexistent")
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

func TestRedisServiceStore_Delete_ThenRecreate(t *testing.T) {
	s := newTestRedisServiceStore(t)
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
// Concurrent Idempotency Tests (Redis — Lua script atomicity)
// ============================================================================

// TestRedisServiceStore_ConcurrentCreateIfAbsent_100 verifies that 100 concurrent
// CreateIfAbsent calls for the same (appID, serviceName) result in exactly 1 record.
func TestRedisServiceStore_ConcurrentCreateIfAbsent_100(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

// TestRedisServiceStore_ConcurrentCreateIfAbsent_MultipleServices verifies that
// concurrent creation of different services all succeeds.
func TestRedisServiceStore_ConcurrentCreateIfAbsent_MultipleServices(t *testing.T) {
	s := newTestRedisServiceStore(t)
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

// TestRedisServiceStore_ConcurrentDelete verifies that concurrent deletes
// of the same service are safe — exactly one succeeds, the rest get ErrServiceNotFound.
func TestRedisServiceStore_ConcurrentDelete(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-del", "id-del")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	const concurrency = 20
	var (
		wg          sync.WaitGroup
		deleteCount atomic.Int32
		notFoundErr atomic.Int32
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

// ============================================================================
// Redis-specific: ID index consistency
// ============================================================================

// TestRedisServiceStore_IDIndex_AtomicCreation verifies that both the app hash
// and the ID index are created atomically by the Lua script.
func TestRedisServiceStore_IDIndex_AtomicCreation(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-idx", "id-idx-001")
	created, result, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "id-idx-001", result.ID)

	// Verify we can look up by ID
	byID, err := s.GetByID(ctx, "id-idx-001")
	require.NoError(t, err)
	assert.Equal(t, "app1", byID.AppID)
	assert.Equal(t, "svc-idx", byID.ServiceName)

	// Verify the main record also exists
	byKey, err := s.Get(ctx, "app1", "svc-idx")
	require.NoError(t, err)
	assert.Equal(t, "id-idx-001", byKey.ID)
}

// TestRedisServiceStore_IDIndex_AtomicDeletion verifies that Delete removes
// both the main record and the ID index entry via Redis pipeline.
func TestRedisServiceStore_IDIndex_AtomicDeletion(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	svc := makeServiceInfo("app1", "svc-idx-del", "id-idx-del")
	_, _, err := s.CreateIfAbsent(ctx, svc)
	require.NoError(t, err)

	// Confirm both exist
	_, err = s.Get(ctx, "app1", "svc-idx-del")
	require.NoError(t, err)
	_, err = s.GetByID(ctx, "id-idx-del")
	require.NoError(t, err)

	// Delete
	require.NoError(t, s.Delete(ctx, "app1", "svc-idx-del"))

	// Both must be gone
	_, err = s.Get(ctx, "app1", "svc-idx-del")
	assert.ErrorIs(t, err, ErrServiceNotFound)
	_, err = s.GetByID(ctx, "id-idx-del")
	assert.ErrorIs(t, err, ErrServiceNotFound)
}

// TestRedisServiceStore_GetByID_StaleIndex verifies that GetByID handles stale
// index entries gracefully (cleans up the index when main record is missing).
func TestRedisServiceStore_GetByID_StaleIndex(t *testing.T) {
	s := newTestRedisServiceStore(t)
	ctx := context.Background()

	// Manually insert a stale index entry (ID exists but main record doesn't)
	client, err := s.getClient()
	require.NoError(t, err)

	// Write stale index: id-stale -> "app1:svc-stale"
	err = client.HSet(ctx, s.idIndexKey(), "id-stale", "app1:svc-stale").Err()
	require.NoError(t, err)

	// GetByID should return not-found and clean up the stale entry
	_, err = s.GetByID(ctx, "id-stale")
	assert.ErrorIs(t, err, ErrServiceNotFound)

	// The stale index entry should now be removed
	exists, err := client.HExists(ctx, s.idIndexKey(), "id-stale").Result()
	require.NoError(t, err)
	assert.False(t, exists, "stale index entry should be cleaned up")
}
