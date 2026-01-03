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

// WSTokenRequest represents a request to generate a WebSocket token.
type WSTokenRequest struct {
	Purpose string `json:"purpose"` // e.g., "arthas_terminal"
}

// WSTokenResponse represents the response containing a WebSocket token.
type WSTokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"` // seconds until expiration
}

// generateWSToken generates a short-lived token for WebSocket authentication.
// This allows secure WebSocket connections without exposing API keys in URLs.
func (e *Extension) generateWSToken(w http.ResponseWriter, r *http.Request) {
	var req WSTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default purpose if not specified
		req.Purpose = "arthas_terminal"
	}

	if req.Purpose == "" {
		req.Purpose = "arthas_terminal"
	}

	// Generate token (userID can be extracted from auth context if needed)
	token := e.wsTokenMgr.GenerateToken("", req.Purpose)

	response := WSTokenResponse{
		Token:     token.Token,
		ExpiresIn: 30, // 30 seconds
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		e.logger.Error("Failed to encode response", zap.Error(err))
	}
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
// Authentication is done via short-lived WS token (obtained from POST /api/v1/auth/ws-token).
func (e *Extension) handleArthasWebSocket(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	token := r.URL.Query().Get("token")

	e.logger.Info("Arthas WebSocket connection request received",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("agent_id", agentID),
		zap.Bool("has_token", token != ""),
	)

	if e.arthasTunnel == nil {
		e.logger.Error("Arthas tunnel not configured")
		http.Error(w, "Arthas tunnel not configured", http.StatusServiceUnavailable)
		return
	}

	// Validate WS token (single-use, consumed on validation)
	if token == "" {
		e.logger.Warn("WebSocket connection rejected: no token provided",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "Unauthorized: token required", http.StatusUnauthorized)
		return
	}

	tokenInfo := e.wsTokenMgr.ValidateAndConsume(token, "arthas_terminal")
	if tokenInfo == nil {
		e.logger.Warn("WebSocket connection rejected: invalid or expired token",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
		return
	}

	e.logger.Debug("WebSocket token validated",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("agent_id", agentID),
	)

	e.arthasTunnel.HandleBrowserWebSocket(w, r)
}
