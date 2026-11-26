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

func intPtr(v int) *int          { return &v }
func stringPtr(v string) *string { return &v }

func TestUpdateTenantObservabilityQueryReplaceAndClear(t *testing.T) {
	ctx := context.Background()
	svc := NewTenantService(NewMemoryTenantRepository(), NewIDGenerator(), fakeAppLister{}, nil)
	created, err := svc.CreateTenant(ctx, &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)

	full := &TenantObservabilityQueryConfig{
		QueryTimeout:    intPtr(45),
		MaxTimeRange:    stringPtr("48h"),
		MetricSeries:    intPtr(200),
		Traces:          intPtr(80),
		Logs:            intPtr(300),
		LogContextLines: intPtr(120),
	}
	updated, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{ObservabilityQuery: full})
	require.NoError(t, err)
	require.NotNil(t, updated.Capabilities)
	require.NotNil(t, updated.Capabilities.ObservabilityQuery)
	assert.Equal(t, 45, *updated.Capabilities.ObservabilityQuery.QueryTimeout)
	assert.Equal(t, "48h", *updated.Capabilities.ObservabilityQuery.MaxTimeRange)
	assert.Equal(t, 200, *updated.Capabilities.ObservabilityQuery.MetricSeries)

	// Whole-object replacement: omitted fields are cleared back to inherit.
	partial, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		ObservabilityQuery: &TenantObservabilityQueryConfig{Traces: intPtr(5)},
	})
	require.NoError(t, err)
	cfg := partial.Capabilities.ObservabilityQuery
	assert.Equal(t, 5, *cfg.Traces)
	assert.Nil(t, cfg.QueryTimeout)
	assert.Nil(t, cfg.MaxTimeRange)
	assert.Nil(t, cfg.MetricSeries)
	assert.Nil(t, cfg.Logs)
	assert.Nil(t, cfg.LogContextLines)

	cleared, err := svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{ClearObservabilityQuery: true})
	require.NoError(t, err)
	if cleared.Capabilities != nil {
		assert.Nil(t, cleared.Capabilities.ObservabilityQuery)
	}

	// Clear also wins when both fields are supplied.
	_, err = svc.UpdateTenant(ctx, created.ID, &UpdateTenantRequest{
		ClearObservabilityQuery: true,
		ObservabilityQuery:      &TenantObservabilityQueryConfig{Traces: intPtr(9)},
	})
	require.NoError(t, err)
	stored, err := svc.GetTenant(ctx, created.ID)
	require.NoError(t, err)
	if stored.Capabilities != nil {
		assert.Nil(t, stored.Capabilities.ObservabilityQuery)
	}
}

func TestObservabilityQueryConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  *TenantObservabilityQueryConfig
		bad  bool
	}{
		{"nil", nil, false},
		{"valid", &TenantObservabilityQueryConfig{QueryTimeout: intPtr(30), MaxTimeRange: stringPtr("72h")}, false},
		{"zero timeout", &TenantObservabilityQueryConfig{QueryTimeout: intPtr(0)}, true},
		{"negative logs", &TenantObservabilityQueryConfig{Logs: intPtr(-1)}, true},
		{"bad duration", &TenantObservabilityQueryConfig{MaxTimeRange: stringPtr("tomorrow")}, true},
		{"zero duration", &TenantObservabilityQueryConfig{MaxTimeRange: stringPtr("0s")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateObservabilityQueryConfig(tc.cfg)
			if tc.bad {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
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
