// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

func TestEffectiveEtagIncludesOpsRevision(t *testing.T) {
	h := NewConfigPollHandler(zap.NewNop(), nil)
	require.Equal(t, "business-1", h.effectiveEtag("business-1"))

	h.SetOpsRevisionProvider(func() string { return "ops-7" })
	require.Equal(t, "business-1|ops-7", h.effectiveEtag("business-1"))

	h.SetOpsRevisionProvider(func() string { return "" })
	require.Equal(t, "business-1", h.effectiveEtag("business-1"))
}

type staticMetadataProvider struct{ values map[string]string }

func (p staticMetadataProvider) Name() string { return "static" }
func (p staticMetadataProvider) ProvideMetadata(_ context.Context, _ *PollRequest) map[string]string {
	return p.values
}

func TestNotifyOpsChangedDeliversMetadataWithoutVersionDowngrade(t *testing.T) {
	h := NewConfigPollHandler(zap.NewNop(), nil)
	h.running.Store(true)
	h.SetOpsRevisionProvider(func() string { return "ops-1" })
	h.RegisterMetadataProvider(staticMetadataProvider{values: map[string]string{
		"ops.arthas.tunnel_endpoint": "ws://ops.example/v1/arthas/ws",
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := &ConfigWaiter{
		agentID:           "agent-1",
		appID:             "app-1",
		serviceName:       "svc-1",
		lastConfigVersion: "v42",
		resultChan:        make(chan *HandlerResult, 1),
		ctx:               ctx,
		cancel:            cancel,
	}
	state := &serviceState{}
	state.waiters.Register(waiter.agentID, waiter)
	h.services.Store(AgentKey(waiter.appID, waiter.serviceName), state)

	h.NotifyOpsChanged()

	select {
	case result := <-waiter.resultChan:
		require.NotNil(t, result)
		require.True(t, result.HasChanges)
		require.NotNil(t, result.Response)
		require.NotNil(t, result.Response.Config)
		require.Equal(t, "v42", result.Response.Config.Version, "business version must not be downgraded")
		require.Equal(t, "v42", result.Response.ConfigVersion)
		require.Equal(t, "|ops-1", result.Response.ConfigEtag)
		require.Equal(
			t,
			"ws://ops.example/v1/arthas/ws",
			result.Response.Config.ServerMetadata["ops.arthas.tunnel_endpoint"],
		)
		require.Equal(t, "", result.Response.Config.ServerMetadata["nonexistent"])
	case <-time.After(time.Second):
		t.Fatal("waiter was not woken")
	}
}

func TestNotifyOpsChangedWithEmptyCacheKeepsMetadata(t *testing.T) {
	h := NewConfigPollHandler(zap.NewNop(), nil)
	h.RegisterMetadataProvider(staticMetadataProvider{values: map[string]string{"ops.revision": "3"}})

	waiter := &ConfigWaiter{
		agentID:    "agent-2",
		resultChan: make(chan *HandlerResult, 1),
		ctx:        context.Background(),
		cancel:     func() {},
	}
	state := &serviceState{config: newConfigCache("", "", nil, zap.NewNop())}
	state.config.Set(&model.AgentConfig{Version: "v7", Etag: "e7"})
	state.waiters.Register(waiter.agentID, waiter)
	h.services.Store(AgentKey("", ""), state)

	h.NotifyOpsChanged()
	result := <-waiter.resultChan
	require.Equal(t, "v7", result.Response.Config.Version)
	require.Equal(t, "3", result.Response.Config.ServerMetadata["ops.revision"])
}
