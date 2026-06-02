// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"
	"time"
)

// InMemoryRetentionStore implements RetentionStore with a simple in-memory map.
// Suitable for single-instance deployments or as a baseline before introducing
// persistent storage (PG, etcd, etc.) in later sprints.
//
// Thread-safe via sync.RWMutex.
type InMemoryRetentionStore struct {
	mu    sync.RWMutex
	store map[storeKey]*time.Duration
}

// storeKey is the composite key for per-app, per-signal retention override.
type storeKey struct {
	appID  string
	signal SignalType
}

// NewInMemoryRetentionStore creates a new empty in-memory retention store.
func NewInMemoryRetentionStore() *InMemoryRetentionStore {
	return &InMemoryRetentionStore{
		store: make(map[storeKey]*time.Duration),
	}
}

// Compile-time interface satisfaction check.
var _ RetentionStore = (*InMemoryRetentionStore)(nil)

// GetForApp returns the per-app override for the given signal.
// Returns nil if no override exists.
func (s *InMemoryRetentionStore) GetForApp(_ context.Context, appID string, signal SignalType) (*time.Duration, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := storeKey{appID: appID, signal: signal}
	dur, ok := s.store[key]
	if !ok {
		return nil, nil
	}
	// Return a copy to prevent external mutation
	copied := *dur
	return &copied, nil
}

// SetForApp sets a per-app retention override.
func (s *InMemoryRetentionStore) SetForApp(_ context.Context, appID string, signal SignalType, retention time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := storeKey{appID: appID, signal: signal}
	s.store[key] = &retention
	return nil
}

// DeleteForApp removes a per-app override.
func (s *InMemoryRetentionStore) DeleteForApp(_ context.Context, appID string, signal SignalType) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := storeKey{appID: appID, signal: signal}
	delete(s.store, key)
	return nil
}

// ListAppOverrides returns all apps that have custom retention settings.
func (s *InMemoryRetentionStore) ListAppOverrides(_ context.Context) ([]AppRetentionEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Group by appID
	appMap := make(map[string]map[SignalType]time.Duration)
	for key, dur := range s.store {
		if dur == nil {
			continue
		}
		if _, ok := appMap[key.appID]; !ok {
			appMap[key.appID] = make(map[SignalType]time.Duration)
		}
		appMap[key.appID][key.signal] = *dur
	}

	entries := make([]AppRetentionEntry, 0, len(appMap))
	for appID, overrides := range appMap {
		entries = append(entries, AppRetentionEntry{
			AppID:     appID,
			Overrides: overrides,
		})
	}
	return entries, nil
}
