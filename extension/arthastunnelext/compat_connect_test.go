// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/pending"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/registry"
)

// ── shared test helpers (WS handoff + agent injection) ────────────────

// handoffWSServer starts a WS server that upgrades and hands the server-side
// conn to the caller via the returned channel, then blocks until shutdown.
func handoffWSServer(t *testing.T) (string, <-chan *websocket.Conn) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testBlockingUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ch <- conn
		<-r.Context().Done()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), ch
}

// wsURL converts an httptest server http URL to ws.
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// injectAgent registers a compatAgent backed by a fresh WS pair and returns the
// client-side conn (reads what the agent "receives", e.g. startTunnel frames).
func injectAgent(t *testing.T, s *arthasURICompat, agentID string) *websocket.Conn {
	t.Helper()
	u, ch := handoffWSServer(t)
	cc, _, err := websocket.DefaultDialer.Dial(u, nil)
	require.NoError(t, err)
	sc := <-ch
	now := time.Now().UnixMilli()
	a := &compatAgent{
		agentID:        agentID,
		conn:           sc,
		connectedAt:    now,
		lastPongAt:     now,
		lastActivityAt: now,
	}
	s.mu.Lock()
	s.agents[agentID] = a
	s.mu.Unlock()
	return cc
}

// newTunnelPair returns a fresh WS pair to use as a tunnel connection: the
// server-side conn (handed to DeliverTunnel) and the consumer-side conn.
func newTunnelPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	u, ch := handoffWSServer(t)
	consumer, _, err := websocket.DefaultDialer.Dial(u, nil)
	require.NoError(t, err)
	serverSide := <-ch
	return serverSide, consumer
}

// readFrame reads one text message within a deadline.
func readFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(data)
}

// extractClientConnID parses the clientConnectionId from a startTunnel frame.
func extractClientConnID(t *testing.T, frame string) string {
	t.Helper()
	v, err := url.ParseQuery(strings.TrimPrefix(frame, "response:/?"))
	require.NoError(t, err)
	id := v.Get("clientConnectionId")
	require.NotEmpty(t, id)
	return id
}

// fakeTaskSubmitter captures submitted tasks.
type fakeTaskSubmitter struct {
	mu    sync.Mutex
	tasks []*model.Task
	err   error
}

func (f *fakeTaskSubmitter) SubmitTaskForAgent(_ context.Context, _ string, task *model.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	return f.err
}

func (f *fakeTaskSubmitter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks)
}

// newDistributedCompat builds an arthasURICompat in distributed mode backed by
// miniredis, returning the compat and the distributed manager.
func newDistributedCompat(t *testing.T) (*arthasURICompat, *DistributedManager) {
	t.Helper()
	cfg := newTestDistributedConfig()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	dm := NewDistributedManager(context.Background(), zap.NewNop(), cfg, client, 9000)
	t.Cleanup(func() { dm.Shutdown(context.Background()) })
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, dm, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })
	return s, dm
}

// ── connectToAgentProgrammatic ─────────────────────────────────────────

func TestConnectToAgentProgrammatic_EmptyID(t *testing.T) {
	s := newLocalCompat(t)
	_, err := s.connectToAgentProgrammatic(context.Background(), "", zap.NewNop())
	assert.Error(t, err)
}

