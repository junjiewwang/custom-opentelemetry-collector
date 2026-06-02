// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInMemoryRetentionStore_SetAndGet(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	retention := 7 * 24 * time.Hour
	err := store.SetForApp(ctx, "app-001", SignalTrace, retention)
	if err != nil {
		t.Fatalf("SetForApp failed: %v", err)
	}

	got, err := store.GetForApp(ctx, "app-001", SignalTrace)
	if err != nil {
		t.Fatalf("GetForApp failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if *got != retention {
		t.Errorf("expected %v, got %v", retention, *got)
	}
}

func TestInMemoryRetentionStore_GetNonExistent(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	got, err := store.GetForApp(ctx, "non-existent", SignalTrace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-existent, got %v", *got)
	}
}

func TestInMemoryRetentionStore_DifferentSignals(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	traceRetention := 7 * 24 * time.Hour
	metricRetention := 30 * 24 * time.Hour
	logRetention := 14 * 24 * time.Hour

	_ = store.SetForApp(ctx, "app-001", SignalTrace, traceRetention)
	_ = store.SetForApp(ctx, "app-001", SignalMetric, metricRetention)
	_ = store.SetForApp(ctx, "app-001", SignalLog, logRetention)

	tests := []struct {
		signal   SignalType
		expected time.Duration
	}{
		{SignalTrace, traceRetention},
		{SignalMetric, metricRetention},
		{SignalLog, logRetention},
	}

	for _, tt := range tests {
		t.Run(string(tt.signal), func(t *testing.T) {
			got, err := store.GetForApp(ctx, "app-001", tt.signal)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestInMemoryRetentionStore_DifferentApps(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	_ = store.SetForApp(ctx, "app-001", SignalTrace, 7*24*time.Hour)
	_ = store.SetForApp(ctx, "app-002", SignalTrace, 14*24*time.Hour)

	got1, _ := store.GetForApp(ctx, "app-001", SignalTrace)
	got2, _ := store.GetForApp(ctx, "app-002", SignalTrace)

	if got1 == nil || *got1 != 7*24*time.Hour {
		t.Errorf("app-001: expected 7d, got %v", got1)
	}
	if got2 == nil || *got2 != 14*24*time.Hour {
		t.Errorf("app-002: expected 14d, got %v", got2)
	}
}

func TestInMemoryRetentionStore_Delete(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	_ = store.SetForApp(ctx, "app-001", SignalTrace, 7*24*time.Hour)

	// Verify it exists
	got, _ := store.GetForApp(ctx, "app-001", SignalTrace)
	if got == nil {
		t.Fatal("expected non-nil before delete")
	}

	// Delete
	err := store.DeleteForApp(ctx, "app-001", SignalTrace)
	if err != nil {
		t.Fatalf("DeleteForApp failed: %v", err)
	}

	// Verify it's gone
	got, err = store.GetForApp(ctx, "app-001", SignalTrace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %v", *got)
	}
}

func TestInMemoryRetentionStore_DeleteNonExistent(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	// Should not error on deleting non-existent key
	err := store.DeleteForApp(ctx, "non-existent", SignalTrace)
	if err != nil {
		t.Errorf("expected no error on delete of non-existent, got %v", err)
	}
}

func TestInMemoryRetentionStore_Overwrite(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	_ = store.SetForApp(ctx, "app-001", SignalTrace, 7*24*time.Hour)
	_ = store.SetForApp(ctx, "app-001", SignalTrace, 14*24*time.Hour) // overwrite

	got, _ := store.GetForApp(ctx, "app-001", SignalTrace)
	if got == nil || *got != 14*24*time.Hour {
		t.Errorf("expected overwritten value 14d, got %v", got)
	}
}

func TestInMemoryRetentionStore_ListAppOverrides_Empty(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	entries, err := store.ListAppOverrides(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestInMemoryRetentionStore_ListAppOverrides_MultipleApps(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	_ = store.SetForApp(ctx, "app-001", SignalTrace, 7*24*time.Hour)
	_ = store.SetForApp(ctx, "app-001", SignalMetric, 30*24*time.Hour)
	_ = store.SetForApp(ctx, "app-002", SignalLog, 14*24*time.Hour)

	entries, err := store.ListAppOverrides(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 app entries, got %d", len(entries))
	}

	// Build map for easier verification
	entryMap := make(map[string]AppRetentionEntry)
	for _, e := range entries {
		entryMap[e.AppID] = e
	}

	app1, ok := entryMap["app-001"]
	if !ok {
		t.Fatal("app-001 not found in entries")
	}
	if len(app1.Overrides) != 2 {
		t.Errorf("app-001: expected 2 overrides, got %d", len(app1.Overrides))
	}
	if app1.Overrides[SignalTrace] != 7*24*time.Hour {
		t.Errorf("app-001 trace: expected 7d, got %v", app1.Overrides[SignalTrace])
	}
	if app1.Overrides[SignalMetric] != 30*24*time.Hour {
		t.Errorf("app-001 metric: expected 30d, got %v", app1.Overrides[SignalMetric])
	}

	app2, ok := entryMap["app-002"]
	if !ok {
		t.Fatal("app-002 not found in entries")
	}
	if len(app2.Overrides) != 1 {
		t.Errorf("app-002: expected 1 override, got %d", len(app2.Overrides))
	}
	if app2.Overrides[SignalLog] != 14*24*time.Hour {
		t.Errorf("app-002 log: expected 14d, got %v", app2.Overrides[SignalLog])
	}
}

func TestInMemoryRetentionStore_GetReturnsCopy(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	original := 7 * 24 * time.Hour
	_ = store.SetForApp(ctx, "app-001", SignalTrace, original)

	got, _ := store.GetForApp(ctx, "app-001", SignalTrace)
	if got == nil {
		t.Fatal("expected non-nil result")
	}

	// Mutate the returned pointer — should NOT affect the store
	*got = 999 * time.Hour

	// Verify store is unchanged
	got2, _ := store.GetForApp(ctx, "app-001", SignalTrace)
	if got2 == nil || *got2 != original {
		t.Errorf("store was mutated via returned pointer! expected %v, got %v", original, got2)
	}
}

// ═══════════════════════════════════════════════════
// Concurrency Safety Test
// ═══════════════════════════════════════════════════

func TestInMemoryRetentionStore_ConcurrentAccess(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	const numGoroutines = 100
	const numOps = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3) // writers + readers + deleters

	// Concurrent writers
	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for j := range numOps {
				appID := "app-" + string(rune('A'+id%26))
				signals := AllSignals()
				signal := signals[j%3]
				dur := time.Duration(j+1) * 24 * time.Hour
				_ = store.SetForApp(ctx, appID, signal, dur)
			}
		}(i)
	}

	// Concurrent readers
	for range numGoroutines {
		go func() {
			defer wg.Done()
			for range numOps {
				_, _ = store.GetForApp(ctx, "app-A", SignalTrace)
				_, _ = store.ListAppOverrides(ctx)
			}
		}()
	}

	// Concurrent deleters
	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for range numOps {
				appID := "app-" + string(rune('A'+id%26))
				_ = store.DeleteForApp(ctx, appID, SignalTrace)
			}
		}(i)
	}

	// If there's a race condition, this will be caught by -race flag
	wg.Wait()
}
