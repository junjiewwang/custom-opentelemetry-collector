// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/opsconfig"
)

func newOpsTestHandler(t *testing.T) (*adminHandlers, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := opsconfig.NewStore(client, zap.NewNop(), "")
	require.NoError(t, store.Start())
	h := &adminHandlers{opsStore: store, logger: zap.NewNop()}
	return h, func() { store.Stop(); _ = client.Close(); mr.Close() }
}

func withURLParam(req *http.Request, key string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestGetOpsConfigsIncludesSchema(t *testing.T) {
	h, cleanup := newOpsTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	h.getOpsConfigs(rec, httptest.NewRequest(http.MethodGet, "/api/v2/ops/configs", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var view opsConfigView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.Equal(t, int64(0), view.Revision)
	require.Len(t, view.Schema, 1)
	require.Equal(t, opsconfig.KeyTunnelEndpoint, view.Schema[0].Key)
}

func TestSetOpsConfigValidationAndPersistence(t *testing.T) {
	h, cleanup := newOpsTestHandler(t)
	defer cleanup()

	bad := httptest.NewRequest(http.MethodPut, "/api/v2/ops/configs/key", bytes.NewBufferString(`{"value":"http://bad"}`))
	rec := httptest.NewRecorder()
	h.setOpsConfig(rec, withURLParam(bad, opsconfig.KeyTunnelEndpoint))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, int64(0), h.opsStore.Revision())

	good := httptest.NewRequest(http.MethodPut, "/api/v2/ops/configs/key",
		bytes.NewBufferString(`{"value":"ws://collector.example/v1/arthas/ws"}`))
	rec = httptest.NewRecorder()
	h.setOpsConfig(rec, withURLParam(good, opsconfig.KeyTunnelEndpoint))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(1), h.opsStore.Revision())
	v, ok := h.opsStore.Get(opsconfig.KeyTunnelEndpoint)
	require.True(t, ok)
	require.Equal(t, "ws://collector.example/v1/arthas/ws", v)

	del := httptest.NewRequest(http.MethodDelete, "/api/v2/ops/configs/key", nil)
	rec = httptest.NewRecorder()
	h.deleteOpsConfig(rec, withURLParam(del, opsconfig.KeyTunnelEndpoint))
	require.Equal(t, http.StatusOK, rec.Code)
	_, ok = h.opsStore.Get(opsconfig.KeyTunnelEndpoint)
	require.False(t, ok)
}

func TestOpsUnavailable(t *testing.T) {
	h := &adminHandlers{logger: zap.NewNop()}
	rec := httptest.NewRecorder()
	h.getOpsConfigs(rec, httptest.NewRequest(http.MethodGet, "/api/v2/ops/configs", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
