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

func (e *Extension) listInstrumentationRules(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
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

	rules, err := e.instrMgr.ListRules(r.Context(), query)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"rules": rules,
		"total": len(rules),
	})
}

func (e *Extension) createInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	req, err := decodeJSON[instrumentationmanager.CreateRuleRequest](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	rule, err := e.instrMgr.CreateRule(r.Context(), req)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instrumentation rule created", map[string]any{"rule": rule}))
}

func (e *Extension) getInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := e.instrMgr.GetRule(r.Context(), ruleID)
	if err != nil {
		e.handleError(w, errNotFound(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, rule)
}

func (e *Extension) updateInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	req, err := decodeJSON[instrumentationmanager.UpdateRuleRequest](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	rule, err := e.instrMgr.UpdateRule(r.Context(), ruleID, req)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instrumentation rule updated", map[string]any{"rule": rule}))
}

func (e *Extension) pauseInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := e.instrMgr.PauseRule(r.Context(), ruleID)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instrumentation rule paused", map[string]any{"rule": rule}))
}

func (e *Extension) resumeInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := e.instrMgr.ResumeRule(r.Context(), ruleID)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instrumentation rule resumed", map[string]any{"rule": rule}))
}

func (e *Extension) deleteInstrumentationRule(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	rule, err := e.instrMgr.DeleteRule(r.Context(), ruleID)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("instrumentation rule deleted", map[string]any{"rule": rule}))
}

func (e *Extension) listInstrumentationTargets(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	targets, err := e.instrMgr.ListTargetStatuses(r.Context(), ruleID)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, map[string]any{
		"targets": targets,
		"total":   len(targets),
	})
}

func (e *Extension) getInstrumentationRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	snapshot, err := e.instrMgr.GetRuleRuntimeSnapshot(r.Context(), ruleID)
	if err != nil {
		if errors.Is(err, instrumentationmanager.ErrRuleNotFound) {
			e.handleError(w, errNotFound(err.Error()))
			return
		}
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, snapshot)
}

func (e *Extension) refreshInstrumentationRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if e.instrMgr == nil {
		e.handleError(w, errInternal("instrumentation manager not available"))
		return
	}

	ruleID := chi.URLParam(r, "ruleID")
	snapshot, err := e.instrMgr.RefreshRuleRuntimeSnapshot(r.Context(), ruleID)
	if err != nil {
		if errors.Is(err, instrumentationmanager.ErrRuleNotFound) {
			e.handleError(w, errNotFound(err.Error()))
			return
		}
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	e.writeJSON(w, http.StatusOK, snapshot)
}
