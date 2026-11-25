// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBool(v bool) *bool { return &v }

func TestUpdateTenantClearDynamicInstrumentation(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantService(NewMemoryTenantRepository(), NewIDGenerator(), fakeAppLister{}, nil)

	created, err := svc.CreateTenant(ctx, &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)
	_, err = svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		DynamicInstrumentation: testBool(false),
	})
	require.NoError(t, err)

	cleared, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		ClearDynamicInstrumentation: true,
	})
	require.NoError(t, err)
	assert.Nil(t, cleared.Capabilities)

	stored, err := svc.GetTenant(ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.Capabilities, "the override must be removed from storage")

	// Clear wins when both fields are supplied, avoiding an ambiguous request.
	_, err = svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		ClearDynamicInstrumentation: true,
		DynamicInstrumentation:      testBool(true),
	})
	require.NoError(t, err)
	stored, err = svc.GetTenant(ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.Capabilities)
}

func TestTenantCapabilitiesLegacyJSON(t *testing.T) {
	var tenant Tenant
	require.NoError(t, json.Unmarshal(
		[]byte(`{"id":"tn-1","name":"acme","status":"active"}`), &tenant))
	assert.Nil(t, tenant.Capabilities)
}

func TestUpdateTenantDynamicInstrumentationTriState(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantService(NewMemoryTenantRepository(), NewIDGenerator(), fakeAppLister{}, nil)

	created, err := svc.CreateTenant(ctx, &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)
	assert.Nil(t, created.Capabilities, "new tenants inherit the deployment default")

	disabled, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		DynamicInstrumentation: testBool(false),
	})
	require.NoError(t, err)
	require.NotNil(t, disabled.Capabilities)
	require.NotNil(t, disabled.Capabilities.DynamicInstrumentation)
	assert.False(t, *disabled.Capabilities.DynamicInstrumentation)

	unchanged, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{Name: "acme-renamed"})
	require.NoError(t, err)
	assert.Equal(t, "acme-renamed", unchanged.Name)
	require.NotNil(t, unchanged.Capabilities)
	assert.False(t, *unchanged.Capabilities.DynamicInstrumentation,
		"omitting the field must leave the capability unchanged")

	before, err := svc.GetTenant(ctx, created.ID)
	require.NoError(t, err)
	enabled, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		DynamicInstrumentation: testBool(true),
	})
	require.NoError(t, err)
	assert.True(t, *enabled.Capabilities.DynamicInstrumentation)
	assert.False(t, *before.Capabilities.DynamicInstrumentation,
		"the stored record must not be mutated in place by UpdateTenant")

	stored, err := svc.GetTenant(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.Capabilities)
	assert.True(t, *stored.Capabilities.DynamicInstrumentation)
}