func TestConnectToAgentProgrammatic_NotConnected(t *testing.T) {
	s := newLocalCompat(t)
	_, err := s.connectToAgentProgrammatic(context.Background(), "ghost", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

func TestConnectToAgentProgrammatic_Unhealthy(t *testing.T) {
	s := newLocalCompat(t)
	cc := injectAgent(t, s, "a1")
	defer cc.Close()

	// Force liveness timeout.
	s.mu.Lock()
	s.agents["a1"].lastPongAt = time.Now().Add(-10 * time.Hour).UnixMilli()
	s.mu.Unlock()

	_, err := s.connectToAgentProgrammatic(context.Background(), "a1", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unhealthy")
}

func TestConnectToAgentProgrammatic_StartTunnelFails(t *testing.T) {
	s := newLocalCompat(t)
	cc := injectAgent(t, s, "a1")
	// Close the agent's server-side conn so safeWriteMessage fails.
	s.mu.Lock()
	agentConn := s.agents["a1"].conn
	s.mu.Unlock()
	require.NoError(t, agentConn.Close())
	defer cc.Close()

	_, err := s.connectToAgentProgrammatic(context.Background(), "a1", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "startTunnel")
}

func TestConnectToAgentProgrammatic_LocalSuccess(t *testing.T) {
	s := newLocalCompat(t)
	agentCC := injectAgent(t, s, "a1")
	defer agentCC.Close()

	sessCh := make(chan *ArthasSession, 1)
	errCh := make(chan error, 1)
	go func() {
		sess, err := s.connectToAgentProgrammatic(context.Background(), "a1", zap.NewNop())
		if err != nil {
			errCh <- err
			return
		}
		sessCh <- sess
	}()

	// Read the startTunnel frame the agent receives, extract clientConnID.
	// (url.Values.Encode sorts keys alphabetically, so check by substring.)
	frame := readFrame(t, agentCC, 2*time.Second)
	require.True(t, strings.HasPrefix(frame, "response:/?"))
	require.Contains(t, frame, "method=startTunnel")
	clientConnID := extractClientConnID(t, frame)

	// Deliver a tunnel conn for that pending.
	tunnelServerSide, tunnelConsumer := newTunnelPair(t)
	defer tunnelConsumer.Close()
	require.NoError(t, s.pendingStore.DeliverTunnel(clientConnID, tunnelServerSide))

	select {
	case sess := <-sessCh:
		require.NotNil(t, sess)
		assert.Equal(t, "a1", sess.AgentID())
		assert.NotEmpty(t, sess.SessionID())
		// The session wraps the delivered tunnel conn (server side); the consumer
		// side observes a close when the session is closed.
		require.NoError(t, sess.Close())
		_ = tunnelConsumer.SetReadDeadline(time.Now().Add(time.Second))
		_, _, err := tunnelConsumer.ReadMessage()
		assert.Error(t, err, "closing the session must close the tunnel conn")
	case err := <-errCh:
		t.Fatalf("connectToAgentProgrammatic failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("did not return a session in time")
	}
}

func TestConnectToAgentProgrammatic_WaitForTunnelTimeout(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 200 * time.Millisecond
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	agentCC := injectAgent(t, s, "a1")
	defer agentCC.Close()

	start := time.Now()
	_, err := s.connectToAgentProgrammatic(context.Background(), "a1", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	// Should return close to the configured timeout, not block for the default 20s.
	assert.Less(t, time.Since(start), 3*time.Second)
}

// ── connectToAgentCrossNode ────────────────────────────────────────────

// crossNodeTargetServer emulates a remote node's HandleInternalConnectArthas:
// it writes the given status frames (terminal-formatted), then either holds the
// conn open (READY case, for session use) or closes it.
func crossNodeTargetServer(t *testing.T, frames []string, holdOpen bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testBlockingUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for _, f := range frames {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(f)); err != nil {
				_ = conn.Close()
				return
			}
		}
		if holdOpen {
			<-r.Context().Done()
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return wsURL(srv)
}

func TestConnectToAgentCrossNode_NoDistributed(t *testing.T) {
	s := newLocalCompat(t)
	_, err := s.connectToAgentCrossNode(context.Background(), "a1", "127.0.0.1:1", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "distributed proxy not available")
}

func TestConnectToAgentCrossNode_DialError(t *testing.T) {
	s, _ := newDistributedCompat(t)
	_, err := s.connectToAgentCrossNode(context.Background(), "a1", "ws://127.0.0.1:1", zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to dial target node")
}

func TestConnectToAgentCrossNode_Ready(t *testing.T) {
	s, _ := newDistributedCompat(t)
	target := crossNodeTargetServer(t, []string{
		formatTerminalStatus(statusConnecting, "connecting..."),
		formatTerminalStatus(statusReady, "ready"),
	}, true)

	sess, err := s.connectToAgentCrossNode(context.Background(), "a1", target, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "a1", sess.AgentID())
	assert.NotEmpty(t, sess.SessionID())
	require.NoError(t, sess.Close())
}

func TestConnectToAgentCrossNode_ErrorStatus(t *testing.T) {
	s, _ := newDistributedCompat(t)
	target := crossNodeTargetServer(t, []string{
		formatTerminalStatus(statusError, "boom"),
	}, false)

	_, err := s.connectToAgentCrossNode(context.Background(), "a1", target, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cross-node connection failed")
	assert.Contains(t, err.Error(), "boom")
}

func TestConnectToAgentCrossNode_TimeoutStatus(t *testing.T) {
	s, _ := newDistributedCompat(t)
	target := crossNodeTargetServer(t, []string{
		formatTerminalStatus(statusTimeout, "timed out"),
	}, false)

	_, err := s.connectToAgentCrossNode(context.Background(), "a1", target, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cross-node connection timeout")
}

// connectToAgentProgrammatic delegates to cross-node when the agent is remote.
func TestConnectToAgentProgrammatic_CrossNodeDelegates(t *testing.T) {
	s, dm := newDistributedCompat(t)
	target := crossNodeTargetServer(t, []string{
		formatTerminalStatus(statusReady, "ready"),
	}, true)

	// Register the agent in the redis registry on a *remote* node whose address
	// points at the target server, so GetAgentNodeAddr returns it.
	rr := dm.Registry().GetRedisRegistry()
	require.NotNil(t, rr)
	require.NoError(t, rr.Register(context.Background(), &registry.AgentInfo{
		AgentID:    "remote-a1",
		NodeID:     "other-node",
		NodeAddr:   target,
		LastPongAt: time.Now().UnixMilli(),
	}))

	sess, err := s.connectToAgentProgrammatic(context.Background(), "remote-a1", zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "remote-a1", sess.AgentID())
	require.NoError(t, sess.Close())
}

// echoWSServerCompat starts a WS server that echoes received messages back.
func echoWSServerCompat(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testBlockingUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return wsURL(srv)
}

// drainUntil reads frames from conn until one contains the marker (or timeout).
func drainUntil(t *testing.T, conn *websocket.Conn, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("error while draining for %q: %v", marker, err)
		}
		if strings.Contains(string(data), marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out draining for %q", marker)
		}
	}
}

// ── handleConnectArthas full flows ─────────────────────────────────────

func TestHandleConnectArthas_LocalAgentSuccessRelays(t *testing.T) {
	s := newLocalCompat(t)

	// Register an agent through the real handleWS path (starts control loops).
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	}))
	t.Cleanup(agentSrv.Close)
	agentConn, _, err := websocket.DefaultDialer.Dial(wsURL(agentSrv)+"?method=agentRegister&id=a1", nil)
	require.NoError(t, err)
	defer agentConn.Close()
	readFrame(t, agentConn, 2*time.Second) // consume agentRegister response
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents["a1"]
		return ok
	}, time.Second, 10*time.Millisecond)

	// Browser connects via admin ingress.
	adminSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAdmin, w, r)
	}))
	t.Cleanup(adminSrv.Close)
	browserConn, _, err := websocket.DefaultDialer.Dial(wsURL(adminSrv)+"?method=connectArthas&id=a1", nil)
	require.NoError(t, err)
	defer browserConn.Close()

	// The handler sends startTunnel to the agent; read it and extract the
	// clientConnectionId it generated.
	stFrame := readFrame(t, agentConn, 2*time.Second)
	require.Contains(t, stFrame, "method=startTunnel")
	clientConnID := extractClientConnID(t, stFrame)

	// Deliver a tunnel conn to satisfy the pending.
	tunnelServerSide, tunnelConsumer := newTunnelPair(t)
	defer tunnelConsumer.Close()
	require.NoError(t, s.pendingStore.DeliverTunnel(clientConnID, tunnelServerSide))

	// Drain browser status frames (connecting, waiting) until ready ([+]).
	drainUntil(t, browserConn, "[+]", 2*time.Second)

	// Relay is now live: browser -> tunnel.
	require.NoError(t, browserConn.WriteMessage(websocket.TextMessage, []byte("cmd")))
	assert.Equal(t, "cmd", readFrame(t, tunnelConsumer, 2*time.Second))
}

