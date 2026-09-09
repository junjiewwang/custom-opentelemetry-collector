// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
)

// tenantError maps a tenantmanager sentinel error to the HTTP API error.
func tenantError(err error) *APIError {
	switch {
	case errors.Is(err, tenantmanager.ErrNotFound), errors.Is(err, tenantmanager.ErrKeyNotFound):
		return errNotFound(err.Error())
	case errors.Is(err, tenantmanager.ErrTenantNameExists), errors.Is(err, tenantmanager.ErrTenantNotEmpty):
		return errConflict(err.Error())
	case errors.Is(err, tenantmanager.ErrDefaultTenantImmutable):
		return errBadRequest(err.Error())
	default:
		return errInternal(err.Error())
	}
}

// ── Tenant CRUD ─────────────────────────────────────

func (h *adminHandlers) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.tenantMgr.Tenants.ListTenants(r.Context())
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, listResponse("tenants", tenants, len(tenants)))
}

func (h *adminHandlers) createTenant(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[tenantmanager.CreateTenantRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}
	tenant, err := h.tenantMgr.Tenants.CreateTenant(r.Context(), req)
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.logger.Info("Tenant created via API", zap.String("id", tenant.ID), zap.String("name", tenant.Name))
	h.writeJSON(w, http.StatusCreated, tenant)
}

func (h *adminHandlers) getTenant(w http.ResponseWriter, r *http.Request) {
	tenant, err := h.tenantMgr.Tenants.GetTenant(r.Context(), chi.URLParam(r, "tenantID"))
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, tenant)
}

func (h *adminHandlers) updateTenant(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[tenantmanager.UpdateTenantRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}
	tenant, err := h.tenantMgr.Tenants.UpdateTenant(r.Context(), chi.URLParam(r, "tenantID"), req)
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, tenant)
}

func (h *adminHandlers) deleteTenant(w http.ResponseWriter, r *http.Request) {
	if err := h.tenantMgr.Tenants.DeleteTenant(r.Context(), chi.URLParam(r, "tenantID")); err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ── Tenant-App relationship ─────────────────────────

func (h *adminHandlers) listTenantApps(w http.ResponseWriter, r *http.Request) {
	appIDs, err := h.tenantMgr.Tenants.ListTenantApps(r.Context(), chi.URLParam(r, "tenantID"))
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, listResponse("apps", appIDs, len(appIDs)))
}

// ── Tenant API keys ─────────────────────────────────

func (h *adminHandlers) createTenantKey(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[tenantmanager.CreateAPIKeyRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}
	resp, err := h.tenantMgr.Keys.CreateAPIKey(r.Context(), chi.URLParam(r, "tenantID"), req)
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	// The plaintext key is returned exactly once here; it is never stored.
	h.writeJSON(w, http.StatusCreated, resp)
}

func (h *adminHandlers) listTenantKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.tenantMgr.Keys.ListAPIKeys(r.Context(), chi.URLParam(r, "tenantID"))
	if err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	// Strip KeyHash — never expose the SHA-256 hash to the API surface.
	type keyView struct {
		ID        string   `json:"id"`
		TenantID  string   `json:"tenant_id"`
		KeyType   string   `json:"key_type"`
		KeyPrefix string   `json:"key_prefix"`
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes,omitempty"`
		Status    string   `json:"status"`
	}
	view := make([]keyView, 0, len(keys))
	for _, k := range keys {
		view = append(view, keyView{
			ID: k.ID, TenantID: k.TenantID, KeyType: k.KeyType, KeyPrefix: k.KeyPrefix,
			Name: k.Name, Scopes: k.Scopes, Status: k.Status,
		})
	}
	h.writeJSON(w, http.StatusOK, listResponse("keys", view, len(view)))
}

func (h *adminHandlers) revokeTenantKey(w http.ResponseWriter, r *http.Request) {
	if err := h.tenantMgr.Keys.RevokeAPIKey(r.Context(), chi.URLParam(r, "keyID")); err != nil {
		h.handleError(w, tenantError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}
