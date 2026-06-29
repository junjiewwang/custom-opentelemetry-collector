// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import "sync"

// WaiterMap is a type-safe concurrency-safe map for managing poll waiters.
// It wraps sync.Map and enforces the CompareAndDelete pattern on deregistration,
// eliminating the stale-defer race where an old Poll's defer would delete
// a new waiter registered for the same agent.
//
// Both ConfigPollHandler and TaskPollHandlerEngine use this to eliminate
// duplicated waiter management logic (DRY).
type WaiterMap[W any] struct {
	m sync.Map
}

// Register stores a waiter by key, overwriting any existing entry.
func (wm *WaiterMap[W]) Register(key string, waiter *W) {
	wm.m.Store(key, waiter)
}

// Deregister removes the waiter by key ONLY if the stored value matches the given pointer.
// This prevents the race where an old Poll's defer deletes a new Poll's waiter.
func (wm *WaiterMap[W]) Deregister(key string, waiter *W) {
	wm.m.CompareAndDelete(key, waiter)
}

// Load retrieves a waiter by key. Returns nil, false if not found.
func (wm *WaiterMap[W]) Load(key string) (*W, bool) {
	val, ok := wm.m.Load(key)
	if !ok {
		return nil, false
	}
	return val.(*W), true
}

// Range iterates over all waiters. Stops if f returns false.
func (wm *WaiterMap[W]) Range(f func(key string, waiter *W) bool) {
	wm.m.Range(func(k, v any) bool {
		return f(k.(string), v.(*W))
	})
}

// IsEmpty returns true if no waiters are registered.
// O(1) when non-empty (early exit), O(n) only when empty.
func (wm *WaiterMap[W]) IsEmpty() bool {
	empty := true
	wm.m.Range(func(_, _ any) bool {
		empty = false
		return false // stop at first element
	})
	return empty
}

// Count returns the number of registered waiters. O(n) — use sparingly.
func (wm *WaiterMap[W]) Count() int {
	count := 0
	wm.m.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// Clear removes all waiters and cancels them if a cancelFn is provided.
func (wm *WaiterMap[W]) Clear(cancelFn func(waiter *W)) {
	wm.m.Range(func(key, _ any) bool {
		if cancelFn != nil {
			if val, ok := wm.m.Load(key); ok {
				cancelFn(val.(*W))
			}
		}
		wm.m.Delete(key)
		return true
	})
}
