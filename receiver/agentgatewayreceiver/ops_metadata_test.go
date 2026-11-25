// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/opsconfig"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
)

func TestOpsMetadataProviderInjectsValuesAndRevision(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := opsconfig.NewStore(client, zap.NewNop(), "")
	require.NoError(t, store.Start())
	defer store.Stop()

	_, err := store.Put(context.Background(), opsconfig.KeyTunnelEndpoint,
		"ws://custom-collector.devcloud/v1/arthas/ws", "admin")
	require.NoError(t, err)

	p := &opsMetadataProvider{store: store}
	meta := p.ProvideMetadata(context.Background(), &longpoll.PollRequest{AgentID: "a1"})
	require.Equal(t, "ws://custom-collector.devcloud/v1/arthas/ws", meta[opsconfig.KeyTunnelEndpoint])
	require.Equal(t, "1", meta["ops.revision"])
}

func TestOpsMetadataProviderNilSafe(t *testing.T) {
	var p *opsMetadataProvider
	require.Nil(t, p.ProvideMetadata(context.Background(), &longpoll.PollRequest{}))
	require.Nil(t, (&opsMetadataProvider{}).ProvideMetadata(context.Background(), &longpoll.PollRequest{}))
}
