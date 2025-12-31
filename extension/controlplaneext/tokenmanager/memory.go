// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MemoryTokenManager implements TokenManager using in-memory storage.
type MemoryTokenManager struct {
	logger *zap.Logger
	config Config

	// apps stores apps by ID.
	apps map[string]*AppInfo

	// tokens maps token to app ID.
	tokens map[string]string

	mu      sync.RWMutex
	started bool
}

// NewMemoryTokenManager creates a new in-memory token manager.
func NewMemoryTokenManager(logger *zap.Logger, config Config) *MemoryTokenManager {
	return &MemoryTokenManager{
		logger: logger,
		config: config,
		apps:   make(map[string]*AppInfo),
		tokens: make(map[string]string),
	}
}

// Ensure MemoryTokenManager implements TokenManager.
var _ TokenManager = (*MemoryTokenManager)(nil)

// Start initializes the token manager.
func (m *MemoryTokenManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting memory token manager")
	m.started = true
	return nil
}

// Close releases resources.
func (m *MemoryTokenManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.started = false
	m.logger.Info("Memory token manager stopped")
	return nil
}

// CreateApp creates a new application group and generates a token.
func (m *MemoryTokenManager) CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for duplicate name
	for _, app := range m.apps {
		if app.Name == req.Name {
			return nil, errors.New("app name already exists")
		}
	}

	// Generate ID
	id, err := GenerateID()
	if err != nil {
		return nil, err
	}

	// Use custom token if provided, otherwise generate one
	token := req.Token
	if token == "" {
		token, err = GenerateToken(0)
		if err != nil {
			return nil, err
		}
	} else {
		// Check for duplicate token
		if _, exists := m.tokens[token]; exists {
			return nil, errors.New("token already exists")
		}
	}

	now := time.Now()
	app := &AppInfo{
		ID:          id,
		Name:        req.Name,
		Token:       token,
		Description: req.Description,
		Metadata:    req.Metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
		Status:      "active",
	}

	m.apps[id] = app
	m.tokens[token] = id

	m.logger.Info("App created",
		zap.String("id", id),
		zap.String("name", req.Name),
	)

	return app, nil
}

// GetApp returns app info by ID.
func (m *MemoryTokenManager) GetApp(ctx context.Context, appID string) (*AppInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	app, ok := m.apps[appID]
	if !ok {
		return nil, errors.New("app not found")
	}

	return app, nil
}

// GetAppByToken returns app info by token.
func (m *MemoryTokenManager) GetAppByToken(ctx context.Context, token string) (*AppInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	appID, ok := m.tokens[token]
	if !ok {
		return nil, errors.New("invalid token")
	}

	app, ok := m.apps[appID]
	if !ok {
		return nil, errors.New("app not found")
	}

	return app, nil
}

// UpdateApp updates an existing app.
func (m *MemoryTokenManager) UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error) {
	if req == nil {
		return nil, errors.New("update request is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[appID]
	if !ok {
		return nil, errors.New("app not found")
	}

	// Check for duplicate name if changing
	if req.Name != "" && req.Name != app.Name {
		for _, other := range m.apps {
			if other.ID != appID && other.Name == req.Name {
				return nil, errors.New("app name already exists")
			}
		}
		app.Name = req.Name
	}

	if req.Description != "" {
		app.Description = req.Description
	}

	if req.Metadata != nil {
		if app.Metadata == nil {
			app.Metadata = make(map[string]string)
		}
		for k, v := range req.Metadata {
			app.Metadata[k] = v
		}
	}

	if req.Status != "" {
		if req.Status != "active" && req.Status != "disabled" {
			return nil, errors.New("invalid status, must be 'active' or 'disabled'")
		}
		app.Status = req.Status
	}

	app.UpdatedAt = time.Now()

	m.logger.Info("App updated",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return app, nil
}

// DeleteApp deletes an app and invalidates its token.
func (m *MemoryTokenManager) DeleteApp(ctx context.Context, appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[appID]
	if !ok {
		return errors.New("app not found")
	}

	// Remove token mapping
	delete(m.tokens, app.Token)

	// Remove app
	delete(m.apps, appID)

	m.logger.Info("App deleted",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return nil
}

// ListApps returns all apps.
func (m *MemoryTokenManager) ListApps(ctx context.Context) ([]*AppInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	apps := make([]*AppInfo, 0, len(m.apps))
	for _, app := range m.apps {
		apps = append(apps, app)
	}

	return apps, nil
}

// ValidateToken validates a token and returns the associated app info.
func (m *MemoryTokenManager) ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if token == "" {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "token is empty",
		}, nil
	}

	appID, ok := m.tokens[token]
	if !ok {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "token not found",
		}, nil
	}

	app, ok := m.apps[appID]
	if !ok {
		return &TokenValidationResult{
			Valid:  false,
			Reason: "app not found",
		}, nil
	}

	if app.Status != "active" {
		return &TokenValidationResult{
			Valid:  false,
			AppID:  app.ID,
			Reason: "app is disabled",
		}, nil
	}

	return &TokenValidationResult{
		Valid:   true,
		AppID:   app.ID,
		AppName: app.Name,
	}, nil
}

// RegenerateToken generates a new token for an app.
func (m *MemoryTokenManager) RegenerateToken(ctx context.Context, appID string) (*AppInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[appID]
	if !ok {
		return nil, errors.New("app not found")
	}

	// Remove old token
	delete(m.tokens, app.Token)

	// Generate new token
	newToken, err := GenerateToken(0)
	if err != nil {
		return nil, err
	}

	app.Token = newToken
	app.UpdatedAt = time.Now()

	// Add new token mapping
	m.tokens[newToken] = appID

	m.logger.Info("Token regenerated",
		zap.String("id", appID),
		zap.String("name", app.Name),
	)

	return app, nil
}
