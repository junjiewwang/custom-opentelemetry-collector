// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"strings"
	"sync"
	"time"
)

type memoryRuntimeSnapshotStore struct {
	mu      sync.RWMutex
	entries map[string]*agentRuntimeSnapshotCacheEntry
}

var _ RuntimeSnapshotStore = (*memoryRuntimeSnapshotStore)(nil)

func newMemoryRuntimeSnapshotStore() *memoryRuntimeSnapshotStore {
	return &memoryRuntimeSnapshotStore{
		entries: make(map[string]*agentRuntimeSnapshotCacheEntry),
	}
}

func (s *memoryRuntimeSnapshotStore) Start(context.Context) error {
	return nil
}

func (s *memoryRuntimeSnapshotStore) Close() error {
	return nil
}

func (s *memoryRuntimeSnapshotStore) Get(_ context.Context, agentID string) (*agentRuntimeSnapshotCacheEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAgentRuntimeSnapshotCacheEntry(s.entries[strings.TrimSpace(agentID)]), nil
}

func (s *memoryRuntimeSnapshotStore) Upsert(_ context.Context, agentID string, updater func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry) (*agentRuntimeSnapshotCacheEntry, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || updater == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := cloneAgentRuntimeSnapshotCacheEntry(s.entries[agentID])
	next := updater(current)
	if next == nil {
		delete(s.entries, agentID)
		return nil, nil
	}
	next.AgentID = agentID
	s.entries[agentID] = cloneAgentRuntimeSnapshotCacheEntry(next)
	return cloneAgentRuntimeSnapshotCacheEntry(next), nil
}

func (s *memoryRuntimeSnapshotStore) MarkDirty(_ context.Context, agentIDs []string) error {
	if len(agentIDs) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rawAgentID := range agentIDs {
		agentID := strings.TrimSpace(rawAgentID)
		if agentID == "" {
			continue
		}
		entry := cloneAgentRuntimeSnapshotCacheEntry(s.entries[agentID])
		if entry == nil {
			entry = &agentRuntimeSnapshotCacheEntry{AgentID: agentID}
		}
		entry.Dirty = true
		entry.ExpiresAtMillis = now
		entry.UpdatedAtMillis = now
		s.entries[agentID] = entry
	}
	return nil
}

func (s *memoryRuntimeSnapshotStore) TryAcquireRefreshLease(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
