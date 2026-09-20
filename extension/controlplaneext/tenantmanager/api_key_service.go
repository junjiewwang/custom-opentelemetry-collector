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

// APIKeyService manages read-path tenant credentials (ok_/tk_ keys). It depends
// only on abstractions (APIKeyRepository, TenantRepository, IDGenerator), so it
// is fully unit-testable. The static super key (sk_) is handled by the HTTP
// middleware, not here.
type APIKeyService struct {
	repo    APIKeyRepository
	tenants TenantRepository
	idGen   IDGenerator
	logger  *zap.Logger
}

// NewAPIKeyService creates an APIKeyService with the given dependencies.
func NewAPIKeyService(repo APIKeyRepository, tenants TenantRepository, idGen IDGenerator, logger *zap.Logger) *APIKeyService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &APIKeyService{repo: repo, tenants: tenants, idGen: idGen, logger: logger}
}

// CreateAPIKey creates a key for a tenant and returns its metadata plus the
// one-time plaintext. Operator keys (ok_) are forced onto the admin tenant.
func (s *APIKeyService) CreateAPIKey(ctx context.Context, tenantID string, req *CreateAPIKeyRequest) (*CreateAPIKeyResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.KeyType == KeyTypeOperator {
		tenantID = DefaultTenantID
	}

	tenant, err := s.tenants.FindByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if tenant.Status != StatusActive {
		return nil, fmt.Errorf("tenant %q is disabled", tenantID)
	}

	plainKey, keyHash, keyPrefix, err := GenerateAPIKey(req.KeyType)
	if err != nil {
		return nil, err
	}
	keyID, err := s.idGen.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate key id: %w", err)
	}

	key := &TenantAPIKey{
		ID:        keyID,
		TenantID:  tenantID,
		KeyType:   req.KeyType,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
		Name:      req.Name,
		Scopes:    req.Scopes,
		Status:    KeyStatusActive,
		CreatedAt: time.Now(),
	}
	if err := s.repo.Insert(ctx, key); err != nil {
		return nil, fmt.Errorf("insert api key: %w", err)
	}

	s.logger.Info("API key created",
		zap.String("id", keyID), zap.String("type", req.KeyType), zap.String("tenant", tenantID))
	return &CreateAPIKeyResponse{TenantAPIKey: *key, PlainKey: plainKey}, nil
}

// ListAPIKeys returns all keys (active and revoked) for a tenant.
func (s *APIKeyService) ListAPIKeys(ctx context.Context, tenantID string) ([]*TenantAPIKey, error) {
	return s.repo.ListByTenant(ctx, tenantID)
}

// RevokeAPIKey flips a key's status to revoked. Idempotent.
func (s *APIKeyService) RevokeAPIKey(ctx context.Context, keyID string) error {
	key, err := s.repo.FindByID(ctx, keyID)
	if err != nil {
		return err
	}
	if key.Status == KeyStatusRevoked {
		return nil
	}

	key.Status = KeyStatusRevoked
	if err := s.repo.Save(ctx, key); err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	s.logger.Info("API key revoked", zap.String("id", keyID))
	return nil
}

// ValidateAPIKey resolves a plaintext key to a validation result. A nil error
// means the lookup itself succeeded; check result.Valid for the auth decision
// (invalid covers unknown/revoked keys and disabled tenants).
func (s *APIKeyService) ValidateAPIKey(ctx context.Context, plainKey string) (*KeyValidation, error) {
	if plainKey == "" {
		return &KeyValidation{Valid: false, Reason: "empty key"}, nil
	}

	key, err := s.repo.FindByHash(ctx, sha256Hex(plainKey))
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			return &KeyValidation{Valid: false, Reason: "key not found"}, nil
		}
		return nil, fmt.Errorf("validate api key: %w", err)
	}

	if key.Status != KeyStatusActive {
		return &KeyValidation{Valid: false, TenantID: key.TenantID, KeyType: key.KeyType, Reason: "key revoked"}, nil
	}

	tenant, err := s.tenants.FindByID(ctx, key.TenantID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &KeyValidation{Valid: false, TenantID: key.TenantID, KeyType: key.KeyType, Reason: "tenant not found"}, nil
		}
		return nil, fmt.Errorf("validate api key tenant: %w", err)
	}
	if tenant.Status != StatusActive {
		return &KeyValidation{Valid: false, TenantID: key.TenantID, KeyType: key.KeyType, Reason: "tenant disabled"}, nil
	}

	return &KeyValidation{
		Valid:    true,
		TenantID: key.TenantID,
		KeyType:  key.KeyType,
		Scopes:   key.Scopes,
	}, nil
}
