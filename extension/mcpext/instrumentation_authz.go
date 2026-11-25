// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// tokenManagerProvider is an optional capability of the control plane. The
// concrete controlplaneext.Extension exposes the shared token manager, whose
// implementation also provides app lookup.
type tokenManagerProvider interface {
	GetTokenManager() appmanager.TokenManager
}

// appTenantProvider is deliberately narrow: only the app lookup needed to
// derive the tenant of a rule target (ISP).
type appTenantProvider interface {
	GetApp(ctx context.Context, appID string) (*appmanager.AppInfo, error)
}

// instrumentationAuthorizer enforces tenant-scoped access to instrumentation
// rules. Global identities (static super / ok_) may act across tenants; tk_
// keys are hard-bound to the tenant resolved from the key, never from a header.
type instrumentationAuthorizer struct {
	cp controlplaneext.ControlPlane
}

func (w *mcpServerWrapper) instrumentationAuthorizer() *instrumentationAuthorizer {
	return &instrumentationAuthorizer{cp: w.ext.controlPlane}
}

func (a *instrumentationAuthorizer) tenantOfApp(ctx context.Context, appID string) (string, error) {
	provider, ok := a.cp.(tokenManagerProvider)
	if !ok {
		return "", errors.New("control plane does not expose an app tenant resolver")
	}
	resolver, ok := provider.GetTokenManager().(appTenantProvider)
	if !ok {
		return "", errors.New("token manager does not expose app lookup")
	}
	app, err := resolver.GetApp(ctx, strings.TrimSpace(appID))
	if err != nil {
		return "", err
	}
	if app == nil {
		return "", errors.New("app not found")
	}
	if app.TenantID == "" {
		return tenantmanager.DefaultTenantID, nil
	}
	return app.TenantID, nil
}

// authorize checks that the caller may act on rules targeting appID.
// It fails closed when the identity or the target tenant cannot be resolved.
func (a *instrumentationAuthorizer) authorize(ctx context.Context, appID string) error {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return errors.New("MCP 请求缺少已认证的租户身份")
	}
	if identity.IsGlobal() {
		return nil
	}
	tenant, err := a.tenantOfApp(ctx, appID)
	if err != nil {
		return fmt.Errorf("无法确认目标应用所属租户: %w", err)
	}
	if tenant != identity.TenantID {
		return errors.New("禁止跨租户操作：调用者租户与目标应用所属租户不一致")
	}
	return nil
}

func (a *instrumentationAuthorizer) authorizeRule(ctx context.Context, rule *instrumentationmanager.Rule) error {
	if rule == nil {
		return errors.New("规则不存在")
	}
	return a.authorize(ctx, rule.AppID)
}

// filterRules hides rules belonging to other tenants from tk_ callers.
func (a *instrumentationAuthorizer) filterRules(
	ctx context.Context,
	rules []*instrumentationmanager.Rule,
) ([]*instrumentationmanager.Rule, error) {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("MCP 请求缺少已认证的租户身份")
	}
	if identity.IsGlobal() {
		return rules, nil
	}

	tenants := make(map[string]string)
	out := make([]*instrumentationmanager.Rule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		tenant, cached := tenants[rule.AppID]
		if !cached {
			var err error
			tenant, err = a.tenantOfApp(ctx, rule.AppID)
			if err != nil {
				return nil, fmt.Errorf("无法确认规则目标租户: %w", err)
			}
			tenants[rule.AppID] = tenant
		}
		if tenant == identity.TenantID {
			out = append(out, rule)
		}
	}
	return out, nil
}
