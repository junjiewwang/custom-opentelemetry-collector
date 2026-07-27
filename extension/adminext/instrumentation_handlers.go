// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

func (h *adminHandlers) listInstrumentationRules(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	query := instrumentationmanager.ListRulesQuery{
		AppID:          strings.TrimSpace(r.URL.Query().Get("app_id")),
		ServiceName:    strings.TrimSpace(r.URL.Query().Get("service_name")),
		InstrumentType: instrumentationmanager.InstrumentType(strings.TrimSpace(r.URL.Query().Get("instrument_type"))),
		DesiredState:   instrumentationmanager.RuleDesiredState(strings.TrimSpace(r.URL.Query().Get("desired_state"))),
		Search:         strings.TrimSpace(r.URL.Query().Get("search")),
		IncludeDeleted: strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_deleted")), "true"),
	}

	rules, err := h.instrMgr.ListRules(r.Context(), query)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"rules": rules,
		"total": len(rules),
	})
}

func (h *adminHandlers) createInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	req, err := decodeJSON[instrumentationmanager.CreateRuleRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	rule, err := h.instrMgr.CreateRule(r.Context(), req)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instrumentation rule created", map[string]any{"rule": rule}))
}

func (h *adminHandlers) getInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.GetRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, errNotFound(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, rule)
}

func (h *adminHandlers) updateInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	req, err := decodeJSON[instrumentationmanager.UpdateRuleRequest](r)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	rule, err := h.instrMgr.UpdateRule(r.Context(), ruleID, req)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instrumentation rule updated", map[string]any{"rule": rule}))
}

func (h *adminHandlers) pauseInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.PauseRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instrumentation rule paused", map[string]any{"rule": rule}))
}

func (h *adminHandlers) resumeInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.ResumeRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instrumentation rule resumed", map[string]any{"rule": rule}))
}

func (h *adminHandlers) deleteInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := h.instrMgr.DeleteRule(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, successResponse("instrumentation rule deleted", map[string]any{"rule": rule}))
}

func (h *adminHandlers) listInstrumentationTargets(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	targets, err := h.instrMgr.ListTargetStatuses(r.Context(), ruleID)
	if err != nil {
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"targets": targets,
		"total":   len(targets),
	})
}

func (h *adminHandlers) getInstrumentationRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	snapshot, err := h.instrMgr.GetRuleRuntimeSnapshot(r.Context(), ruleID)
	if err != nil {
		if errors.Is(err, instrumentationmanager.ErrRuleNotFound) {
			h.handleError(w, errNotFound(err.Error()))
			return
		}
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, snapshot)
}

func (h *adminHandlers) refreshInstrumentationRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.instrMgr == nil {
		h.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	snapshot, err := h.instrMgr.RefreshRuleRuntimeSnapshot(r.Context(), ruleID)
	if err != nil {
		if errors.Is(err, instrumentationmanager.ErrRuleNotFound) {
			h.handleError(w, errNotFound(err.Error()))
			return
		}
		h.handleError(w, errBadRequest(err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, snapshot)
}
