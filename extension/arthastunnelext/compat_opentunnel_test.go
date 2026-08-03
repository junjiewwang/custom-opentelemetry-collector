// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/pending"
)

// dialWSURL dials a ws server with the given query string and returns the conn.
func dialWSURL(t *testing.T, wsURL, query string) *websocket.Conn {
	t.Helper()
	target := wsURL
	if query != "" {
		target += "?" + query
	}
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	require.NoError(t, err)
	return conn
}

// readTextOrFail reads one text message within a deadline, failing the test on error/timeout.
func readTextOrFail(t *testing.T, conn *websocket.Conn, timeout time.Duration) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(data)
}

// ── handleOpenTunnel ───────────────────────────────────────────────────

func TestHandleOpenTunnel_MissingClientConnID_Closes(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r) // openTunnel allowed on agentgateway
	})
	defer srv.Close()

	conn := dialWSURL(t, wsURL, "method=openTunnel") // no clientConnectionId
	defer conn.Close()

	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "server must close on missing clientConnectionId")
}

func TestHandleOpenTunnel_NoPending_Closes(t *testing.T) {
	s := newLocalCompat(t)
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()

	conn := dialWSURL(t, wsURL, "method=openTunnel&clientConnectionId=none")
	defer conn.Close()

	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "no pending → server closes")
}

func TestHandleOpenTunnel_DeliversToLocalPending(t *testing.T) {
	s := newLocalCompat(t)

	// Pre-register a pending (as handleConnectArthas would) with a browser conn.
	// The browser conn is a stand-in WS connection that stays open but idle.
	browserConn := dialWSURL(t, mustStartBlockingServer(t), "")
	defer browserConn.Close()

	pendingInfo := &pending.PendingInfo{
		ClientConnID: "c1",
		SessionID:    "s1",
		AgentID:      "a1",
	}
	require.NoError(t, s.pendingStore.CreateWithBrowserConn(context.Background(), pendingInfo, browserConn))

	// A consumer must drain the tunnel so DeliverTunnel's buffered channel doesn't
	// leak; spawn WaitForTunnel to receive the tunnel conn delivered by openTunnel.
	gotTunnel := make(chan struct{}, 1)
	go func() {
		_, _ = s.pendingStore.WaitForTunnel(context.Background(), "c1", 3*time.Second)
		gotTunnel <- struct{}{}
	}()

	// Now an agent dials openTunnel with the matching clientConnectionId.
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()
	conn := dialWSURL(t, wsURL, "method=openTunnel&clientConnectionId=c1")

	// On successful delivery, handleOpenTunnel returns WITHOUT closing the conn
	// (ownership transferred). The WaitForTunnel consumer receives it and the
	// pending is consumed. Verify the consumer woke up.
	select {
	case <-gotTunnel:
		// success: tunnel was delivered and consumed
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForTunnel did not receive the delivered tunnel")
	}
	// The openTunnel conn is now owned by the consumer; close it.
	_ = conn.Close()
}

func TestHandleOpenTunnel_DuplicateDelivery_Closes(t *testing.T) {
	s := newLocalCompat(t)

	browserConn := dialWSURL(t, mustStartBlockingServer(t), "")
	defer browserConn.Close()

	pendingInfo := &pending.PendingInfo{ClientConnID: "dup", SessionID: "s", AgentID: "a"}
	require.NoError(t, s.pendingStore.CreateWithBrowserConn(context.Background(), pendingInfo, browserConn))

	// First delivery fills the buffered tunnel channel (capacity 1) — no consumer.
	// We deliver directly via the store to occupy the slot.
	require.NoError(t, s.pendingStore.DeliverTunnel("dup", browserConn))

	// Now a second openTunnel arrives → DeliverTunnel returns ErrTunnelAlreadyDelivered
	// → handler writes close + closes.
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	})
	defer srv.Close()
	conn := dialWSURL(t, wsURL, "method=openTunnel&clientConnectionId=dup")
	defer conn.Close()

	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "duplicate delivery → server closes")
}

// ── handleConnectArthas ────────────────────────────────────────────────

func TestHandleConnectArthas_MissingAgentID_Closes(t *testing.T) {
	s := newLocalCompat(t)
	// connectArthas is allowed on admin ingress.
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAdmin, w, r)
	})
	defer srv.Close()

	conn := dialWSURL(t, wsURL, "method=connectArthas") // no id
	defer conn.Close()

	// Handler sends an error status frame, then closes.
	msg := readTextOrFail(t, conn, 2*time.Second)
	assert.Contains(t, msg, "[-]", "missing agent id → error status (red [-] icon)")
	assert.Contains(t, msg, ansiRed)
	assert.Contains(t, msg, "Missing agent ID")

	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "server closes after the error status")
}

func TestHandleConnectArthas_AgentOffline_TimesOutAndCloses(t *testing.T) {
	// Use a very short connectTimeout so the wait loop exits quickly.
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 1500 * time.Millisecond
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAdmin, w, r)
	})
	defer srv.Close()

	conn := dialWSURL(t, wsURL, "method=connectArthas&id=ghost")
	defer conn.Close()

	// The handler sends a "connecting" status, waits ~1.5s, then an error status + close.
	// Drain frames until close.
	var last string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		last = string(data)
	}
	assert.Contains(t, last, "[-]", "agent offline after wait → error status (red [-] icon)")
	assert.Contains(t, last, ansiRed)
	assert.Contains(t, last, "Agent is offline")
}

// testBlockingUpgrader is a permissive WS upgrader used to spin up stand-in
// peer/browser connections that stay open but idle.
var testBlockingUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// mustStartBlockingServer starts a ws server that upgrades the connection then
// holds it open without doing anything (a stand-in browser/peer conn). Returns
// its ws URL. The handler must upgrade — otherwise the dialer blocks waiting
// for the HTTP 101 response that never arrives.
func mustStartBlockingServer(t *testing.T) string {
	t.Helper()
	wsURL, srv := wsServerFor(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testBlockingUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-r.Context().Done()
		_ = conn.Close()
	})
	t.Cleanup(srv.Close)
	return wsURL
}
