// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"
)

// ArthasAgentResponse represents an agent with Arthas tunnel connection.
// Fields match the simplified ConnectedAgent from arthastunnelext.
type ArthasAgentResponse struct {
	AgentID     string `json:"agent_id"`
	AppID       string `json:"app_id"`
	ServiceName string `json:"service_name,omitempty"`
	IP          string `json:"ip,omitempty"`
	Version     string `json:"version,omitempty"`
	ConnectedAt int64  `json:"connected_at"`
	LastPingAt  int64  `json:"last_ping_at"`
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
	token, err := e.wsTokenMgr.GenerateToken(r.Context(), "", req.Purpose)
	if err != nil {
		e.logger.Error("Failed to generate WS token", zap.Error(err))
		http.Error(w, `{"error":"Failed to generate token"}`, http.StatusInternalServerError)
		return
	}

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
// The response uses snake_case field names to match the backend ConnectedAgent struct.
func (e *Extension) listArthasAgents(w http.ResponseWriter, r *http.Request) {
	if e.arthasTunnel == nil {
		http.Error(w, `{"error":"Arthas tunnel not configured"}`, http.StatusServiceUnavailable)
		return
	}

	agents := e.arthasTunnel.ListConnectedAgents()

	response := make([]ArthasAgentResponse, 0, len(agents))
	for _, agent := range agents {
		response = append(response, ArthasAgentResponse{
			AgentID:     agent.AgentID,
			AppID:       agent.AppID,
			ServiceName: agent.ServiceName,
			IP:          agent.IP,
			Version:     agent.Version,
			ConnectedAt: agent.ConnectedAt.UnixMilli(),
			LastPingAt:  agent.LastPingAt.UnixMilli(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		e.logger.Error("Failed to encode response", zap.Error(err))
	}
}

// handleArthasWebSocket handles WebSocket connections from browsers for Arthas terminal.
// Authentication is done via short-lived WS token (obtained from POST /api/v2/auth/ws-token).
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

	tokenInfo := e.wsTokenMgr.ValidateAndConsume(r.Context(), token, "arthas_terminal")
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
