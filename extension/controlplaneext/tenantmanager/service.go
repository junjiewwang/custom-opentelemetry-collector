// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// TenantService is the single source of truth for tenant business rules. It
// depends only on abstractions (TenantRepository, IDGenerator, AppLister),
// enabling complete unit-testability without real Redis or randomness — the
// same architecture as appmanager.AppService.
type TenantService struct {
	repo      TenantRepository
	idGen     IDGenerator
	appLister AppLister
	logger    *zap.Logger
}

// NewTenantService creates a TenantService with the given dependencies.
func NewTenantService(repo TenantRepository, idGen IDGenerator, appLister AppLister, logger *zap.Logger) *TenantService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TenantService{repo: repo, idGen: idGen, appLister: appLister, logger: logger}
}

var _ TenantManager = (*TenantService)(nil)

// CreateTenant creates a tenant with a generated ID.
// Business rules: name must be non-empty and unique.
func (s *TenantService) CreateTenant(ctx context.Context, req *CreateTenantRequest) (*Tenant, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	if err := s.checkNameUnique(ctx, req.Name); err != nil {
		return nil, err
	}

	id, err := s.idGen.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate tenant id: %w", err)
	}

	accountID, err := s.repo.NextAccountID(ctx)
	if err != nil {
		return nil, fmt.Errorf("allocate tenant account id: %w", err)
	}

	now := time.Now()
	tenant := &Tenant{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Status:      StatusActive,
		AccountID:   accountID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Insert(ctx, tenant); err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}

	s.logger.Info("Tenant created", zap.String("id", id), zap.String("name", req.Name))
	return tenant, nil
}

// GetTenant returns a tenant by ID.
func (s *TenantService) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	return s.repo.FindByID(ctx, id)
}

// ListTenants returns all tenants.
func (s *TenantService) ListTenants(ctx context.Context) ([]*Tenant, error) {
	return s.repo.List(ctx)
}

// UpdateTenant updates name/description/status. Empty request fields mean
// "leave unchanged".
func (s *TenantService) UpdateTenant(ctx context.Context, id string, req *UpdateTenantRequest) (*Tenant, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Name != "" && req.Name != existing.Name {
		if err := s.checkNameUnique(ctx, req.Name); err != nil {
			return nil, err
		}
	}

	updated := *existing
	if req.Name != "" {
		updated.Name = req.Name
	}
	if req.Description != "" {
		updated.Description = req.Description
	}
	if req.Status != "" {
		updated.Status = req.Status
	}
	updated.UpdatedAt = time.Now()

	if err := s.repo.Save(ctx, &updated); err != nil {
		return nil, fmt.Errorf("save tenant: %w", err)
	}

	s.logger.Info("Tenant updated", zap.String("id", id))
	return &updated, nil
}

// DeleteTenant removes a tenant. It refuses to delete a tenant that still owns
// apps (data-isolation guard) or the built-in default tenant.
func (s *TenantService) DeleteTenant(ctx context.Context, id string) error {
	if id == DefaultTenantID {
		return ErrDefaultTenantImmutable
	}
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return err
	}

	apps, err := s.ListTenantApps(ctx, id)
	if err != nil {
		return err
	}
	if len(apps) > 0 {
		return ErrTenantNotEmpty
	}

	return s.repo.Delete(ctx, id)
}

// ListTenantApps returns the app IDs owned by a tenant. An app whose TenantID
// is empty (pre-migration) is treated as belonging to the default tenant.
func (s *TenantService) ListTenantApps(ctx context.Context, tenantID string) ([]string, error) {
	if s.appLister == nil {
		return nil, errors.New("app lister not configured")
	}
	apps, err := s.appLister.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenant apps: %w", err)
	}

	ids := make([]string, 0)
	for _, app := range apps {
		if effectiveTenantID(app.TenantID) == tenantID {
			ids = append(ids, app.ID)
		}
	}
	return ids, nil
}

// ResolveAccountID returns the VictoriaMetrics account ID that scopes data for
// the given tenant. An empty tenantID (global / super / operator request) maps
// to DefaultAccountID (0), as does the built-in "admin" tenant — matching the
// write path where tenant-less data lands in the default account.
func (s *TenantService) ResolveAccountID(ctx context.Context, tenantID string) (uint32, error) {
	if tenantID == "" || tenantID == DefaultTenantID {
		return DefaultAccountID, nil
	}
	tenant, err := s.repo.FindByID(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("resolve account id: %w", err)
	}
	return tenant.AccountID, nil
}

// EnsureDefaultTenant idempotently creates the built-in "admin" tenant.
func (s *TenantService) EnsureDefaultTenant(ctx context.Context) error {
	if _, err := s.repo.FindByID(ctx, DefaultTenantID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("check default tenant: %w", err)
	}

	now := time.Now()
	tenant := &Tenant{
		ID:          DefaultTenantID,
		Name:        "admin",
		Description: "Default tenant (owns pre-existing apps)",
		Status:      StatusActive,
		AccountID:   DefaultAccountID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Insert(ctx, tenant); err != nil {
		return fmt.Errorf("insert default tenant: %w", err)
	}
	s.logger.Info("Default tenant created", zap.String("id", DefaultTenantID))
	return nil
}

// Start ensures the default tenant exists.
func (s *TenantService) Start(ctx context.Context) error {
	return s.EnsureDefaultTenant(ctx)
}

// Close is a no-op (no background resources to release).
func (s *TenantService) Close() error {
	return nil
}

// checkNameUnique returns nil if no tenant uses the given name, ErrTenantNameExists
// if one does, or a wrapped error on storage failure.
func (s *TenantService) checkNameUnique(ctx context.Context, name string) error {
	if _, err := s.repo.FindByName(ctx, name); err == nil {
		return ErrTenantNameExists
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("check tenant name: %w", err)
	}
	return nil
}

// effectiveTenantID maps an empty TenantID (pre-migration app) to the default tenant.
func effectiveTenantID(tenantID string) string {
	if tenantID == "" {
		return DefaultTenantID
	}
	return tenantID
}