func TestHandleConnectArthas_CrossNodeProxies(t *testing.T) {
	s, dm := newDistributedCompat(t)
	target := echoWSServerCompat(t)

	// Register the agent on a remote node whose address is the echo target.
	rr := dm.Registry().GetRedisRegistry()
	require.NotNil(t, rr)
	require.NoError(t, rr.Register(context.Background(), &registry.AgentInfo{
		AgentID:    "r-a1",
		NodeID:     "other-node",
		NodeAddr:   target,
		LastPongAt: time.Now().UnixMilli(),
	}))

	adminSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAdmin, w, r)
	}))
	t.Cleanup(adminSrv.Close)
	browserConn, _, err := websocket.DefaultDialer.Dial(wsURL(adminSrv)+"?method=connectArthas&id=r-a1", nil)
	require.NoError(t, err)
	defer browserConn.Close()

	// proxyConnectArthas sends a "connecting" status, then relays to the target.
	drainUntil(t, browserConn, "[*]", 2*time.Second)

	// Round-trip a message through browser -> proxy -> echo target -> back.
	require.NoError(t, browserConn.WriteMessage(websocket.TextMessage, []byte("hello")))
	assert.Equal(t, "hello", readFrame(t, browserConn, 2*time.Second))
}

// registerAgentViaWS registers an agent through the real handleWS path and
// returns the agent's client-side conn (reads what the agent "receives").
func registerAgentViaWS(t *testing.T, s *arthasURICompat, agentID string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAgentGateway, w, r)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv)+"?method=agentRegister&id="+agentID, nil)
	require.NoError(t, err)
	readFrame(t, conn, 2*time.Second) // consume agentRegister response
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.agents[agentID]
		return ok
	}, time.Second, 10*time.Millisecond)
	return conn
}

