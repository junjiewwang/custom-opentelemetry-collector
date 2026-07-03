// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
)

// retentionPolicyResponse is the JSON response for per-app retention queries.
type retentionPolicyResponse struct {
	Value           string `json:"value"`
	Source          string `json:"source"`
	PlatformDefault string `json:"platform_default"`
}

// handleAppRetention returns per-app retention + platform defaults.
// GET /api/v2/apps/{appID}/retention
func (e *Extension) handleAppRetention(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	if appID == "" {
		e.writeError(w, http.StatusBadRequest, "appID is required")
		return
	}
	if e.retentionProvider == nil {
		e.writeError(w, http.StatusServiceUnavailable, "retention provider not available")
		return
	}

	policy, err := e.retentionProvider.GetRetention(r.Context(), appID)
	if err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := buildRetentionResponse(policy)
	e.writeJSON(w, http.StatusOK, result)
}

// handleSetAppRetention sets per-app retention for a signal.
// PUT /api/v2/apps/{appID}/retention/{signal}
func (e *Extension) handleSetAppRetention(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	signalStr := chi.URLParam(r, "signal")
	if appID == "" || signalStr == "" {
		e.writeError(w, http.StatusBadRequest, "appID and signal are required")
		return
	}
	signal, ok := parseSignalType(signalStr)
	if !ok {
		e.writeError(w, http.StatusBadRequest, "signal must be trace, metric, or log")
		return
	}
	if e.retentionProvider == nil {
		e.writeError(w, http.StatusServiceUnavailable, "retention provider not available")
		return
	}

	var req struct {
		Duration string `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		e.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	dur, err := time.ParseDuration(req.Duration)
	if err != nil {
		e.writeError(w, http.StatusBadRequest, "invalid duration: "+err.Error())
		return
	}

	if err := e.retentionProvider.SetRetention(r.Context(), appID, signal, dur); err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("retention updated"))
}

// handleDeleteAppRetention removes per-app retention override.
// DELETE /api/v2/apps/{appID}/retention/{signal}
func (e *Extension) handleDeleteAppRetention(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "appID")
	signalStr := chi.URLParam(r, "signal")
	if appID == "" || signalStr == "" {
		e.writeError(w, http.StatusBadRequest, "appID and signal are required")
		return
	}
	signal, ok := parseSignalType(signalStr)
	if !ok {
		e.writeError(w, http.StatusBadRequest, "signal must be trace, metric, or log")
		return
	}
	if e.retentionProvider == nil {
		e.writeError(w, http.StatusServiceUnavailable, "retention provider not available")
		return
	}

	if err := e.retentionProvider.DeleteRetention(r.Context(), appID, signal); err != nil {
		e.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	e.writeJSON(w, http.StatusOK, successResponse("retention reset to platform default"))
}

// buildRetentionResponse converts a RetentionPolicy to the API response format.
func buildRetentionResponse(policy appmanager.RetentionPolicy) map[string]retentionPolicyResponse {
	result := make(map[string]retentionPolicyResponse)
	for _, signal := range appmanager.AllSignals() {
		d := policyDuration(policy, signal)
		source := "platform"
		if d > 0 {
			source = "app"
		}
		result[string(signal)] = retentionPolicyResponse{
			Value:           d.String(),
			Source:          source,
			PlatformDefault: platformDefaultFor(signal).String(),
		}
	}
	return result
}

// policyDuration returns the duration for the given signal from the policy.
func policyDuration(p appmanager.RetentionPolicy, signal appmanager.SignalType) time.Duration {
	switch signal {
	case appmanager.SignalTrace:
		return p.Trace
	case appmanager.SignalMetric:
		return p.Metric
	case appmanager.SignalLog:
		return p.Log
	default:
		return 0
	}
}

// parseSignalType parses a signal string into a SignalType.
func parseSignalType(s string) (appmanager.SignalType, bool) {
	switch s {
	case "trace":
		return appmanager.SignalTrace, true
	case "metric":
		return appmanager.SignalMetric, true
	case "log":
		return appmanager.SignalLog, true
	default:
		return "", false
	}
}

// platformDefaultFor returns the platform-level default retention for a signal.
// These values should be kept in sync with the platform config.
// TODO: Make these configurable, e.g., from ConfigManager or via AppRetentionProvider.
func platformDefaultFor(signal appmanager.SignalType) time.Duration {
	switch signal {
	case appmanager.SignalTrace:
		return 7 * 24 * time.Hour
	case appmanager.SignalMetric:
		return 30 * 24 * time.Hour
	case appmanager.SignalLog:
		return 14 * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}
