// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newLocalCompat builds an arthasURICompat in local mode (no distributed,
// no taskSubmitter) for handler tests. The returned cleanup cancels the
// internal ctx and waits for goroutines.
func newLocalCompat(t *testing.T) *arthasURICompat {
	t.Helper()
	s := newArthasURICompat(context.Background(), zap.NewNop(), createDefaultConfig(), nil, nil)
	t.Cleanup(func() {
		s.shutdown(context.Background())
	})
	return s
}

// wsServerFor wraps a handler as an httptest WS server. Returns the ws:// URL.
func wsServerFor(handler http.HandlerFunc) (string, *httptest.Server) {
	srv := httptest.NewServer(handler)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), srv
}

func TestResolveAgentRegisterParams(t *testing.T) {
	s := newLocalCompat(t)

	// Explicit id wins.
	id, app, ver := s.resolveAgentRegisterParams(url.Values{"id": {"a1"}, "appName": {"svc"}, "arthasVersion": {"4.0"}})
	assert.Equal(t, "a1", id)
	assert.Equal(t, "svc", app)
	assert.Equal(t, "4.0", ver)

	// No id + appName → random id prefixed with appName.
	id, app, ver = s.resolveAgentRegisterParams(url.Values{"appName": {"svc"}})
	assert.True(t, strings.HasPrefix(id, "svc_"), "random id prefixed with appName")
	assert.Equal(t, "svc", app)
	assert.Len(t, id, len("svc_")+20)

	// No id + no appName → bare random id.
	id, app, ver = s.resolveAgentRegisterParams(url.Values{})
	assert.Len(t, id, 20, "bare random id is 20 chars")
	assert.Empty(t, app)
}

// ── handleWS early-close branches ─────────────────────────────────────

func TestHandleWS_MissingMethod_Closes(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil) // no method query param
	require.NoError(t, err)
	defer conn.Close()

	// Server writes a close frame then closes; the client sees a close.
	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "server must close after missing method")
}

func TestHandleWS_MethodNotAllowed_Closes(t *testing.T) {
	s := newLocalCompat(t)
	// agentgateway ingress forbids connectArthas; StrictIngressMethodAllowlist defaults true.
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?method=connectArthas", nil)
	require.NoError(t, err)
	defer conn.Close()

	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "server must close on disallowed method")
}

func TestHandleWS_AllowlistDisabled_Dispatches(t *testing.T) {
	// With StrictIngressMethodAllowlist=false, a disallowed method still dispatches.
	// Use an unsupported method so dispatch closes immediately (no handler loop).
	cfg := createDefaultConfig()
	cfg.StrictIngressMethodAllowlist = false
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?method=bogus", nil)
	require.NoError(t, err)
	defer conn.Close()

	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "dispatch closes on unsupported method")
}

// ── dispatch default branch ────────────────────────────────────────────

func TestDispatch_UnsupportedMethod_Closes(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		ctx := &compatConnContext{
			ingress:    ingressAdmin,
			conn:       conn,
			request:    r,
			query:      url.Values{},
			remoteAddr: r.RemoteAddr,
			logger:     zap.NewNop(),
		}
		s.dispatch(ctx, arthasURIMethod("bogus"))
	})
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "dispatch closes on unsupported method")
}

// Ensure the helper compiles + the allowlist const is exercised.
var _ = time.Second

// ── handleAgentRegister end-to-end (local mode) ───────────────────────

func TestHandleAgentRegister_EndToEnd(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	// Dial as an agent with method=agentRegister and an explicit id.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?method=agentRegister&id=agent-1&appName=svc&arthasVersion=4.0", nil)
	require.NoError(t, err)

	// Read the response frame the handler writes on registration.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	frame := string(data)
	assert.True(t, strings.HasPrefix(frame, "response:/?"), "response frame prefix")
	assert.Contains(t, frame, "method=agentRegister")
	assert.Contains(t, frame, "id=agent-1")

	// The agent is registered in the local map while the control loop runs.
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents["agent-1"]
		return ok
	}, time.Second, 10*time.Millisecond, "agent must be registered")

	// Closing the client conn makes runAgentControlLoops' ReadMessage fail →
	// handler exits → agent removed from the map.
	require.NoError(t, conn.Close())

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents["agent-1"]
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "agent must be removed after disconnect")
}

func TestHandleAgentRegister_ReplacesExistingConnection(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	// First registration.
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL+"?method=agentRegister&id=dup", nil)
	require.NoError(t, err)
	_ = conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn1.ReadMessage() // consume response frame

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents["dup"]
		return ok
	}, time.Second, 10*time.Millisecond)

	// Second registration with the same id replaces the first (old conn closed).
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL+"?method=agentRegister&id=dup", nil)
	require.NoError(t, err)
	_ = conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn2.ReadMessage()

	// conn1 should now be closed by the server (replaced).
	require.Eventually(t, func() bool {
		_, _, err := conn1.ReadMessage()
		return err != nil
	}, time.Second, 10*time.Millisecond, "old connection must be closed on replacement")

	// Still exactly one agent registered.
	s.mu.Lock()
	count := len(s.agents)
	s.mu.Unlock()
	assert.Equal(t, 1, count)

	_ = conn2.Close()
	// Allow the handler goroutines to finish before shutdown cleanup runs.
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents["dup"]
		return !ok
	}, 2*time.Second, 10*time.Millisecond)
}