// dialConnectArthas starts an admin ingress server and dials a browser
// connectArthas for the given agentID.
func dialConnectArthas(t *testing.T, s *arthasURICompat, agentID string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleWS(ingressAdmin, w, r)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv)+"?method=connectArthas&id="+agentID, nil)
	require.NoError(t, err)
	return conn
}

// ── handleConnectArthas error / wait branches ──────────────────────────

func TestHandleConnectArthas_StartTunnelSendFails(t *testing.T) {
	s := newLocalCompat(t)
	agentConn := injectAgent(t, s, "a1") // direct injection, no control loop
	defer agentConn.Close()

	// Break the agent's server-side conn so safeWriteMessage(startTunnel) fails.
	s.mu.Lock()
	require.NotNil(t, s.agents["a1"].conn)
	s.agents["a1"].conn.Close()
	s.mu.Unlock()

	browserConn := dialConnectArthas(t, s, "a1")
	defer browserConn.Close()

	// Handler sends connecting status, then startTunnel fails → it sends an
	// error status "Failed to notify agent" and cleans up. Drain until close.
	var last string
	require.NoError(t, browserConn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, data, err := browserConn.ReadMessage()
		if err != nil {
			break
		}
		last = string(data)
	}
	assert.Contains(t, last, "[-]", "startTunnel failure → error status")
	assert.Contains(t, last, "Failed to notify agent")
}

func TestHandleConnectArthas_WaitForTunnelTimeout(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 200 * time.Millisecond
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	agentConn := registerAgentViaWS(t, s, "a1")
	defer agentConn.Close()
	browserConn := dialConnectArthas(t, s, "a1")
	defer browserConn.Close()

	// Read the startTunnel the agent received, but do NOT deliver a tunnel.
	stFrame := readFrame(t, agentConn, 2*time.Second)
	require.Contains(t, stFrame, "method=startTunnel")
	clientConnID := extractClientConnID(t, stFrame)

	// WaitForTunnel times out (~200ms) → timeout status + cleanup + close.
	var last string
	require.NoError(t, browserConn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, data, err := browserConn.ReadMessage()
		if err != nil {
			break
		}
		last = string(data)
	}
	assert.Contains(t, last, "[!]", "timeout → [!] status")
	assert.Contains(t, last, "Timeout")
	assert.False(t, s.pendingStore.IsLocal(clientConnID), "pending must be cleaned up after timeout")
}

