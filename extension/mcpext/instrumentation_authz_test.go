// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

type stubTokenManager struct {
	appmanager.TokenManager
	apps map[string]*appmanager.AppInfo
}

func (s *stubTokenManager) GetApp(_ context.Context, appID string) (*appmanager.AppInfo, error) {
	app, ok := s.apps[appID]
	if !ok {
		return nil, errors.New("app not found")
	}
	return app, nil
}

type controlPlaneWithTenants struct {
	*stubControlPlane
	tm *stubTokenManager
}

func (c *controlPlaneWithTenants) GetTokenManager() appmanager.TokenManager {
	return c.tm
}

func tenantScopedContext(tenantID string) context.Context {
	return tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType:  tenantauth.KeyTypeTenant,
		TenantID: tenantID,
		KeyID:    "key-t",
	})
}

func globalMCPContext(keyType string) context.Context {
	return tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType: keyType,
		KeyID:   "key-g",
	})
}

func tenantAwareControlPlane() *controlPlaneWithTenants {
	return &controlPlaneWithTenants{
		stubControlPlane: &stubControlPlane{},
		tm: &stubTokenManager{apps: map[string]*appmanager.AppInfo{
			"app-a": {ID: "app-a", TenantID: "tn-a"},
			"app-b": {ID: "app-b", TenantID: "tn-b"},
		}},
	}
}

func TestAuthorizeGlobalIdentities(t *testing.T) {
	a := &instrumentationAuthorizer{cp: &stubControlPlane{}}
	require.NoError(t, a.authorize(globalMCPContext(tenantauth.KeyTypeSuper), "app-a"))
	require.NoError(t, a.authorize(globalMCPContext(tenantauth.KeyTypeOperator), "app-b"))
}

func TestAuthorizeTenantScopeFailsClosed(t *testing.T) {
	a := &instrumentationAuthorizer{cp: tenantAwareControlPlane()}

	require.NoError(t, a.authorize(tenantScopedContext("tn-a"), "app-a"))
	require.Error(t, a.authorize(tenantScopedContext("tn-a"), "app-b"))
	require.Error(t, a.authorize(context.Background(), "app-a"))
	require.Error(t, a.authorize(tenantScopedContext("tn-a"), "unknown-app"))

	missingResolver := &instrumentationAuthorizer{cp: &stubControlPlane{}}
	require.Error(t, missingResolver.authorize(tenantScopedContext("tn-a"), "app-a"))
}

func TestFilterRulesByTenant(t *testing.T) {
	a := &instrumentationAuthorizer{cp: tenantAwareControlPlane()}
	rules := []*instrumentationmanager.Rule{
		{ID: "r1", AppID: "app-a"},
		{ID: "r2", AppID: "app-b"},
		{ID: "r3", AppID: "app-a"},
	}

	filtered, err := a.filterRules(tenantScopedContext("tn-a"), rules)
	require.NoError(t, err)
	assert.Len(t, filtered, 2)
	for _, rule := range filtered {
		assert.Equal(t, "app-a", rule.AppID)
	}

	all, err := a.filterRules(globalMCPContext(tenantauth.KeyTypeSuper), rules)
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestCreateRejectsCrossTenant(t *testing.T) {
	mgr := newFakeInstrumentationManager()
	w := newInstrumentationTestWrapper(readWriteConfig(), mgr, tenantAwareControlPlane())
	args := map[string]any{
		"app_id":       "app-b",
		"service_name": "svc-b",
		"class_name":   "com.example.Service",
		"method_name":  "run",
	}

	result, err := w.handleInstrumentRuleCreate(tenantScopedContext("tn-a"), instrumentationCall(args))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, instrumentationText(t, result), "跨租户")
	assert.Nil(t, mgr.createReq)
}

func TestListHidesOtherTenantRules(t *testing.T) {
	mgr := newFakeInstrumentationManager()
	mgr.rules["r1"] = &instrumentationmanager.Rule{ID: "r1", AppID: "app-a", CreatedBy: "mcp:x"}
	mgr.rules["r2"] = &instrumentationmanager.Rule{ID: "r2", AppID: "app-b", CreatedBy: "mcp:x"}
	w := newInstrumentationTestWrapper(readWriteConfig(), mgr, tenantAwareControlPlane())

	result, err := w.handleInstrumentRuleList(tenantScopedContext("tn-a"), instrumentationCall(map[string]any{}))
	require.NoError(t, err)
	text := instrumentationText(t, result)
	assert.Contains(t, text, "r1")
	assert.NotContains(t, text, "r2")
}

func TestActionRejectsCrossTenantRule(t *testing.T) {
	mgr := newFakeInstrumentationManager()
	mgr.rules["r1"] = &instrumentationmanager.Rule{ID: "r1", AppID: "app-b", CreatedBy: "mcp:x"}
	w := newInstrumentationTestWrapper(readWriteConfig(), mgr, tenantAwareControlPlane())

	result, err := w.handleInstrumentRuleAction(
		tenantScopedContext("tn-a"),
		instrumentationCall(map[string]any{"rule_id": "r1"}),
		"instrument_rule_pause",
		mgr.applyAction("pause"),
	)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, instrumentationText(t, result), "租户授权失败")
	assert.Empty(t, mgr.actions)
}
