// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testUpgrader is a permissive upgrader for test stand-in servers.
var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsURLFromServer converts an httptest.Server's http:// URL to ws://.
func wsURLFromServer(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// fakeDeliverer captures tunnels delivered by HandleInternalOpenTunnel.
type fakeDeliverer struct {
	mu        sync.Mutex
	delivered map[string]*websocket.Conn
	err       error
}

func newFakeDeliverer() *fakeDeliverer {
	return &fakeDeliverer{delivered: make(map[string]*websocket.Conn)}
}

func (f *fakeDeliverer) DeliverTunnel(clientConnID string, conn *websocket.Conn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered[clientConnID] = conn
	return f.err
}

func (f *fakeDeliverer) get(id string) (*websocket.Conn, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.delivered[id]
	return c, ok
}

// echoWSServer starts a WS server that echoes every received message back to the
// sender. Serves any path (the proxy rewrites the path when building internal URLs).
func echoWSServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
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
	return wsURLFromServer(srv)
}

// blockingWSServer starts a WS server that upgrades, signals ready once (on the
// first accepted connection), then holds the connection open by reading until
// the peer closes. Used to occupy a proxy session indefinitely while still
// reacting to a close frame (so the relay can tear down).
func blockingWSServer(t *testing.T, ready chan<- struct{}) string {
	t.Helper()
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		once.Do(func() { close(ready) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return wsURLFromServer(srv)
}

// handoffWSServer starts a WS server that upgrades and hands the server-side
// conn to the caller via the returned channel (so the caller can pass it to the
// proxy as the "browser"/"agent" conn), then blocks until shutdown. The proxy's
// relay goroutines take over reading/writing the handed-off conn.
func handoffWSServer(t *testing.T) (string, <-chan *websocket.Conn) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ch <- conn
		<-r.Context().Done()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return wsURLFromServer(srv), ch
}

// ── pure / accessor surface ────────────────────────────────────────────

func TestBuildInternalURL(t *testing.T) {
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{
		InternalPathPrefix:  "/internal/v1/arthas",
		InternalToken:       "tok",
		InternalTokenHeader: "X-Internal-Token",
	}, nil)

	// Bare host → ws:// scheme added, path + params applied.
	u := p.BuildInternalURL("node-1:9000", "connect", map[string]string{"id": "a1"})
	assert.Equal(t, "ws://node-1:9000/internal/v1/arthas/proxy/connect?id=a1", u)

	// Already-schemed host is preserved (wss://).
	u = p.BuildInternalURL("wss://node-2:9000", "opentunnel", map[string]string{"clientConnectionId": "c1"})
	assert.Equal(t, "wss://node-2:9000/internal/v1/arthas/proxy/opentunnel?clientConnectionId=c1", u)

	// Multiple params are encoded (order is map-iteration dependent, so check substrings).
	u = p.BuildInternalURL("h:1", "connect", map[string]string{"id": "a", "x": "y"})
	assert.Contains(t, u, "id=a")
	assert.Contains(t, u, "x=y")
}

func TestNewWSProxy_Accessors(t *testing.T) {
	cfg := &ProxyConfig{
		InternalPathPrefix:  "/p",
		InternalToken:       "tok",
		InternalTokenHeader: "X-Internal-Token",
		WriteTimeout:        5,
		MaxProxySessions:    10,
	}
	p := NewWSProxy(zap.NewNop(), cfg, nil)
	require.NotNil(t, p)
	assert.Same(t, cfg, p.Config())
	assert.Equal(t, int64(0), p.ActiveSessions())
	assert.NoError(t, p.Close()) // no-op, never errors
}

func TestIsNormalClose(t *testing.T) {
	assert.False(t, isNormalClose(nil))
	assert.True(t, isNormalClose(io.EOF))
	assert.True(t, isNormalClose(&websocket.CloseError{Code: websocket.CloseNormalClosure}))
	assert.True(t, isNormalClose(&websocket.CloseError{Code: websocket.CloseGoingAway}))
	assert.False(t, isNormalClose(&websocket.CloseError{Code: websocket.ClosePolicyViolation}))
	assert.False(t, isNormalClose(assert.AnError))
}

// ── HandleInternalOpenTunnel ───────────────────────────────────────────

func TestHandleInternalOpenTunnel_MissingID_400(t *testing.T) {
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{InternalPathPrefix: "/p"}, newFakeDeliverer())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p/proxy/opentunnel", nil) // no clientConnectionId
	p.HandleInternalOpenTunnel(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleInternalOpenTunnel_DeliversTunnel(t *testing.T) {
	del := newFakeDeliverer()
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{InternalPathPrefix: "/p"}, del)

	srv := httptest.NewServer(http.HandlerFunc(p.HandleInternalOpenTunnel))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(wsURLFromServer(srv)+"?clientConnectionId=c1", nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		_, ok := del.get("c1")
		return ok
	}, 2*time.Second, 10*time.Millisecond, "deliverer must receive the tunnel conn")
	// Wrong ID not delivered.
	_, ok := del.get("other")
	assert.False(t, ok)
}

func TestHandleInternalOpenTunnel_DelivererError_ClosesConn(t *testing.T) {
	del := newFakeDeliverer()
	del.err = assert.AnError
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{InternalPathPrefix: "/p"}, del)

	srv := httptest.NewServer(http.HandlerFunc(p.HandleInternalOpenTunnel))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(wsURLFromServer(srv)+"?clientConnectionId=c1", nil)
	require.NoError(t, err)
	defer conn.Close()

	// Deliverer returned an error → handler closes the conn → client read errors.
	require.Eventually(t, func() bool {
		_, ok := del.get("c1")
		return ok
	}, 2*time.Second, 10*time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "conn must be closed when delivery fails")
}