func TestHandleConnectArthas_WaitForTunnelCanceled(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 30 * time.Second // long; we cancel manually
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	agentConn := registerAgentViaWS(t, s, "a1")
	defer agentConn.Close()
	browserConn := dialConnectArthas(t, s, "a1")
	defer browserConn.Close()

	stFrame := readFrame(t, agentConn, 2*time.Second)
	require.Contains(t, stFrame, "method=startTunnel")
	clientConnID := extractClientConnID(t, stFrame)

	// Wait until the handler has entered WaitForTunnel (waiting status sent).
	drainUntil(t, browserConn, "Waiting", 2*time.Second)

	// Cancel the compat ctx → WaitForTunnel returns context.Canceled → the
	// handler sends a "maintenance" (closed) status and cleans up.
	s.cancel()

	var last string
	require.NoError(t, browserConn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, data, err := browserConn.ReadMessage()
		if err != nil {
			break
		}
		last = string(data)
	}
	assert.Contains(t, last, "maintenance", "ctx canceled → closed/maintenance status")
	assert.False(t, s.pendingStore.IsLocal(clientConnID), "pending must be cleaned up after cancel")
}

func TestHandleConnectArthas_AgentAppearsLocallyAfterWait(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 10 * time.Second // wait-loop deadline; ~2s poll
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	// Agent is NOT registered yet → handler enters the wait loop.
	browserConn := dialConnectArthas(t, s, "late-agent")
	defer browserConn.Close()

	// Register the agent while the handler sleeps in its first 2s poll.
	agentConn := registerAgentViaWS(t, s, "late-agent")
	defer agentConn.Close()

	// After ~2s the handler detects the agent and proceeds with the local
	// flow: it sends startTunnel. Read it and deliver a tunnel.
	stFrame := readFrame(t, agentConn, 6*time.Second)
	require.Contains(t, stFrame, "method=startTunnel")
	clientConnID := extractClientConnID(t, stFrame)

	tunnelServerSide, tunnelConsumer := newTunnelPair(t)
	defer tunnelConsumer.Close()
	require.NoError(t, s.pendingStore.DeliverTunnel(clientConnID, tunnelServerSide))

	// Drain browser status frames until ready ([+]), then verify relay.
	drainUntil(t, browserConn, "[+]", 3*time.Second)
	require.NoError(t, browserConn.WriteMessage(websocket.TextMessage, []byte("cmd")))
	assert.Equal(t, "cmd", readFrame(t, tunnelConsumer, 2*time.Second))
}

// ── HandleInternalConnectArthas ────────────────────────────────────────

