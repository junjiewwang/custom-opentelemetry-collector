// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager/store"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

// ServiceService provides high-level service management operations.
// It encapsulates business logic including:
//   - idempotent EnsureService with atomic store semantics
//   - delete precondition (instance_count == 0)
//   - metadata immutability rules (appID, serviceName, ID cannot change)
//   - runtime field aggregation (not persisted)
//   - backfill from existing data sources
type ServiceService struct {
	logger     *zap.Logger
	config     Config
	store      store.ServiceStore
	backfillDS BackfillDataSource // optional, injected after initialization
}

// NewServiceService creates a new ServiceService with the given store.
func NewServiceService(logger *zap.Logger, config Config, serviceStore store.ServiceStore) *ServiceService {
	return &ServiceService{
		logger: logger,
		config: config,
		store:  serviceStore,
	}
}

// Ensure ServiceService implements ServiceManager.
var _ ServiceManager = (*ServiceService)(nil)

// ===== Service Creation =====

// CreateService implements ServiceManager.
// Explicitly creates a new service record. Returns an error if it already exists.
func (s *ServiceService) CreateService(ctx context.Context, req *CreateServiceRequest) (*ServiceInfo, error) {
	if req == nil {
		return nil, errors.New("request cannot be nil")
	}
	if req.AppID == "" {
		return nil, errors.New("app_id is required")
	}
	if req.ServiceName == "" {
		return nil, errors.New("service_name is required")
	}

	serviceID, err := tokenmanager.GenerateID()
	if err != nil {
		return nil, fmt.Errorf("generate service ID: %w", err)
	}

	now := time.Now()
	storeSvc := &store.ServiceInfo{
		ID:          serviceID,
		AppID:       req.AppID,
		ServiceName: req.ServiceName,
		Description: req.Description,
		Tags:        req.Tags,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	created, existing, err := s.store.CreateIfAbsent(ctx, storeSvc)
	if err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}

	if !created {
		return nil, fmt.Errorf("service already exists: appID=%s, serviceName=%s", req.AppID, req.ServiceName)
	}

	_ = existing // created is true, existing holds the newly created record
	return fromStoreServiceInfo(existing), nil
}