// ── ProxyConnectArthas / ProxyOpenTunnel relay ─────────────────────────

// runConnectRelay wires browserClient <-> proxy <-> echoTarget and returns the
// browserClient conn plus a done channel that closes when ProxyConnectArthas returns.
func runConnectRelay(t *testing.T, p *WSProxy, targetNodeAddr, agentID string) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	browserURL, browserCh := handoffWSServer(t)
	browserClient, _, err := websocket.DefaultDialer.Dial(browserURL, nil)
	require.NoError(t, err)

	browserConn := <-browserCh
	done := make(chan struct{})
	go func() {
		_ = p.ProxyConnectArthas(context.Background(), targetNodeAddr, browserConn, agentID)
		close(done)
	}()
	// Wait until the proxy has dialed the target so the relay path is established.
	require.Eventually(t, func() bool { return p.ActiveSessions() == 1 }, time.Second, 5*time.Millisecond)
	return browserClient, done
}

func TestProxyConnectArthas_RelaysBidirectionally(t *testing.T) {
	targetURL := echoWSServer(t)
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{
		InternalPathPrefix: "/internal/v1/arthas",
		InternalToken:      "tok", InternalTokenHeader: "X-Internal-Token",
		WriteTimeout: 5,
	}, nil)

	browserClient, done := runConnectRelay(t, p, targetURL, "a1")
	defer browserClient.Close()

	// browser -> target (echo) -> browser
	require.NoError(t, browserClient.WriteMessage(websocket.TextMessage, []byte("ping")))
	_ = browserClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := browserClient.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	assert.Equal(t, "ping", string(data))

	// Closing the client tears down the relay; ProxyConnectArthas returns.
	require.NoError(t, browserClient.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProxyConnectArthas did not return after client close")
	}
}

func TestProxyOpenTunnel_RelaysBidirectionally(t *testing.T) {
	targetURL := echoWSServer(t)
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{
		InternalPathPrefix: "/internal/v1/arthas",
		InternalToken:      "tok", InternalTokenHeader: "X-Internal-Token",
		WriteTimeout: 5,
	}, nil)

	// openTunnel relays an agent conn; reuse the same handoff wiring.
	agentURL, agentCh := handoffWSServer(t)
	agentClient, _, err := websocket.DefaultDialer.Dial(agentURL, nil)
	require.NoError(t, err)
	defer agentClient.Close()
	agentConn := <-agentCh

	done := make(chan struct{})
	go func() {
		_ = p.ProxyOpenTunnel(context.Background(), targetURL, agentConn, "c1")
		close(done)
	}()
	require.Eventually(t, func() bool { return p.ActiveSessions() == 1 }, time.Second, 5*time.Millisecond)

	require.NoError(t, agentClient.WriteMessage(websocket.BinaryMessage, []byte("frame")))
	_ = agentClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := agentClient.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.BinaryMessage, mt)
	assert.Equal(t, "frame", string(data))

	require.NoError(t, agentClient.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProxyOpenTunnel did not return after client close")
	}
}

func TestProxyConnectArthas_DialError(t *testing.T) {
	p := NewWSProxy(zap.NewNop(), &ProxyConfig{
		InternalPathPrefix:  "/internal/v1/arthas",
		InternalToken:       "tok",
		InternalTokenHeader: "X-Internal-Token",
		WriteTimeout:        5,
	}, nil)

	// Dial a closed port → dialer fails fast (connection refused).
	// Use a browser conn that we never read; the dial fails before relay starts.
	browserURL, browserCh := handoffWSServer(t)
	browserClient, _, err := websocket.DefaultDialer.Dial(browserURL, nil)
	require.NoError(t, err)
	defer browserClient.Close()
	browserConn := <-browserCh

	// 127.0.0.1:1 is guaranteed to refuse connections.
	err = p.ProxyConnectArthas(context.Background(), "ws://127.0.0.1:1", browserConn, "a1")
	assert.Error(t, err)
	assert.Equal(t, int64(0), p.ActiveSessions(), "session counter must decrement after dial failure")
}

func TestProxyConnectArthas_MaxSessionsReached(t *testing.T) {
	ready := make(chan struct{})
	targetURL := blockingWSServer(t, ready)

	p := NewWSProxy(zap.NewNop(), &ProxyConfig{
		InternalPathPrefix:  "/internal/v1/arthas",
		InternalToken:       "tok",
		InternalTokenHeader: "X-Internal-Token",
		WriteTimeout:        5,
		MaxProxySessions:    1,
	}, nil)

	// First session occupies the single slot.
	browserURL1, ch1 := handoffWSServer(t)
	bc1, _, err := websocket.DefaultDialer.Dial(browserURL1, nil)
	require.NoError(t, err)
	defer bc1.Close()
	bconn1 := <-ch1

	done1 := make(chan struct{})
	go func() {
		_ = p.ProxyConnectArthas(context.Background(), targetURL, bconn1, "a1")
		close(done1)
	}()

	// Wait until the first session has dialed the target (slot is now full).
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("first session never reached target")
	}
	require.Equal(t, int64(1), p.ActiveSessions())

	// Second session must be rejected without dialing.
	browserURL2, ch2 := handoffWSServer(t)
	bc2, _, err := websocket.DefaultDialer.Dial(browserURL2, nil)
	require.NoError(t, err)
	defer bc2.Close()
	bconn2 := <-ch2

	err = p.ProxyConnectArthas(context.Background(), targetURL, bconn2, "a2")
	assert.ErrorIs(t, err, ErrMaxProxySessionsReached)
	assert.Equal(t, int64(1), p.ActiveSessions(), "rejected session must not change the counter")

	// Tear down the first session by closing its browser conn.
	require.NoError(t, bc1.Close())
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("first session did not return after close")
	}
}
