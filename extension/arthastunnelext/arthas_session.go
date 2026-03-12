// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// ArthasSession represents a programmatic Arthas session established through the Tunnel.
//
// It wraps a WebSocket relay connection (the "tunnel" side) and provides a high-level
// API for sending Arthas commands and receiving structured JSON results.
//
// The session must be closed by the caller when no longer needed.
type ArthasSession struct {
	conn      *websocket.Conn
	agentID   string
	sessionID string
	logger    *zap.Logger

	mu     sync.Mutex
	closed bool
}

// newArthasSession creates a new ArthasSession wrapping a tunnel WebSocket connection.
func newArthasSession(conn *websocket.Conn, agentID, sessionID string, logger *zap.Logger) *ArthasSession {
	return &ArthasSession{
		conn:      conn,
		agentID:   agentID,
		sessionID: sessionID,
		logger:    logger.Named("arthas-session"),
	}
}

// AgentID returns the agent ID this session is connected to.
func (s *ArthasSession) AgentID() string {
	return s.agentID
}

// SessionID returns the session ID.
func (s *ArthasSession) SessionID() string {
	return s.sessionID
}

// ArthasExecResult represents the JSON response from Arthas HTTP API exec endpoint.
// Arthas returns this structure when commands are executed via the HTTP API or Tunnel relay.
type ArthasExecResult struct {
	State string `json:"state"` // "SUCCEEDED", "FAILED", etc.
	Body  struct {
		Results    []json.RawMessage `json:"results"`
		TimeExpired bool             `json:"timeExpired"`
		Command    string           `json:"command"`
		JobStatus  string           `json:"jobStatus"`
	} `json:"body"`
	// SessionId is the Arthas session ID (not our tunnel session).
	SessionId string `json:"sessionId,omitempty"`
}

// ArthasResultItem represents a single result item from Arthas results array.
type ArthasResultItem struct {
	Type string `json:"type"` // "status", "enhancer", "watch", "trace", "thread", "version", etc.

	// Status result fields
	StatusCode int    `json:"statusCode,omitempty"`
	Message    string `json:"message,omitempty"`

	// Enhancer result fields
	Success bool                   `json:"success,omitempty"`
	Effect  map[string]interface{} `json:"effect,omitempty"`

	// Watch/trace result fields
	Cost  float64     `json:"cost,omitempty"`
	Ts    int64       `json:"ts,omitempty"`
	Value interface{} `json:"value,omitempty"`

	// JobId
	JobId int `json:"jobId,omitempty"`

	// Raw data for unknown types
	Raw json.RawMessage `json:"-"`
}

// ExecCommand sends an Arthas command and waits for the result.
// This is the primary method for executing Arthas commands programmatically.
//
// The command string follows Arthas CLI syntax, e.g.:
//   - "version"
//   - "thread"
//   - "trace com.example.Service methodName '#cost>100'"
//   - "watch com.example.Service methodName '{params, returnObj}'"
//   - "jad com.example.Service"
//   - "sc com.example.*"
//
// Arthas commands through the Tunnel use a JSON-based HTTP API protocol.
// The command is sent as a JSON object: {"action":"exec","command":"..."}
// The response is a JSON object with the execution result.
func (s *ArthasSession) ExecCommand(ctx context.Context, command string, timeout time.Duration) (*ArthasExecResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("session is closed")
	}
	s.mu.Unlock()

	s.logger.Info("Executing Arthas command",
		zap.String("agent_id", s.agentID),
		zap.String("command", command),
		zap.Duration("timeout", timeout),
	)

	// Build the Arthas exec request JSON
	execReq := map[string]interface{}{
		"action":  "exec",
		"command": command,
	}

	reqBytes, err := json.Marshal(execReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal exec request: %w", err)
	}

	// Send command
	s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := s.conn.WriteMessage(websocket.TextMessage, reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Read result with timeout
	s.conn.SetReadDeadline(time.Now().Add(timeout))

	// Create a channel-based reader for context cancellation
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)

	go func() {
		_, data, err := s.conn.ReadMessage()
		resultCh <- readResult{data: data, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return nil, fmt.Errorf("failed to read result: %w", result.err)
		}

		// Parse the Arthas result
		var execResult ArthasExecResult
		if err := json.Unmarshal(result.data, &execResult); err != nil {
			// If it's not valid JSON, return the raw text as a result
			s.logger.Warn("Arthas returned non-JSON response",
				zap.String("agent_id", s.agentID),
				zap.String("response", string(result.data)),
			)
			return &ArthasExecResult{
				State: "RAW",
				Body: struct {
					Results    []json.RawMessage `json:"results"`
					TimeExpired bool             `json:"timeExpired"`
					Command    string           `json:"command"`
					JobStatus  string           `json:"jobStatus"`
				}{
					Command: command,
				},
			}, nil
		}

		s.logger.Info("Arthas command completed",
			zap.String("agent_id", s.agentID),
			zap.String("command", command),
			zap.String("state", execResult.State),
			zap.Int("results_count", len(execResult.Body.Results)),
		)

		return &execResult, nil
	}
}

// Close closes the Arthas session and releases the underlying tunnel connection.
func (s *ArthasSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	s.logger.Info("Closing Arthas session",
		zap.String("agent_id", s.agentID),
		zap.String("session_id", s.sessionID),
	)

	// Send close frame
	_ = writeClose(s.conn, websocket.CloseNormalClosure, "session closed")
	return s.conn.Close()
}