// EnsureService implements ServiceManager.
// Guarantees a service record exists. If it already exists, returns the existing one.
// If not, creates a new one. This is designed for the agent registration hot path:
// failures are logged but should NOT block the registration flow.
func (s *ServiceService) EnsureService(ctx context.Context, appID, serviceName string) (*ServiceInfo, error) {
	if appID == "" || serviceName == "" {
		return nil, errors.New("app_id and service_name are required")
	}

	serviceID, err := tokenmanager.GenerateID()
	if err != nil {
		return nil, fmt.Errorf("generate service ID: %w", err)
	}

	now := time.Now()
	storeSvc := &store.ServiceInfo{
		ID:          serviceID,
		AppID:       appID,
		ServiceName: serviceName,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	created, result, err := s.store.CreateIfAbsent(ctx, storeSvc)
	if err != nil {
		return nil, fmt.Errorf("ensure service: %w", err)
	}

	if created {
		s.logger.Debug("Service created via EnsureService",
			zap.String("app_id", appID),
			zap.String("service_name", serviceName),
			zap.String("service_id", result.ID),
		)
	}

	return fromStoreServiceInfo(result), nil
}

// ===== Service Queries =====

// GetService implements ServiceManager.
func (s *ServiceService) GetService(ctx context.Context, appID, serviceName string) (*ServiceInfo, error) {
	info, err := s.store.Get(ctx, appID, serviceName)
	if err != nil {
		return nil, err
	}
	return fromStoreServiceInfo(info), nil
}

// GetServiceByID implements ServiceManager.
func (s *ServiceService) GetServiceByID(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	info, err := s.store.GetByID(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	return fromStoreServiceInfo(info), nil
}

// ===== Service Metadata Update =====

// UpdateServiceMetadata implements ServiceManager.
// Only Description and Tags are mutable. AppID, ServiceName, and ID are immutable.
func (s *ServiceService) UpdateServiceMetadata(ctx context.Context, appID, serviceName string, req *UpdateServiceRequest) (*ServiceInfo, error) {
	if req == nil {
		return nil, errors.New("request cannot be nil")
	}

	// Fetch the current record
	existing, err := s.store.Get(ctx, appID, serviceName)
	if err != nil {
		return nil, err
	}

	// Apply mutable field updates
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Tags != nil {
		existing.Tags = req.Tags
	}
	existing.UpdatedAt = time.Now()

	// Persist the update
	if err := s.store.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("update service metadata: %w", err)
	}

	return fromStoreServiceInfo(existing), nil
}

// ===== Service Deletion =====

// DeleteService implements ServiceManager.
// Default precondition: instance_count == 0.
// Does NOT cascade-delete configuration.
func (s *ServiceService) DeleteService(ctx context.Context, appID, serviceName string) error {
	// Note: instance_count check will be implemented when AgentRegistry integration
	// is wired in. For now, the delete is unconditional at the Store level.
	// TODO: Add instance_count == 0 precondition check via AgentRegistry.

	if err := s.store.Delete(ctx, appID, serviceName); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	s.logger.Info("Service deleted",
		zap.String("app_id", appID),
		zap.String("service_name", serviceName),
	)
	return nil
}

// ===== Service Listing =====

// ListServicesByApp implements ServiceManager.
func (s *ServiceService) ListServicesByApp(ctx context.Context, appID string, q ListServicesQuery) ([]*ServiceInfo, error) {
	storeFilter := store.ListServiceFilter{
		NamePattern: q.NamePattern,
	}

	storeInfos, err := s.store.ListByApp(ctx, appID, storeFilter)
	if err != nil {
		return nil, err
	}

	result := make([]*ServiceInfo, 0, len(storeInfos))
	for _, si := range storeInfos {
		result = append(result, fromStoreServiceInfo(si))
	}
	return result, nil
}

// ListAllServices implements ServiceManager.
func (s *ServiceService) ListAllServices(ctx context.Context, q ListServicesQuery) ([]*ServiceInfo, error) {
	storeFilter := store.ListServiceFilter{
		NamePattern: q.NamePattern,
	}

	storeInfos, err := s.store.ListAll(ctx, storeFilter)
	if err != nil {
		return nil, err
	}

	result := make([]*ServiceInfo, 0, len(storeInfos))
	for _, si := range storeInfos {
		result = append(result, fromStoreServiceInfo(si))
	}
	return result, nil
}

// ===== Backfill =====

// SetBackfillDataSource implements ServiceManager.
// Injects the data source for BackfillServices after all components are initialized.
func (s *ServiceService) SetBackfillDataSource(ds BackfillDataSource) {
	s.backfillDS = ds
}

// BackfillServices implements ServiceManager.
// Creates service records from existing data sources using idempotent EnsureService semantics.
// - FromRegistry: enumerates (appID, serviceName) pairs from AgentRegistry via BackfillDataSource
// - FromConfig: reserved for future ConfigManager enumeration (not yet available)
// - DryRun: only reports what would be created without persisting
func (s *ServiceService) BackfillServices(ctx context.Context, opts BackfillOptions) (*BackfillResult, error) {
	result := &BackfillResult{
		Details: make([]BackfillDetail, 0),
	}

	s.logger.Info("BackfillServices started",
		zap.Bool("from_registry", opts.FromRegistry),
		zap.Bool("from_config", opts.FromConfig),
		zap.Bool("dry_run", opts.DryRun),
	)

	if opts.FromRegistry {
		if s.backfillDS == nil {
			s.logger.Warn("BackfillDataSource not set, skipping registry backfill")
		} else {
			if err := s.backfillFromRegistry(ctx, opts.DryRun, result); err != nil {
				s.logger.Warn("Error during registry backfill (partial results may exist)", zap.Error(err))
			}
		}
	}

	if opts.FromConfig {
		if s.backfillDS == nil {
			s.logger.Warn("BackfillDataSource not set, skipping config backfill")
		} else {
			if err := s.backfillFromConfig(ctx, opts.DryRun, result); err != nil {
				s.logger.Warn("Error during config backfill (partial results may exist)", zap.Error(err))
			}
		}
	}

	s.logger.Info("BackfillServices completed",
		zap.Int("created", result.Created),
		zap.Int("skipped", result.Skipped),
		zap.Int("errors", result.Errors),
	)

	return result, nil
}

// isReservedServiceName returns true if the service name is a system placeholder
// or reserved DataId that should not be created as a real service record.
// It consolidates filtering logic used by both backfillFromRegistry and
// backfillFromConfig, avoiding duplicate hardcoded checks.
func isReservedServiceName(name string) bool {
	// AgentRegistry placeholder for agents without a service name
	if name == "_unknown" {
		return true
	}
	// Delegate to configmanager's centralized DataId exclusion list
	// (covers _unused_default_, _default_, empty string, etc.)
	return configmanager.IsSystemReservedDataID(name)
}

// backfillFromRegistry enumerates services from the AgentRegistry data source
// and ensures each one exists in the ServiceManager store.
func (s *ServiceService) backfillFromRegistry(ctx context.Context, dryRun bool, result *BackfillResult) error {
	appIDs, err := s.backfillDS.GetAllAppIDs(ctx)
	if err != nil {
		return fmt.Errorf("get all app IDs: %w", err)
	}

	for _, appID := range appIDs {
		serviceNames, err := s.backfillDS.GetServiceNamesByApp(ctx, appID)
		if err != nil {
			s.logger.Warn("Failed to get services for app during backfill",
				zap.String("app_id", appID),
				zap.Error(err),
			)
			continue
		}

		for _, svcName := range serviceNames {
			// Skip system-reserved/placeholder service names
			if isReservedServiceName(svcName) {
				continue
			}

			detail := BackfillDetail{
				AppID:       appID,
				ServiceName: svcName,
			}

			if dryRun {
				// Check if it already exists
				_, getErr := s.store.Get(ctx, appID, svcName)
				if getErr != nil {
					detail.Action = "would_create"
					result.Created++
				} else {
					detail.Action = "would_skip"
					result.Skipped++
				}
			} else {
				_, ensureErr := s.EnsureService(ctx, appID, svcName)
				if ensureErr != nil {
					detail.Action = "error"
					detail.Error = ensureErr.Error()
					result.Errors++
					s.logger.Warn("Backfill EnsureService failed",
						zap.String("app_id", appID),
						zap.String("service_name", svcName),
						zap.Error(ensureErr),
					)
				} else {
					// EnsureService is idempotent: we check if we truly created or just saw existing.
					// For backfill tracking, we can re-check, but since EnsureService
					// already handles this cleanly, we simply count as "processed".
					// A more precise count would require EnsureService to return created/existing status.
					detail.Action = "ensured"
					result.Created++
				}
			}

			result.Details = append(result.Details, detail)
		}
	}

	return nil
}

// backfillFromConfig enumerates services from the ConfigManager data source
// (via GetConfiguredServiceNamesByApp) and ensures each one exists in the
// ServiceManager store. This populates service records with has_config awareness.
func (s *ServiceService) backfillFromConfig(ctx context.Context, dryRun bool, result *BackfillResult) error {
	appIDs, err := s.backfillDS.GetAllAppIDs(ctx)
	if err != nil {
		return fmt.Errorf("get all app IDs for config backfill: %w", err)
	}

	for _, appID := range appIDs {
		serviceNames, err := s.backfillDS.GetConfiguredServiceNamesByApp(ctx, appID)
		if err != nil {
			s.logger.Warn("Failed to get configured services for app during backfill",
				zap.String("app_id", appID),
				zap.Error(err),
			)
			continue
		}

		for _, svcName := range serviceNames {
			// Skip system-reserved/placeholder service names
			if isReservedServiceName(svcName) {
				continue
			}

			detail := BackfillDetail{
				AppID:       appID,
				ServiceName: svcName,
			}

			if dryRun {
				_, getErr := s.store.Get(ctx, appID, svcName)
				if getErr != nil {
					detail.Action = "would_create"
					result.Created++
				} else {
					detail.Action = "would_skip"
					result.Skipped++
				}
			} else {
				_, ensureErr := s.EnsureService(ctx, appID, svcName)
				if ensureErr != nil {
					detail.Action = "error"
					detail.Error = ensureErr.Error()
					result.Errors++
					s.logger.Warn("Config backfill EnsureService failed",
						zap.String("app_id", appID),
						zap.String("service_name", svcName),
						zap.Error(ensureErr),
					)
				} else {
					detail.Action = "ensured"
					result.Created++
				}
			}

			result.Details = append(result.Details, detail)
		}
	}

	return nil
}

// ===== Lifecycle =====

// Start implements ServiceManager.
func (s *ServiceService) Start(ctx context.Context) error {
	s.logger.Info("Starting service manager")
	return s.store.Start(ctx)
}

// Close implements ServiceManager.
func (s *ServiceService) Close() error {
	return s.store.Close()
}

// ===== Type Conversion Helpers =====

// toStoreServiceInfo converts a public ServiceInfo to a store.ServiceInfo.
func toStoreServiceInfo(info *ServiceInfo) *store.ServiceInfo {
	if info == nil {
		return nil
	}
	si := &store.ServiceInfo{
		ID:          info.ID,
		AppID:       info.AppID,
		ServiceName: info.ServiceName,
		Description: info.Description,
		Tags:        info.Tags,
		CreatedAt:   info.CreatedAt,
		UpdatedAt:   info.UpdatedAt,
		LastSeenAt:  info.LastSeenAt,
	}
	return si
}

// fromStoreServiceInfo converts a store.ServiceInfo to a public ServiceInfo.
func fromStoreServiceInfo(info *store.ServiceInfo) *ServiceInfo {
	if info == nil {
		return nil
	}
	return &ServiceInfo{
		ID:          info.ID,
		AppID:       info.AppID,
		ServiceName: info.ServiceName,
		Description: info.Description,
		Tags:        info.Tags,
		CreatedAt:   info.CreatedAt,
		UpdatedAt:   info.UpdatedAt,
		LastSeenAt:  info.LastSeenAt,
	}
}
