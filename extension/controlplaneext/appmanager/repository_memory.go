// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"sync"
)

// MemoryAppRepository implements AppRepository using in-memory maps.
// Pure data access — no business logic. Suitable for testing and single-node deployments.
type MemoryAppRepository struct {
	mu     sync.RWMutex
	apps   map[string]*AppInfo // id → app
	tokens map[string]string   // token → id
}

// NewMemoryAppRepository creates a new in-memory AppRepository.
func NewMemoryAppRepository() *MemoryAppRepository {
	return &MemoryAppRepository{
		apps:   make(map[string]*AppInfo),
		tokens: make(map[string]string),
	}
}

var _ AppRepository = (*MemoryAppRepository)(nil)

// Insert stores a new app. Returns an error if the ID or token is already in use.
func (r *MemoryAppRepository) Insert(ctx context.Context, app *AppInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.apps[app.ID]; exists {
		return ErrNotFound // TODO: consider a more specific error type in future
	}
	if _, exists := r.tokens[app.Token]; exists {
		return ErrNotFound
	}

	// Store a copy to prevent external mutation
	clone := *app
	r.apps[app.ID] = &clone
	r.tokens[app.Token] = app.ID
	return nil
}

// FindByID returns the app for the given ID, or ErrNotFound.
func (r *MemoryAppRepository) FindByID(ctx context.Context, id string) (*AppInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	app, ok := r.apps[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *app
	return &clone, nil
}

// FindByToken returns the app associated with the given token, or ErrNotFound.
func (r *MemoryAppRepository) FindByToken(ctx context.Context, token string) (*AppInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, ok := r.tokens[token]
	if !ok {
		return nil, ErrNotFound
	}
	app, ok := r.apps[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *app
	return &clone, nil
}

// Save fully overwrites the stored app. Returns ErrNotFound if the ID doesn't exist.
func (r *MemoryAppRepository) Save(ctx context.Context, app *AppInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.apps[app.ID]
	if !ok {
		return ErrNotFound
	}

	// Update token mapping if token changed
	if existing.Token != app.Token {
		delete(r.tokens, existing.Token)
		r.tokens[app.Token] = app.ID
	}

	clone := *app
	r.apps[app.ID] = &clone
	return nil
}

// Delete removes the app and its token mapping. Returns ErrNotFound if not found.
func (r *MemoryAppRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	app, ok := r.apps[id]
	if !ok {
		return ErrNotFound
	}

	delete(r.tokens, app.Token)
	delete(r.apps, id)
	return nil
}

// List returns all stored apps. Returns an empty slice if none exist.
func (r *MemoryAppRepository) List(ctx context.Context) ([]*AppInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	apps := make([]*AppInfo, 0, len(r.apps))
	for _, app := range r.apps {
		clone := *app
		apps = append(apps, &clone)
	}
	return apps, nil
}
