// Copyright The Open Telemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// appTenantProvider is a deliberately narrow interface: the token manager's
// concrete implementation also exposes app lookup.
type appTenantProvider interface {
	GetApp(ctx context.Context, appID string) (*appmanager.AppInfo, error)
}

func (h *adminHandlers) tenantOfApp(ctx context.Context, appID string) (string, error) {
	provider, ok := h.tokenMgr.(appTenantProvider)
	if !ok {
		return "", errors.New("token manager does not expose app lookup")
	}
	app, err := provider.GetApp(ctx, strings.TrimSpace(appID))
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

// authorizeTenantRule enforces tenant-scoped access to a single rule.
// Global identities (static super / ok_) may act across tenants. tk_ keys are
// bound to the tenant resolved from the key; X-Tenant-Id is ignored for them
// by the shared auth middleware. Anything unresolved fails closed.
func (h *adminHandlers) authorizeTenantRule(r *http.Request, rule *instrumentationmanager.Rule) *APIError {
	if rule == nil {
		return errNotFound("rule not found")
	}
	identity, ok := tenantauth.IdentityFromContext(r.Context())
	if !ok {
		return &APIError{Status: http.StatusForbidden, Message: "missing authenticated identity"}
	}
	if identity.IsGlobal() {
		return nil
	}
	tenant, err := h.tenantOfApp(r.Context(), rule.AppID)
	if err != nil {
		return &APIError{Status: http.StatusForbidden, Message: "cannot resolve app tenant: " + err.Error()}
	}
	if tenant != identity.TenantID {
		return &APIError{Status: http.StatusForbidden, Message: "rule belongs to a different tenant"}
	}
	return nil
}

// listTenantInstrumentationRules returns deleted rules visible to the caller,
// optionally limited to those still missing uninstall evidence.
func (h *adminHandlers) listTenantInstrumentationRules(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}
	query := instrumentationmanager.ListRulesQuery{
		AppID:           strings.TrimSpace(r.URL.Query().Get("app_id")),
		ServiceName:     strings.TrimSpace(r.URL.Query().Get("service_name")),
		IncludeDeleted:  true,
		UnconfirmedOnly: strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("unconfirmed")), "true"),
		DesiredState:    instrumentationmanager.RuleDesiredStateDeleted,
	}
	rules, err := h.instrMgr.ListRules(r.Context(), query)
	if err != nil {
		h.handleError(w, instrumentationError(err))
		return
	}
	identity, _ := tenantauth.IdentityFromContext(r.Context())
	out := make([]*instrumentationmanager.Rule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		if !identity.IsGlobal() {
			if apiErr := h.authorizeTenantRule(r, rule); apiErr != nil {
				continue
			}
		}
		out = append(out, rule)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"rules": out, "total": len(out)})
}

type purgeInstrumentationRuleRequest struct {
	Reason string `json:"reason"`
}

// purgeTenantInstrumentationRule physically removes a deleted rule. Allowed
// for the tenant owning the rule's app (or global identities). No scope is
// required in phase 1; app ownership plus deleted state plus audit is the
// authorization boundary.
func (h *adminHandlers) purgeTenantInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}
	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.GetRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, instrumentationError(err))
		return
	}
	if rule.DesiredState != instrumentationmanager.RuleDesiredStateDeleted {
		h.handleError(w, errConflict("only deleted rules can be purged"))
		return
	}
	if apiErr := h.authorizeTenantRule(r, rule); apiErr != nil {
		h.handleError(w, apiErr)
		return
	}
	req, err := decodeJSON[purgeInstrumentationRuleRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		h.handleError(w, errBadRequest("reason is required"))
		return
	}
	if err := h.instrMgr.PurgeRule(r.Context(), ruleID, reason); err != nil {
		h.handleError(w, instrumentationError(err))
		return
	}
	identity, _ := tenantauth.IdentityFromContext(r.Context())
	h.logger.Info("Tenant instrumentation rule purged",
		zap.String("rule_id", ruleID),
		zap.String("app_id", rule.AppID),
		zap.String("tenant_id", identity.TenantID),
		zap.String("key_id", identity.KeyID),
		zap.String("reason", reason),
	)
	h.writeJSON(w, http.StatusOK, map[string]any{"purged": ruleID})
}

// verifyTenantInstrumentationRule lets an operator trigger evidence refresh
// for a deleted rule without waiting for the background verifier.
func (h *adminHandlers) verifyTenantInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}
	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.GetRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, instrumentationError(err))
		return
	}
	if apiErr := h.authorizeTenantRule(r, rule); apiErr != nil {
		h.handleError(w, apiErr)
		return
	}
	updated, err := h.instrMgr.VerifyUninstall(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, instrumentationError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"rule": updated})
}
