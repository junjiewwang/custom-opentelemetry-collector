// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opsconfig

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidateTunnelEndpoint(t *testing.T) {
	valid := []string{
		"ws://custom-collector.devcloud/v1/arthas/ws",
		"wss://collector.example.com/v1/arthas/ws",
		"ws://10.0.0.1:4318/v1/arthas/ws",
	}
	for _, v := range valid {
		require.NoError(t, Validate(KeyTunnelEndpoint, v), v)
	}

	invalid := []string{
		"",
		"http://host/v1/arthas/ws",
		"ws://user:pass@host/v1/arthas/ws",
		"ws://host/v1/arthas/ws?token=abc",
		"ws://host/v1/arthas/ws#frag",
		"ws://host/wrong/path",
		"ws:///v1/arthas/ws",
		"not-a-url",
	}
	for _, v := range invalid {
		require.Error(t, Validate(KeyTunnelEndpoint, v), v)
	}
	require.Error(t, Validate("unknown.key", "ws://host/v1/arthas/ws"))
}

func TestRevisionTagChanges(t *testing.T) {
	s := NewStore(nil, nil, "")
	require.Equal(t, "ops-0", s.RevisionTag())
	s.mu.Lock()
	s.revision = 7
	s.mu.Unlock()
	require.Equal(t, "ops-7", s.RevisionTag())
}

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewStore(client, zap.NewNop(), "")
	require.NoError(t, s.Start())
	return s, func() { s.Stop(); _ = client.Close(); mr.Close() }
}

func TestStorePutDeleteAudit(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	_, ok := s.Get(KeyTunnelEndpoint)
	require.False(t, ok)
	require.Equal(t, int64(0), s.Revision())

	rev, err := s.Put(context.Background(), KeyTunnelEndpoint, "ws://collector.example/v1/arthas/ws", "admin")
	require.NoError(t, err)
	require.Equal(t, int64(1), rev)
	v, ok := s.Get(KeyTunnelEndpoint)
	require.True(t, ok)
	require.Equal(t, "ws://collector.example/v1/arthas/ws", v)
	require.Equal(t, "ops-1", s.RevisionTag())

	rev2, err := s.Put(context.Background(), KeyTunnelEndpoint, "wss://other.example/v1/arthas/ws", "admin")
	require.NoError(t, err)
	require.Equal(t, int64(2), rev2)

	entries, err := s.Audit(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, int64(2), entries[0].Revision)
	require.Equal(t, "ws://collector.example/v1/arthas/ws", entries[0].OldValue)

	rev3, err := s.Delete(context.Background(), KeyTunnelEndpoint, "admin")
	require.NoError(t, err)
	require.Equal(t, int64(3), rev3)
	_, ok = s.Get(KeyTunnelEndpoint)
	require.False(t, ok)
}

func TestStoreRejectsInvalidInput(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	_, err := s.Put(context.Background(), KeyTunnelEndpoint, "http://bad/v1/arthas/ws", "admin")
	require.ErrorIs(t, err, ErrValidation)
	_, err = s.Put(context.Background(), "un.registered", "value", "admin")
	require.ErrorIs(t, err, ErrValidation)
	require.Equal(t, int64(0), s.Revision(), "failed writes must not increment revision")
}

func TestStorePubSubRefreshesSecondReplica(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	c1 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	c2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer c1.Close()
	defer c2.Close()

	writer := NewStore(c1, zap.NewNop(), "")
	reader := NewStore(c2, zap.NewNop(), "")
	require.NoError(t, writer.Start())
	require.NoError(t, reader.Start())
	defer writer.Stop()
	defer reader.Stop()
	changes := reader.SubscribeChanges()

	_, err := writer.Put(context.Background(), KeyTunnelEndpoint, "ws://collector.example/v1/arthas/ws", "admin")
	require.NoError(t, err)

	deadline := time.After(3 * time.Second)
	for {
		v, ok := reader.Get(KeyTunnelEndpoint)
		if ok && v == "ws://collector.example/v1/arthas/ws" {
			break
		}
		select {
		case <-changes:
		case <-deadline:
			t.Fatal("reader replica did not observe the published change")
		case <-time.After(20 * time.Millisecond):
		}
	}
	require.Equal(t, writer.Revision(), reader.Revision())
}

func TestSharedProviderReturnsSameStore(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	p := &Provider{}
	s1, release1, err := p.Acquire(client, zap.NewNop())
	require.NoError(t, err)
	s2, release2, err := p.Acquire(client, zap.NewNop())
	require.NoError(t, err)
	require.Same(t, s1, s2)
	release1()
	require.NotNil(t, p.store, "shared store must stay alive while another owner holds it")
	release2()
	require.Nil(t, p.store)
}
