// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// ArthasAgentResponse represents an agent with Arthas tunnel connection.
type ArthasAgentResponse struct {
	AgentID      string `json:"agentId"`
	AppID        string `json:"appId"`
	ServiceName  string `json:"serviceName"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Version      string `json:"version"`
	ConnectedAt  int64  `json:"connectedAt"`
	LastPingAt   int64  `json:"lastPingAt"`
	ArthasStatus string `json:"arthasStatus"` // "running", "stopped", "unknown"
}

// ArthasStatusResponse represents the Arthas status of an agent.
type ArthasStatusResponse struct {
	AgentID        string `json:"agentId"`
	State          string `json:"state"`
	ArthasVersion  string `json:"arthasVersion,omitempty"`
	ActiveSessions int    `json:"activeSessions"`
	MaxSessions    int    `json:"maxSessions"`
	UptimeMs       int64  `json:"uptimeMs"`
}

// listArthasAgents returns all agents with active tunnel connections.
func (e *Extension) listArthasAgents(w http.ResponseWriter, r *http.Request) {
	if e.arthasTunnel == nil {
		http.Error(w, `{"error":"Arthas tunnel not configured"}`, http.StatusServiceUnavailable)
		return
	}

	agents := e.arthasTunnel.ListConnectedAgents()

	response := make([]ArthasAgentResponse, 0, len(agents))
	for _, agent := range agents {
		arthasStatus := "unknown"
		if agent.ArthasStatus != nil {
			switch agent.ArthasStatus.State {
			case "running":
				arthasStatus = "running"
			case "stopped", "":
				arthasStatus = "stopped"
			default:
				arthasStatus = agent.ArthasStatus.State
			}
		}

		response = append(response, ArthasAgentResponse{
			AgentID:      agent.AgentID,
			AppID:        agent.AppID,
			ServiceName:  agent.ServiceName,
			Hostname:     agent.Hostname,
			IP:           agent.IP,
			Version:      agent.Version,
			ConnectedAt:  agent.ConnectedAt.UnixMilli(),
			LastPingAt:   agent.LastPingAt.UnixMilli(),
			ArthasStatus: arthasStatus,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		e.logger.Error("Failed to encode response", zap.Error(err))
	}
}

// getAgentArthasStatus returns the Arthas status for a specific agent.
func (e *Extension) getAgentArthasStatus(w http.ResponseWriter, r *http.Request) {
	if e.arthasTunnel == nil {
		http.Error(w, `{"error":"Arthas tunnel not configured"}`, http.StatusServiceUnavailable)
		return
	}

	agentID := chi.URLParam(r, "agentID")
	if agentID == "" {
		http.Error(w, `{"error":"agentID is required"}`, http.StatusBadRequest)
		return
	}

	status, err := e.arthasTunnel.GetAgentArthasStatus(agentID)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}

	response := ArthasStatusResponse{
		AgentID:        agentID,
		State:          status.State,
		ArthasVersion:  status.ArthasVersion,
		ActiveSessions: status.ActiveSessions,
		MaxSessions:    status.MaxSessions,
		UptimeMs:       status.UptimeMs,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		e.logger.Error("Failed to encode response", zap.Error(err))
	}
}

// handleArthasWebSocket handles WebSocket connections from browsers for Arthas terminal.
func (e *Extension) handleArthasWebSocket(w http.ResponseWriter, r *http.Request) {
	if e.arthasTunnel == nil {
		http.Error(w, "Arthas tunnel not configured", http.StatusServiceUnavailable)
		return
	}

	e.arthasTunnel.HandleBrowserWebSocket(w, r)
}
