// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// MemoryServiceStore implements ServiceStore using in-memory storage.
// It provides atomic CreateIfAbsent semantics via sync.RWMutex.
type MemoryServiceStore struct {
	logger *zap.Logger

	mu sync.RWMutex
	// services stores service records keyed by "appID:serviceName".
	services map[string]*ServiceInfo
	// idIndex maps serviceID -> "appID:serviceName" for GetByID lookups.
	idIndex map[string]string

	started bool
}

// NewMemoryServiceStore creates a new in-memory service store.
func NewMemoryServiceStore(logger *zap.Logger) *MemoryServiceStore {
	return &MemoryServiceStore{
		logger:   logger,
		services: make(map[string]*ServiceInfo),
		idIndex:  make(map[string]string),
	}
}

// Ensure MemoryServiceStore implements ServiceStore.
var _ ServiceStore = (*MemoryServiceStore)(nil)

// compositeKey builds the map key from (appID, serviceName).
func compositeKey(appID, serviceName string) string {
	return appID + ":" + serviceName
}

// ===== CRUD Operations =====

// CreateIfAbsent implements ServiceStore.
// Under a single write lock, it checks for existence and creates atomically.
func (s *MemoryServiceStore) CreateIfAbsent(ctx context.Context, svc *ServiceInfo) (bool, *ServiceInfo, error) {
	if svc == nil {
		return false, nil, ErrServiceNotFound
	}

	key := compositeKey(svc.AppID, svc.ServiceName)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already exists
	if existing, ok := s.services[key]; ok {
		copied := cloneServiceInfo(existing)
		return false, copied, nil
	}

	// Create: store both the main record and the ID index
	stored := cloneServiceInfo(svc)
	s.services[key] = stored
	s.idIndex[svc.ID] = key

	return true, cloneServiceInfo(stored), nil
}

// Get implements ServiceStore.
func (s *MemoryServiceStore) Get(ctx context.Context, appID, serviceName string) (*ServiceInfo, error) {
	key := compositeKey(appID, serviceName)

	s.mu.RLock()
	defer s.mu.RUnlock()

	info, ok := s.services[key]
	if !ok {
		return nil, ServiceNotFound(appID, serviceName)
	}
	return cloneServiceInfo(info), nil
}

// GetByID implements ServiceStore.
func (s *MemoryServiceStore) GetByID(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key, ok := s.idIndex[serviceID]
	if !ok {
		return nil, ServiceNotFoundByID(serviceID)
	}

	info, ok := s.services[key]
	if !ok {
		// Inconsistent state: index exists but record doesn't.
		// Clean up the stale index entry.
		// Note: we hold RLock, so we cannot delete here.
		// This is a defensive return; cleanup happens on next write.
		return nil, ServiceNotFoundByID(serviceID)
	}

	return cloneServiceInfo(info), nil
}

// Update implements ServiceStore.
func (s *MemoryServiceStore) Update(ctx context.Context, svc *ServiceInfo) error {
	if svc == nil {
		return ErrServiceNotFound
	}

	key := compositeKey(svc.AppID, svc.ServiceName)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.services[key]; !ok {
		return ServiceNotFound(svc.AppID, svc.ServiceName)
	}

	s.services[key] = cloneServiceInfo(svc)
	return nil
}

// Delete implements ServiceStore.
func (s *MemoryServiceStore) Delete(ctx context.Context, appID, serviceName string) error {
	key := compositeKey(appID, serviceName)

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.services[key]
	if !ok {
		return ServiceNotFound(appID, serviceName)
	}

	// Remove both the main record and the ID index atomically
	delete(s.idIndex, existing.ID)
	delete(s.services, key)
	return nil
}

// ===== List Operations =====

// ListByApp implements ServiceStore.
func (s *MemoryServiceStore) ListByApp(ctx context.Context, appID string, filter ListServiceFilter) ([]*ServiceInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := appID + ":"
	result := make([]*ServiceInfo, 0)
	for key, info := range s.services {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !matchesFilter(info, filter) {
			continue
		}
		result = append(result, cloneServiceInfo(info))
	}
	return result, nil
}

// ListAll implements ServiceStore.
func (s *MemoryServiceStore) ListAll(ctx context.Context, filter ListServiceFilter) ([]*ServiceInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*ServiceInfo, 0, len(s.services))
	for _, info := range s.services {
		if !matchesFilter(info, filter) {
			continue
		}
		result = append(result, cloneServiceInfo(info))
	}
	return result, nil
}

// ===== Lifecycle =====

// Start implements ServiceStore.
func (s *MemoryServiceStore) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	s.logger.Info("Starting memory service store")
	s.started = true
	return nil
}

// Close implements ServiceStore.
func (s *MemoryServiceStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil
	}

	s.started = false
	return nil
}

// ===== Helpers =====

// cloneServiceInfo returns a deep copy to prevent external mutation.
func cloneServiceInfo(info *ServiceInfo) *ServiceInfo {
	if info == nil {
		return nil
	}
	copied := *info
	if info.Tags != nil {
		copied.Tags = make(map[string]string, len(info.Tags))
		for k, v := range info.Tags {
			copied.Tags[k] = v
		}
	}
	if info.LastSeenAt != nil {
		t := *info.LastSeenAt
		copied.LastSeenAt = &t
	}
	return &copied
}

// matchesFilter checks if a service info matches the given filter criteria.
func matchesFilter(info *ServiceInfo, filter ListServiceFilter) bool {
	if filter.NamePattern != "" {
		if !strings.Contains(info.ServiceName, filter.NamePattern) {
			return false
		}
	}
	return true
}
