// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
)

// tenantCapabilityProvider resolves app → tenant → capability using the single
// shared app manager and tenant manager owned by the control plane. It never
// trusts a tenant ID supplied by the caller.
type tenantCapabilityProvider struct {
	apps           appmanager.AppManager
	tenants        tenantmanager.TenantManager
	defaultAllowed bool
}

var _ instrumentationmanager.CapabilityProvider = (*tenantCapabilityProvider)(nil)

func newTenantCapabilityProvider(
	apps appmanager.AppManager,
	tenants tenantmanager.TenantManager,
	defaultAllowed bool,
) *tenantCapabilityProvider {
	return &tenantCapabilityProvider{
		apps:           apps,
		tenants:        tenants,
		defaultAllowed: defaultAllowed,
	}
}

func (p *tenantCapabilityProvider) IsInstrumentationAllowed(ctx context.Context, appID string) (bool, error) {
	app := strings.TrimSpace(appID)
	if app == "" {
		return false, fmt.Errorf("app id is empty")
	}
	if p.apps == nil || p.tenants == nil {
		return false, fmt.Errorf("tenant capability provider is not initialized")
	}

	info, err := p.apps.GetApp(ctx, app)
	if err != nil {
		return false, fmt.Errorf("resolve app %q: %w", app, err)
	}
	if info == nil {
		return false, fmt.Errorf("app %q not found", app)
	}

	tenantID := info.TenantID
	if tenantID == "" {
		tenantID = tenantmanager.DefaultTenantID
	}
	tenant, err := p.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("resolve tenant %q: %w", tenantID, err)
	}
	if tenant == nil {
		return false, fmt.Errorf("tenant %q not found", tenantID)
	}

	if tenant.Capabilities == nil || tenant.Capabilities.DynamicInstrumentation == nil {
		return p.defaultAllowed, nil
	}
	return *tenant.Capabilities.DynamicInstrumentation, nil
}

// instrumentationCapabilityOptions wires the tenant capability check into the
// rule manager. Only the redis tenant repository is enabled (D9): memory mode
// is per-replica and must not expose a switch that cannot propagate.
func (e *Extension) instrumentationCapabilityOptions() ([]instrumentationmanager.Option, error) {
	if e.tenantMgr == nil || e.tenantMgr.Type != "redis" {
		e.logger.Info("Tenant instrumentation capability check disabled (requires redis tenant manager)")
		return nil, nil
	}
	apps, ok := e.tokenMgr.(appmanager.AppManager)
	if !ok || apps == nil {
		return nil, fmt.Errorf("redis tenant manager requires a token manager implementing AppManager")
	}

	defaultAllowed := true
	if e.config != nil {
		if configured := e.config.InstrumentationManager.DefaultTenantInstrumentationEnabled; configured != nil {
			defaultAllowed = *configured
		}
	}
	e.logger.Info("Tenant instrumentation capability check enabled",
		zap.String("tenant_manager_type", e.tenantMgr.Type),
		zap.Bool("default_allowed", defaultAllowed))
	return []instrumentationmanager.Option{
		instrumentationmanager.WithCapabilityProvider(
			newTenantCapabilityProvider(apps, e.tenantMgr.Tenants, defaultAllowed)),
	}, nil
}
