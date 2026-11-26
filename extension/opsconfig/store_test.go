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

// The legacy full-URL key is no longer supported after the one-shot migration.
const removedTunnelEndpointKey = "ops.arthas.tunnel_endpoint"

func TestValidateTunnelBaseURL(t *testing.T) {
	valid := []string{
		"http://10.0.0.1:4318",
		"https://custom-collector.devcloud",
		"wss://gw.example.com/otel",
		"ws://collector.example:4318/prefix",
	}
	for _, v := range valid {
		require.NoError(t, Validate(KeyTunnelBaseURL, v), v)
	}

	invalid := []string{
		"",
		"ftp://host",
		"http://user:pass@host",
		"https://host?token=abc",
		"https://host#frag",
		"https://host:70000",
		"https://host/v1/arthas/ws",
		"https://host/prefix/v1/arthas/ws",
		"https://host/prefix/",
		"not-a-url",
	}
	for _, v := range invalid {
		require.Error(t, Validate(KeyTunnelBaseURL, v), v)
	}
	require.ErrorIs(t, Validate(KeyTunnelBaseURL, "ftp://host"), ErrValidation)
	require.ErrorIs(t, Validate("unknown.key", "https://host"), ErrValidation)
}

func TestSchemaOnlyContainsBaseURL(t *testing.T) {
	keys := map[string]string{}
	for _, entry := range Schema() {
		keys[entry.Key] = entry.ValueType
	}
	require.Len(t, keys, 1)
	require.Equal(t, "base_url", keys[KeyTunnelBaseURL])
	require.NotContains(t, keys, removedTunnelEndpointKey)
}

func TestRemovedTunnelEndpointIsUnsupported(t *testing.T) {
	require.ErrorIs(t,
		Validate(removedTunnelEndpointKey, "ws://collector.example/v1/arthas/ws"),
		ErrValidation)

	s, cleanup := newTestStore(t)
	defer cleanup()
	_, err := s.Put(context.Background(), removedTunnelEndpointKey,
		"ws://collector.example/v1/arthas/ws", "admin")
	require.ErrorIs(t, err, ErrValidation)
	_, err = s.Delete(context.Background(), removedTunnelEndpointKey, "admin")
	require.ErrorIs(t, err, ErrValidation, "an unsupported delete is a 400, not a backend failure")
	require.Equal(t, int64(0), s.Revision(), "rejected writes must not change revision")
}

func TestStorePublishesTunnelBaseURL(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	value := "https://custom-collector.devcloud/otel"
	_, err := s.Put(context.Background(), KeyTunnelBaseURL, value, "admin")
	require.NoError(t, err)

	// opsMetadataProvider iterates Snapshot(), so the key is delivered
	// without any change to the metadata provider.
	snapshot, _ := s.Snapshot()
	require.Equal(t, value, snapshot[KeyTunnelBaseURL])

	_, err = s.Delete(context.Background(), KeyTunnelBaseURL, "admin")
	require.NoError(t, err)
	afterDelete, _ := s.Snapshot()
	_, ok := afterDelete[KeyTunnelBaseURL]
	require.False(t, ok, "deleting the override restores derived mode")
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

	_, ok := s.Get(KeyTunnelBaseURL)
	require.False(t, ok)
	require.Equal(t, int64(0), s.Revision())

	first := "ws://collector.example"
	rev, err := s.Put(context.Background(), KeyTunnelBaseURL, first, "admin")
	require.NoError(t, err)
	require.Equal(t, int64(1), rev)
	v, ok := s.Get(KeyTunnelBaseURL)
	require.True(t, ok)
	require.Equal(t, first, v)
	require.Equal(t, "ops-1", s.RevisionTag())

	rev2, err := s.Put(context.Background(), KeyTunnelBaseURL, "wss://other.example/otel", "admin")
	require.NoError(t, err)
	require.Equal(t, int64(2), rev2)

	entries, err := s.Audit(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, int64(2), entries[0].Revision)
	require.Equal(t, first, entries[0].OldValue)

	rev3, err := s.Delete(context.Background(), KeyTunnelBaseURL, "admin")
	require.NoError(t, err)
	require.Equal(t, int64(3), rev3)
	_, ok = s.Get(KeyTunnelBaseURL)
	require.False(t, ok)
}

func TestStoreRejectsInvalidInput(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	_, err := s.Put(context.Background(), KeyTunnelBaseURL, "ftp://bad", "admin")
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

	value := "ws://collector.example"
	_, err := writer.Put(context.Background(), KeyTunnelBaseURL, value, "admin")
	require.NoError(t, err)

	deadline := time.After(3 * time.Second)
	for {
		v, ok := reader.Get(KeyTunnelBaseURL)
		if ok && v == value {
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
