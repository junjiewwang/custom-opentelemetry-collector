// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opsconfig

import (
	"sync"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Provider owns the process-wide Store so multiple components (admin API and
// config-poll receiver) share one Redis connection, one pub/sub subscription
// and one lifecycle. This removes duplicated initialization and guarantees
// both sides always observe the same in-memory snapshot.
type Provider struct {
	mu    sync.Mutex
	store *Store
	refs  int
}

var defaultProvider = &Provider{}

// Shared returns the process-wide provider.
func Shared() *Provider { return defaultProvider }

// Acquire returns the shared store, creating and starting it on first use.
// The returned release func must be called by the owner during Shutdown.
func (p *Provider) Acquire(client redis.UniversalClient, logger *zap.Logger) (*Store, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.store != nil {
		p.refs++
		return p.store, p.releaseLocked, nil
	}
	store := NewStore(client, logger, "")
	if err := store.Start(); err != nil {
		return nil, nil, err
	}
	p.store = store
	p.refs = 1
	return store, p.releaseLocked, nil
}

// releaseLocked decrements the reference count and stops the shared store
// when the last owner exits. It uses Provider.mu, so it must not be invoked
// while that lock is held by the caller.
func (p *Provider) releaseLocked() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refs <= 1 {
		p.refs = 0
		if p.store != nil {
			p.store.Stop()
			p.store = nil
		}
		return
	}
	p.refs--
}
