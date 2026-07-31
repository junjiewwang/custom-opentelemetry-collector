// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// startEchoWS starts an httptest server that upgrades to WebSocket and, for
// each received message, replies with the provided response payload. Returns
// the server URL (ws://) and a cleanup func.
func startEchoWS(t *testing.T, responsePayload string) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(responsePayload)); err != nil {
				return
			}
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return wsURL, srv.Close
}

// dialWS connects to a ws server and returns the conn (client side).
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	return conn
}

func TestNewArthasSession_Accessors(t *testing.T) {
	s := newArthasSession(nil, "agent-1", "sess-1", zap.NewNop())
	assert.Equal(t, "agent-1", s.AgentID())
	assert.Equal(t, "sess-1", s.SessionID())
}

func TestArthasSession_ExecCommand_JSONResponse(t *testing.T) {
	// Server replies with a valid Arthas exec result JSON.
	resp := `{"state":"SUCCEEDED","body":{"results":[],"timeExpired":false,"command":"version","jobStatus":"COMPLETED"},"sessionId":"arthas-1"}`
	wsURL, cleanup := startEchoWS(t, resp)
	defer cleanup()

	conn := dialWS(t, wsURL)
	defer conn.Close()
	s := newArthasSession(conn, "agent-1", "sess-1", zap.NewNop())

	res, err := s.ExecCommand(context.Background(), "version", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "SUCCEEDED", res.State)
	assert.Equal(t, "version", res.Body.Command)
	assert.Equal(t, "arthas-1", res.SessionId)
}

func TestArthasSession_ExecCommand_NonJSONResponse(t *testing.T) {
	// Server replies with plain text → ExecCommand returns a RAW result.
	wsURL, cleanup := startEchoWS(t, "not-json-at-all")
	defer cleanup()

	conn := dialWS(t, wsURL)
	defer conn.Close()
	s := newArthasSession(conn, "agent-1", "sess-1", zap.NewNop())

	res, err := s.ExecCommand(context.Background(), "version", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "RAW", res.State)
	assert.Equal(t, "version", res.Body.Command)
}

func TestArthasSession_ExecCommand_CtxCancel(t *testing.T) {
	// Server that never replies → ExecCommand blocks until ctx cancelled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()
		// Read the command but never write back.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	conn := dialWS(t, wsURL)
	defer conn.Close()
	s := newArthasSession(conn, "agent-1", "sess-1", zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := s.ExecCommand(ctx, "version", 5*time.Second)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestArthasSession_ExecCommand_OnClosedSession(t *testing.T) {
	wsURL, cleanup := startEchoWS(t, `{}`)
	defer cleanup()
	conn := dialWS(t, wsURL)
	defer conn.Close()
	s := newArthasSession(conn, "agent-1", "sess-1", zap.NewNop())

	require.NoError(t, s.Close())
	_, err := s.ExecCommand(context.Background(), "version", time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestArthasSession_Close_Idempotent(t *testing.T) {
	wsURL, cleanup := startEchoWS(t, `{}`)
	defer cleanup()
	conn := dialWS(t, wsURL)
	s := newArthasSession(conn, "agent-1", "sess-1", zap.NewNop())

	require.NoError(t, s.Close())
	// Second Close is a no-op (no error, no panic).
	require.NoError(t, s.Close())
}

func TestArthasExecResult_Unmarshal(t *testing.T) {
	// Sanity-check the result struct parses a representative Arthas response.
	data := `{"state":"SUCCEEDED","body":{"results":[{"type":"version"}],"timeExpired":false,"command":"version","jobStatus":"COMPLETED"},"sessionId":"s"}`
	var r ArthasExecResult
	require.NoError(t, json.Unmarshal([]byte(data), &r))
	assert.Equal(t, "SUCCEEDED", r.State)
	assert.Len(t, r.Body.Results, 1)
}
