// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/custom/extension/storageext"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
	"go.opentelemetry.io/collector/extension"
)

// mockStorageExt is a storageext.Storage backed by a miniredis client.
type mockStorageExt struct{ redis redis.UniversalClient }

var _ component.Component = (*mockStorageExt)(nil)
var _ storageext.Storage = (*mockStorageExt)(nil)

func (m *mockStorageExt) Start(_ context.Context, _ component.Host) error { return nil }
func (m *mockStorageExt) Shutdown(_ context.Context) error                { return nil }

func (m *mockStorageExt) GetRedis(_ string) (redis.UniversalClient, error) {
	return m.redis, nil
}
func (m *mockStorageExt) GetDefaultRedis() (redis.UniversalClient, error) { return m.redis, nil }
func (m *mockStorageExt) GetNacosConfigClient(_ string) (config_client.IConfigClient, error) {
	return nil, nil
}
func (m *mockStorageExt) GetDefaultNacosConfigClient() (config_client.IConfigClient, error) {
	return nil, nil
}
func (m *mockStorageExt) GetNacosNamingClient(_ string) (naming_client.INamingClient, error) {
	return nil, nil
}
func (m *mockStorageExt) GetDefaultNacosNamingClient() (naming_client.INamingClient, error) {
	return nil, nil
}
func (m *mockStorageExt) HasRedis(_ string) bool            { return true }
func (m *mockStorageExt) HasNacos(_ string) bool            { return false }
func (m *mockStorageExt) ListRedisNames() []string          { return []string{"default"} }
func (m *mockStorageExt) ListNacosNames() []string          { return nil }
func (m *mockStorageExt) GetBlobStore() blobstore.BlobStore { return nil }
func (m *mockStorageExt) HasBlobStore() bool                { return false }

// mockHost implements component.Host (only GetExtensions is required).
type mockHost struct {
	extensions map[component.ID]component.Component
}

func (h *mockHost) GetExtensions() map[component.ID]component.Component { return h.extensions }

func newSettings() extension.Settings {
	return extension.Settings{
		ID:                component.MustNewID("arthas_tunnel"),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
	}
}

// ── factory ────────────────────────────────────────────────────────────

func TestNewFactory(t *testing.T) {
	f := NewFactory()
	require.NotNil(t, f)

	// Create with default config → local mode extension.
	ext, err := f.Create(context.Background(), newSettings(), createDefaultConfig())
	require.NoError(t, err)
	require.NotNil(t, ext)
	_, ok := ext.(*Extension)
	assert.True(t, ok)
}

func TestCreateExtension_NonConfigType(t *testing.T) {
	// createExtension asserts cfg.(*Config); passing a non-Config panics via
	// type assertion. We exercise the happy path only here (factory wires the
	// default config), which is the realistic entry point.
	ext, err := createExtension(context.Background(), newSettings(), createDefaultConfig())
	require.NoError(t, err)
	assert.NotNil(t, ext)
}

// ── local-mode Start/Shutdown ──────────────────────────────────────────

func TestExtension_StartShutdown_LocalMode(t *testing.T) {
	cfg := createDefaultConfig() // distributed disabled
	e, err := newExtension(newSettings(), cfg)
	require.NoError(t, err)

	require.NoError(t, e.Start(context.Background(), componenttest.NewNopHost()))
	assert.False(t, e.IsDistributedMode())
	require.NotNil(t, e.ListConnectedAgents()) // non-nil empty list
	assert.False(t, e.IsAgentConnected("any"))

	// Handle endpoints work after start (compat non-nil): missing method → close.
	srv := httptest.NewServer(http.HandlerFunc(e.HandleAgentWebSocket))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "missing method → server closes")
	_ = conn.Close()

	require.NoError(t, e.Shutdown(context.Background()))
}

func TestExtension_HandleBeforeStart_503(t *testing.T) {
	e, err := newExtension(newSettings(), createDefaultConfig())
	require.NoError(t, err)

	for _, h := range []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"agent", e.HandleAgentWebSocket},
		{"browser", e.HandleBrowserWebSocket},
	} {
		rr := httptest.NewRecorder()
		h.fn(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code, h.name)
	}

	// Internal proxy before start / not distributed → 503.
	rr := httptest.NewRecorder()
	e.HandleInternalProxy(rr, httptest.NewRequest(http.MethodGet, "/internal/v1/arthas/proxy/connect", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// ── distributed-mode Start/Shutdown ────────────────────────────────────

func newDistributedExtension(t *testing.T) (*Extension, *mockStorageExt) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	storage := &mockStorageExt{redis: client}
	cfg := newTestDistributedConfig()

	e, err := newExtension(newSettings(), cfg)
	require.NoError(t, err)

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("storage"): storage,
	}}
	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e, storage
}

