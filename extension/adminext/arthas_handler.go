// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"
)

// getTunnelAgentIDs returns a set of agent IDs that currently have an Arthas tunnel connection.
func (h *adminHandlers) getTunnelAgentIDs() map[string]struct{} {
	if h.arthasTunnel == nil {
		return nil
	}
	agents := h.arthasTunnel.ListConnectedAgents()
	set := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		set[a.AgentID] = struct{}{}
	}
	return set
}

// WSTokenRequest represents a request to generate a WebSocket token.
type WSTokenRequest struct {
	Purpose string `json:"purpose"` // h.g., "arthas_terminal"
}

// WSTokenResponse represents the response containing a WebSocket token.
type WSTokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"` // seconds until expiration
}

// generateWSToken generates a short-lived token for WebSocket authentication.
// This allows secure WebSocket connections without exposing API keys in URLs.
func (h *adminHandlers) generateWSToken(w http.ResponseWriter, r *http.Request) {
	var req WSTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default purpose if not specified
		req.Purpose = "arthas_terminal"
	}

	if req.Purpose == "" {
		req.Purpose = "arthas_terminal"
	}

	// Generate token (userID can be extracted from auth context if needed)
	token, err := h.wsTokenMgr.GenerateToken(r.Context(), "", req.Purpose)
	if err != nil {
		h.logger.Error("Failed to generate WS token", zap.Error(err))
		http.Error(w, `{"error":"Failed to generate token"}`, http.StatusInternalServerError)
		return
	}

	response := WSTokenResponse{
		Token:     token.Token,
		ExpiresIn: 30, // 30 seconds
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("Failed to encode response", zap.Error(err))
	}
}

// handleArthasWebSocket handles WebSocket connections from browsers for Arthas terminal.
// Authentication is done via short-lived WS token (obtained from POST /api/v2/auth/ws-token).
func (h *adminHandlers) handleArthasWebSocket(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	token := r.URL.Query().Get("token")

	h.logger.Info("Arthas WebSocket connection request received",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("agent_id", agentID),
		zap.Bool("has_token", token != ""),
	)

	if h.arthasTunnel == nil {
		h.logger.Error("Arthas tunnel not configured")
		http.Error(w, "Arthas tunnel not configured", http.StatusServiceUnavailable)
		return
	}

	// Validate WS token (single-use, consumed on validation)
	if token == "" {
		h.logger.Warn("WebSocket connection rejected: no token provided",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "Unauthorized: token required", http.StatusUnauthorized)
		return
	}

	tokenInfo := h.wsTokenMgr.ValidateAndConsume(r.Context(), token, "arthas_terminal")
	if tokenInfo == nil {
		h.logger.Warn("WebSocket connection rejected: invalid or expired token",
			zap.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
		return
	}

	h.logger.Debug("WebSocket token validated",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("agent_id", agentID),
	)

	h.arthasTunnel.HandleBrowserWebSocket(w, r)
}
