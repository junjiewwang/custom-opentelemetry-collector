// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestKeyService builds an APIKeyService backed by memory repos and a fixed
// ID generator, seeded with the admin tenant.
func newTestKeyService() (*APIKeyService, *MemoryTenantRepository) {
	tenants := NewMemoryTenantRepository()
	// Seed admin tenant (active) so operator keys resolve.
	_ = tenants.Insert(context.Background(), &Tenant{
		ID: DefaultTenantID, Name: "admin", Status: StatusActive,
	})
	return NewAPIKeyService(NewMemoryAPIKeyRepository(), tenants, FixedIDGenerator("key-1"), nil), tenants
}

func TestCreateAndValidateAPIKey_Tenant(t *testing.T) {
	svc, tenants := newTestKeyService()
	// Create a real tenant to hold the key.
	require.NoError(t, tenants.Insert(context.Background(), &Tenant{
		ID: "tn-acme", Name: "acme", Status: StatusActive,
	}))

	resp, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "Grafana Tempo", KeyType: KeyTypeTenant})
	require.NoError(t, err)
	assert.True(t, len(resp.PlainKey) > 0, "plaintext key must be returned")
	assert.True(t, len(resp.KeyHash) == 64, "key hash must be SHA-256 hex")

	v, err := svc.ValidateAPIKey(context.Background(), resp.PlainKey)
	require.NoError(t, err)
	assert.True(t, v.Valid)
	assert.Equal(t, "tn-acme", v.TenantID)
	assert.Equal(t, KeyTypeTenant, v.KeyType)
}

func TestValidateAPIKey_UnknownKey(t *testing.T) {
	svc, _ := newTestKeyService()
	v, err := svc.ValidateAPIKey(context.Background(), "tk_doesnotexist")
	require.NoError(t, err)
	assert.False(t, v.Valid)
	assert.Equal(t, "key not found", v.Reason)
}

func TestValidateAPIKey_RevokedKey(t *testing.T) {
	svc, tenants := newTestKeyService()
	require.NoError(t, tenants.Insert(context.Background(), &Tenant{ID: "tn-acme", Name: "acme", Status: StatusActive}))

	resp, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "k", KeyType: KeyTypeTenant})
	require.NoError(t, err)
	require.NoError(t, svc.RevokeAPIKey(context.Background(), resp.ID))

	v, err := svc.ValidateAPIKey(context.Background(), resp.PlainKey)
	require.NoError(t, err)
	assert.False(t, v.Valid)
	assert.Equal(t, "key revoked", v.Reason)
}

func TestValidateAPIKey_DisabledTenant(t *testing.T) {
	svc, tenants := newTestKeyService()
	require.NoError(t, tenants.Insert(context.Background(), &Tenant{ID: "tn-acme", Name: "acme", Status: StatusActive}))

	resp, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "k", KeyType: KeyTypeTenant})
	require.NoError(t, err)

	// Disable the tenant.
	require.NoError(t, tenants.Save(context.Background(), &Tenant{ID: "tn-acme", Name: "acme", Status: StatusDisabled}))

	v, err := svc.ValidateAPIKey(context.Background(), resp.PlainKey)
	require.NoError(t, err)
	assert.False(t, v.Valid)
	assert.Equal(t, "tenant disabled", v.Reason)
}

func TestCreateAPIKey_OperatorForcedToAdmin(t *testing.T) {
	svc, _ := newTestKeyService()
	// Even when passed a tenant ID, ok_ keys are forced onto admin.
	resp, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "ops", KeyType: KeyTypeOperator})
	require.NoError(t, err)
	assert.Equal(t, DefaultTenantID, resp.TenantID)

	v, err := svc.ValidateAPIKey(context.Background(), resp.PlainKey)
	require.NoError(t, err)
	assert.True(t, v.Valid)
	assert.Equal(t, DefaultTenantID, v.TenantID)
	assert.Equal(t, KeyTypeOperator, v.KeyType)
}

func TestCreateAPIKey_UnknownTenant(t *testing.T) {
	svc, _ := newTestKeyService()
	_, err := svc.CreateAPIKey(context.Background(), "tn-missing", &CreateAPIKeyRequest{Name: "k", KeyType: KeyTypeTenant})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCreateAPIKey_InvalidKeyType(t *testing.T) {
	svc, _ := newTestKeyService()
	_, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "k", KeyType: "sk"})
	require.Error(t, err)
}

func TestRevokeAPIKey_Idempotent(t *testing.T) {
	svc, tenants := newTestKeyService()
	require.NoError(t, tenants.Insert(context.Background(), &Tenant{ID: "tn-acme", Name: "acme", Status: StatusActive}))
	resp, err := svc.CreateAPIKey(context.Background(), "tn-acme", &CreateAPIKeyRequest{Name: "k", KeyType: KeyTypeTenant})
	require.NoError(t, err)

	require.NoError(t, svc.RevokeAPIKey(context.Background(), resp.ID))
	require.NoError(t, svc.RevokeAPIKey(context.Background(), resp.ID)) // idempotent

	keys, err := svc.ListAPIKeys(context.Background(), "tn-acme")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, KeyStatusRevoked, keys[0].Status)
}
