// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/opsconfig"
)

// opsConfigView is the read model for the admin-managed runtime config page.
type opsConfigView struct {
	Revision int64                   `json:"revision"`
	Configs  map[string]string       `json:"configs"`
	Schema   []opsconfig.SchemaEntry `json:"schema"`
}

func (h *adminHandlers) opsUnavailable(w http.ResponseWriter) bool {
	if h.opsStore == nil {
		h.writeError(w, http.StatusServiceUnavailable, "ops config store unavailable")
		return true
	}
	return false
}

func (h *adminHandlers) getOpsConfigs(w http.ResponseWriter, r *http.Request) {
	if h.opsUnavailable(w) {
		return
	}
	values, revision := h.opsStore.Snapshot()
	h.writeJSON(w, http.StatusOK, opsConfigView{
		Revision: revision,
		Configs:  values,
		Schema:   opsconfig.Schema(),
	})
}

func (h *adminHandlers) setOpsConfig(w http.ResponseWriter, r *http.Request) {
	if h.opsUnavailable(w) {
		return
	}
	key := chi.URLParam(r, "key")
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	operator := KeyTypeFromContext(r.Context())
	if operator == "" {
		operator = "admin"
	}
	revision, err := h.opsStore.Put(r.Context(), key, body.Value, operator)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	h.logger.Info("ops config updated",
		zap.String("key", key),
		zap.String("operator", operator),
		zap.Int64("revision", revision),
	)
	h.writeJSON(w, http.StatusOK, map[string]any{"key": key, "revision": revision})
}

func (h *adminHandlers) deleteOpsConfig(w http.ResponseWriter, r *http.Request) {
	if h.opsUnavailable(w) {
		return
	}
	key := chi.URLParam(r, "key")
	operator := KeyTypeFromContext(r.Context())
	if operator == "" {
		operator = "admin"
	}
	revision, err := h.opsStore.Delete(r.Context(), key, operator)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	h.logger.Info("ops config deleted",
		zap.String("key", key),
		zap.String("operator", operator),
		zap.Int64("revision", revision),
	)
	h.writeJSON(w, http.StatusOK, map[string]any{"key": key, "revision": revision})
}

func (h *adminHandlers) getOpsConfigAudit(w http.ResponseWriter, r *http.Request) {
	if h.opsUnavailable(w) {
		return
	}
	entries, err := h.opsStore.Audit(r.Context(), 20)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// getOpsConfigKeys returns the stable, sorted schema for the frontend form.
func (h *adminHandlers) getOpsConfigKeys(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]any{"schema": opsconfig.Schema()})
}

// writeOpsError separates client-side validation failures from backend
// failures and never leaks internal error details to the caller.
func (h *adminHandlers) writeOpsError(w http.ResponseWriter, err error) {
	if errors.Is(err, opsconfig.ErrValidation) {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Warn("ops config backend error", zap.Error(err))
	h.writeError(w, http.StatusServiceUnavailable, "ops config store temporarily unavailable")
}