func TestHandleInternalConnectArthas_RunsConnectFlow(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.CompatConnectTimeout = 200 * time.Millisecond
	s := newArthasURICompat(context.Background(), zap.NewNop(), cfg, nil, nil)
	t.Cleanup(func() { s.shutdown(context.Background()) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.HandleInternalConnectArthas(w, r, "ghost")
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	require.NoError(t, err)
	defer conn.Close()

	// handleConnectArthas runs (agent missing) → it sends a "connecting" status,
	// polls once (2s hardcoded interval), then sends an error status + close.
	// Drain frames until close (allow >2s for the poll cycle).
	var last string
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		last = string(data)
	}
	assert.Contains(t, last, "[-]", "internal connect of a missing agent → error status")
}

// ── proxyConnectArthas / proxyOpenTunnel (no-distributed branches) ────

func newCompatCtxConn(t *testing.T) (*compatConnContext, *websocket.Conn) {
	t.Helper()
	u, ch := handoffWSServer(t)
	cc, _, err := websocket.DefaultDialer.Dial(u, nil)
	require.NoError(t, err)
	sc := <-ch
	ctx := &compatConnContext{
		ingress:    ingressAdmin,
		conn:       sc,
		request:    httptest.NewRequest(http.MethodGet, "/", nil),
		query:      url.Values{},
		remoteAddr: "test:1",
		logger:     zap.NewNop(),
	}
	return ctx, cc
}

func TestProxyConnectArthas_NoDistributed_Closes(t *testing.T) {
	s := newLocalCompat(t)
	ctx, cc := newCompatCtxConn(t)
	defer cc.Close()

	s.proxyConnectArthas(ctx, "a1", "s1", "node:1")
	// Sends error status then closes → client read errors.
	assert.Contains(t, readFrame(t, cc, time.Second), "[-]")
	_ = cc.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := cc.ReadMessage()
	assert.Error(t, err)
}

func TestProxyOpenTunnel_NoDistributed_Closes(t *testing.T) {
	s := newLocalCompat(t)
	ctx, cc := newCompatCtxConn(t)
	defer cc.Close()

	s.proxyOpenTunnel(ctx, "c1", "node:1")
	_ = cc.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := cc.ReadMessage()
	assert.Error(t, err)
}

// ── cleanupPending ─────────────────────────────────────────────────────

func TestCleanupPending_DeletesAndClosesBrowserConn(t *testing.T) {
	s := newLocalCompat(t)
	browserCC, browserSC := newTunnelPair(t) // reuse as a stand-in browser conn pair
	defer browserCC.Close()

	require.NoError(t, s.pendingStore.CreateWithBrowserConn(context.Background(),
		&pending.PendingInfo{ClientConnID: "c1", SessionID: "s1", AgentID: "a1"}, browserSC))

	s.cleanupPending("c1", browserSC, "test reason")
	assert.False(t, s.pendingStore.IsLocal("c1"), "pending must be deleted")
	// browserSC was closed by cleanupPending.
	_ = browserCC.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := browserCC.ReadMessage()
	assert.Error(t, err, "browser conn must be closed")
}

// ── ListAgents / IsAgentOnline ─────────────────────────────────────────

func TestListAgents_LocalMode_FiltersTimedOut(t *testing.T) {
	s := newLocalCompat(t)
	healthy := injectAgent(t, s, "alive")
	defer healthy.Close()
	_ = injectAgent(t, s, "dead")

	s.mu.Lock()
	s.agents["dead"].lastPongAt = time.Now().Add(-10 * time.Hour).UnixMilli()
	s.mu.Unlock()

	agents := s.ListAgents()
	require.Len(t, agents, 1)
	assert.Equal(t, "alive", agents[0].AgentID)
}

func TestListAgents_DistributedMode(t *testing.T) {
	s, dm := newDistributedCompat(t)
	rr := dm.Registry().GetRedisRegistry()
	require.NotNil(t, rr)
	require.NoError(t, rr.Register(context.Background(), &registry.AgentInfo{
		AgentID: "rd-a1", AppID: "app", AppName: "svc", NodeID: "node-x", LastPongAt: time.Now().UnixMilli(),
	}))

	agents := s.ListAgents()
	require.Len(t, agents, 1)
	assert.Equal(t, "rd-a1", agents[0].AgentID)
	assert.Equal(t, "svc", agents[0].ServiceName)
}

func TestIsAgentOnline_LocalHealthyAndTimedOut(t *testing.T) {
	s := newLocalCompat(t)
	_ = injectAgent(t, s, "alive")
	assert.True(t, s.IsAgentOnline("alive"))
	assert.False(t, s.IsAgentOnline("nope"))

	s.mu.Lock()
	s.agents["alive"].lastPongAt = time.Now().Add(-10 * time.Hour).UnixMilli()
	s.mu.Unlock()
	assert.False(t, s.IsAgentOnline("alive"), "timed-out agent must be offline")
}

func TestIsAgentOnline_DistributedChecksRedis(t *testing.T) {
	s, dm := newDistributedCompat(t)
	rr := dm.Registry().GetRedisRegistry()
	require.NoError(t, rr.Register(context.Background(), &registry.AgentInfo{
		AgentID: "rd-a1", NodeID: "node-x", LastPongAt: time.Now().UnixMilli(),
	}))
	assert.True(t, s.IsAgentOnline("rd-a1"))
	assert.False(t, s.IsAgentOnline("missing"))
}
