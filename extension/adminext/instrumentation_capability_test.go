// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
)

type stubCapabilityAppManager struct {
	appmanager.AppManager
	apps map[string]*appmanager.AppInfo
	err  error
}

func (s *stubCapabilityAppManager) GetApp(_ context.Context, id string) (*appmanager.AppInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	app, ok := s.apps[id]
	if !ok {
		return nil, errors.New("app not found")
	}
	return app, nil
}

type stubCapabilityTenantManager struct {
	tenantmanager.TenantManager
	tenants map[string]*tenantmanager.Tenant
	err     error
}

func (s *stubCapabilityTenantManager) GetTenant(_ context.Context, id string) (*tenantmanager.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	tenant, ok := s.tenants[id]
	if !ok {
		return nil, errors.New("tenant not found")
	}
	return tenant, nil
}

func capabilityTestProvider(capability *bool) *tenantCapabilityProvider {
	apps := &stubCapabilityAppManager{apps: map[string]*appmanager.AppInfo{
		"app-a":      {ID: "app-a", TenantID: "tn-a"},
		"app-legacy": {ID: "app-legacy"},
	}}
	tenants := &stubCapabilityTenantManager{tenants: map[string]*tenantmanager.Tenant{
		"tn-a": {ID: "tn-a", Status: tenantmanager.StatusActive},
		tenantmanager.DefaultTenantID: {
			ID:           tenantmanager.DefaultTenantID,
			Status:       tenantmanager.StatusActive,
			Capabilities: &tenantmanager.TenantCapabilities{DynamicInstrumentation: capability},
		},
	}}
	return newTenantCapabilityProvider(apps, tenants, true)
}

func TestTenantCapabilityProvider(t *testing.T) {
	tests := []struct {
		name       string
		appID      string
		capability *bool
		want       bool
		wantErr    bool
	}{
		{name: "legacy tenant inherits allowed", appID: "app-a", want: true},
		{name: "explicit enabled", appID: "app-a", capability: tenantBool(true), want: true},
		{name: "explicit disabled", appID: "app-a", capability: tenantBool(false), want: false},
		{name: "empty app id fails closed", appID: "", wantErr: true},
		{name: "unknown app fails closed", appID: "missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Put the capability on tn-a for the enabled/disabled cases.
			provider := capabilityTestProvider(nil)
			if tt.capability != nil {
				allowed := *tt.capability
				provider.tenants.(*stubCapabilityTenantManager).tenants["tn-a"] = &tenantmanager.Tenant{
					ID:           "tn-a",
					Status:       tenantmanager.StatusActive,
					Capabilities: &tenantmanager.TenantCapabilities{DynamicInstrumentation: &allowed},
				}
			}
			got, err := provider.IsInstrumentationAllowed(context.Background(), tt.appID)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTenantCapabilityProviderLegacyAppTenant(t *testing.T) {
	provider := capabilityTestProvider(tenantBool(false))
	allowed, err := provider.IsInstrumentationAllowed(context.Background(), "app-legacy")
	require.NoError(t, err)
	assert.False(t, allowed, "an app without TenantID must resolve to the default tenant")
}

func TestTenantCapabilityProviderErrors(t *testing.T) {
	provider := capabilityTestProvider(nil)

	provider.apps = &stubCapabilityAppManager{err: errors.New("backend down")}
	_, err := provider.IsInstrumentationAllowed(context.Background(), "app-a")
	require.Error(t, err)

	provider = capabilityTestProvider(nil)
	provider.tenants = &stubCapabilityTenantManager{err: errors.New("backend down")}
	_, err = provider.IsInstrumentationAllowed(context.Background(), "app-a")
	require.Error(t, err)
}

func TestTenantCapabilityProviderDeploymentDefault(t *testing.T) {
	provider := capabilityTestProvider(nil)
	provider.defaultAllowed = false

	// tn-a has no explicit capability, so it inherits the deployment default.
	allowed, err := provider.IsInstrumentationAllowed(context.Background(), "app-a")
	require.NoError(t, err)
	assert.False(t, allowed)

	// An explicit tenant override always wins over the deployment default.
	explicit := true
	provider.tenants.(*stubCapabilityTenantManager).tenants["tn-a"] = &tenantmanager.Tenant{
		ID:           "tn-a",
		Status:       tenantmanager.StatusActive,
		Capabilities: &tenantmanager.TenantCapabilities{DynamicInstrumentation: &explicit},
	}
	allowed, err = provider.IsInstrumentationAllowed(context.Background(), "app-a")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestInstrumentationCapabilityOptionsMemoryMode(t *testing.T) {
	ext := &Extension{
		logger:    zap.NewNop(),
		tenantMgr: &tenantmanager.MultiTenantManager{Type: "memory"},
	}
	opts, err := ext.instrumentationCapabilityOptions()
	require.NoError(t, err)
	assert.Empty(t, opts, "memory mode must not enable a switch that cannot propagate across replicas")

	withoutTenantMgr := &Extension{logger: zap.NewNop()}
	opts, err = withoutTenantMgr.instrumentationCapabilityOptions()
	require.NoError(t, err)
	assert.Empty(t, opts)
}

func tenantBool(v bool) *bool { return &v }
