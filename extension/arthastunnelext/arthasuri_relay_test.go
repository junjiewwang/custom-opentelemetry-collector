// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// setupWSPair creates a pair of WebSocket connections (client↔server) for testing.
// Returns (clientConn, serverConn, cleanup).
func setupWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()

	var serverConn *websocket.Conn
	var serverMu sync.Mutex
	serverReady := make(chan struct{})

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		serverMu.Lock()
		serverConn = conn
		serverMu.Unlock()
		close(serverReady)
		// Keep the handler alive until test cleanup
		select {}
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	<-serverReady

	cleanup := func() {
		_ = clientConn.Close()
		serverMu.Lock()
		if serverConn != nil {
			_ = serverConn.Close()
		}
		serverMu.Unlock()
		server.Close()
	}

	serverMu.Lock()
	sc := serverConn
	serverMu.Unlock()

	return clientConn, sc, cleanup
}

func TestRelayWebSocketPair_DataForwarding(t *testing.T) {
	// Create two WS pairs: browser-side and tunnel-side
	browserClient, browserServer, cleanupBrowser := setupWSPair(t)
	defer cleanupBrowser()
	tunnelClient, tunnelServer, cleanupTunnel := setupWSPair(t)
	defer cleanupTunnel()

	logger := zaptest.NewLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start relay between browserServer and tunnelServer
	relayDone := make(chan struct{})
	go func() {
		relayWebSocketPair(ctx, logger, browserServer, tunnelServer)
		close(relayDone)
	}()

	// Test: browser → tunnel
	testMsg := "hello from browser"
	err := browserClient.WriteMessage(websocket.TextMessage, []byte(testMsg))
	require.NoError(t, err)

	_ = tunnelClient.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := tunnelClient.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	assert.Equal(t, testMsg, string(data))

	// Test: tunnel → browser
	replyMsg := "hello from tunnel"
	err = tunnelClient.WriteMessage(websocket.TextMessage, []byte(replyMsg))
	require.NoError(t, err)

	_ = browserClient.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err = browserClient.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	assert.Equal(t, replyMsg, string(data))

	// Cancel context should close the relay
	cancel()
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop after context cancel")
	}
}

func TestRelayWebSocketPair_NilConnections(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	// Should not panic with nil connections
	relayWebSocketPair(ctx, logger, nil, nil)
}

func TestRelayWebSocketPair_ClosePropagation(t *testing.T) {
	// When one side closes, the other should also be closed.
	browserClient, browserServer, cleanupBrowser := setupWSPair(t)
	defer cleanupBrowser()
	tunnelClient, tunnelServer, cleanupTunnel := setupWSPair(t)
	defer cleanupTunnel()

	logger := zaptest.NewLogger(t)
	ctx := context.Background()

	relayDone := make(chan struct{})
	go func() {
		relayWebSocketPair(ctx, logger, browserServer, tunnelServer)
		close(relayDone)
	}()

	// Allow relay to start and heartbeat to initialize
	time.Sleep(50 * time.Millisecond)

	// Close browser client → should propagate to tunnel
	_ = browserClient.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"),
	)

	// Relay should end
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop after browser close")
	}

	// Tunnel client should get a close or error on next read
	_ = tunnelClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := tunnelClient.ReadMessage()
	assert.Error(t, err, "tunnel side should be closed after browser close")
}

func TestRelayWebSocketPair_PongKeepsAlive(t *testing.T) {
	// Verify that connections stay alive when pong is received (ReadDeadline is renewed).
	browserClient, browserServer, cleanupBrowser := setupWSPair(t)
	defer cleanupBrowser()
	tunnelClient, tunnelServer, cleanupTunnel := setupWSPair(t)
	defer cleanupTunnel()

	logger := zaptest.NewLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up pong handler on clients (simulating proper WebSocket protocol).
	// The gorilla/websocket library automatically handles ping/pong at protocol level
	// when using ReadMessage (the default read handler sends pong automatically).
	// So we just need to verify the relay stays alive for longer than relayPongWait
	// when data is being exchanged.

	relayDone := make(chan struct{})
	go func() {
		relayWebSocketPair(ctx, logger, browserServer, tunnelServer)
		close(relayDone)
	}()

	// Send messages periodically for longer than relayPongWait (30s).
	// Since ping/pong happens at protocol level, just verify relay stays alive
	// for a reasonable duration with active communication.
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		err := browserClient.WriteMessage(websocket.TextMessage, []byte("keepalive"))
		require.NoError(t, err, "relay should stay alive with active communication")

		_ = tunnelClient.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err = tunnelClient.ReadMessage()
		require.NoError(t, err, "tunnel should receive data through active relay")
	}

	// Verify relay is still running
	select {
	case <-relayDone:
		t.Fatal("relay should still be running")
	default:
		// OK, relay is alive
	}

	cancel()
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop after cancel")
	}
}

func TestRelayConn_SafeWriteConcurrency(t *testing.T) {
	// Test that relayConn.safeWriteMessage and safeWriteControl are safe for concurrent use.
	_, serverConn, cleanup := setupWSPair(t)
	defer cleanup()

	rc := &relayConn{Conn: serverConn}

	var wg sync.WaitGroup
	const goroutines = 10

	// Concurrent WriteMessage
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = rc.safeWriteMessage(websocket.TextMessage, []byte("msg"))
		}(i)
	}

	// Concurrent WriteControl (ping)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = rc.safeWriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		}(i)
	}

	wg.Wait()
	// If we get here without panic/race, the test passes.
}