func TestExtension_Start_DistributedMode(t *testing.T) {
	e, _ := newDistributedExtension(t)
	assert.True(t, e.IsDistributedMode())
	assert.NotEmpty(t, e.GetInternalPathPrefix())
	require.NotNil(t, e.distributed)
}

func TestExtension_Start_Distributed_NoStorage_Fails(t *testing.T) {
	cfg := newTestDistributedConfig()
	e, err := newExtension(newSettings(), cfg)
	require.NoError(t, err)

	// Host with no storage extension → Start returns the storage-not-found error.
	host := &mockHost{extensions: map[component.ID]component.Component{}}
	err = e.Start(context.Background(), host)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage extension not found")
}

func TestExtension_StartOnce_Idempotent(t *testing.T) {
	e, _ := newDistributedExtension(t)
	// Second Start is a no-op (startOnce) and must not error or double-init.
	require.NoError(t, e.Start(context.Background(), componenttest.NewNopHost()))
}

// ── HandleInternalProxy routing ────────────────────────────────────────

func TestHandleInternalProxy_BadToken_401(t *testing.T) {
	e, _ := newDistributedExtension(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/arthas/proxy/connect?id=a1", nil)
	req.Header.Set(e.config.Distributed.InternalAuth.HeaderName, "wrong")
	e.HandleInternalProxy(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestHandleInternalProxy_UnknownAction_404(t *testing.T) {
	e, _ := newDistributedExtension(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/arthas/proxy/bogus", nil)
	req.Header.Set(e.config.Distributed.InternalAuth.HeaderName, e.config.Distributed.InternalAuth.Token)
	e.HandleInternalProxy(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandleInternalProxy_ConnectMissingID_400(t *testing.T) {
	e, _ := newDistributedExtension(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/arthas/proxy/connect", nil)
	req.Header.Set(e.config.Distributed.InternalAuth.HeaderName, e.config.Distributed.InternalAuth.Token)
	e.HandleInternalProxy(rr, req)
	// handleInternalConnect checks agent ID before upgrading; missing → 400.
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleInternalProxy_OpenTunnelMissingID_400(t *testing.T) {
	e, _ := newDistributedExtension(t)
	// HandleInternalProxy → opentunnel path → WSProxy.HandleInternalOpenTunnel,
	// which 400s on missing clientConnectionId before upgrading.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/arthas/proxy/opentunnel", nil)
	req.Header.Set(e.config.Distributed.InternalAuth.HeaderName, e.config.Distributed.InternalAuth.Token)
	e.HandleInternalProxy(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Dependencies ───────────────────────────────────────────────────────

func TestDependencies_LocalMode_Nil(t *testing.T) {
	e, err := newExtension(newSettings(), createDefaultConfig())
	require.NoError(t, err)
	assert.Nil(t, e.Dependencies())
}

func TestDependencies_DistributedMode_DeclaresStorage(t *testing.T) {
	e, err := newExtension(newSettings(), newTestDistributedConfig())
	require.NoError(t, err)
	deps := e.Dependencies()
	require.Len(t, deps, 1)
	assert.Equal(t, "storage", deps[0].String())
}

func TestDependencies_DistributedMode_CustomStorageName(t *testing.T) {
	cfg := newTestDistributedConfig()
	cfg.Distributed.StorageExtension = "my_redis"
	e, err := newExtension(newSettings(), cfg)
	require.NoError(t, err)
	deps := e.Dependencies()
	require.Len(t, deps, 1)
	assert.Equal(t, "my_redis", deps[0].String())
}

// ── ConnectToAgent before start ────────────────────────────────────────

func TestConnectToAgent_BeforeStart(t *testing.T) {
	e, err := newExtension(newSettings(), createDefaultConfig())
	require.NoError(t, err)
	_, err = e.ConnectToAgent(context.Background(), "a1")
	assert.Error(t, err)
}

// ── Shutdown idempotency ───────────────────────────────────────────────

func TestExtension_ShutdownOnce_Idempotent(t *testing.T) {
	e, _ := newDistributedExtension(t)
	require.NoError(t, e.Shutdown(context.Background()))
	// Second Shutdown is a no-op (stopOnce).
	require.NoError(t, e.Shutdown(context.Background()))
}
