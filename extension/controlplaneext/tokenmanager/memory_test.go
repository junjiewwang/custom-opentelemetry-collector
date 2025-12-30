// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestMemoryTokenManager_CreateApp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	app, err := mgr.CreateApp(ctx, &CreateAppRequest{
		Name:        "test-app",
		Description: "Test application",
		Metadata:    map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, app.ID)
	assert.NotEmpty(t, app.Token)
	assert.Equal(t, "test-app", app.Name)
	assert.Equal(t, "Test application", app.Description)
	assert.Equal(t, "active", app.Status)
	assert.Equal(t, "test", app.Metadata["env"])

	// Create duplicate name should fail
	_, err = mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	assert.Error(t, err)

	// Create with empty name should fail
	_, err = mgr.CreateApp(ctx, &CreateAppRequest{})
	assert.Error(t, err)
}

func TestMemoryTokenManager_GetApp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	created, err := mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	require.NoError(t, err)

	// Get by ID
	app, err := mgr.GetApp(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, app.ID)
	assert.Equal(t, created.Token, app.Token)

	// Get by token
	app, err = mgr.GetAppByToken(ctx, created.Token)
	require.NoError(t, err)
	assert.Equal(t, created.ID, app.ID)

	// Get non-existent
	_, err = mgr.GetApp(ctx, "non-existent")
	assert.Error(t, err)

	_, err = mgr.GetAppByToken(ctx, "invalid-token")
	assert.Error(t, err)
}

func TestMemoryTokenManager_UpdateApp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	created, err := mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	require.NoError(t, err)

	// Update name
	updated, err := mgr.UpdateApp(ctx, created.ID, &UpdateAppRequest{
		Name:        "updated-app",
		Description: "Updated description",
	})
	require.NoError(t, err)
	assert.Equal(t, "updated-app", updated.Name)
	assert.Equal(t, "Updated description", updated.Description)

	// Update status
	updated, err = mgr.UpdateApp(ctx, created.ID, &UpdateAppRequest{Status: "disabled"})
	require.NoError(t, err)
	assert.Equal(t, "disabled", updated.Status)

	// Invalid status
	_, err = mgr.UpdateApp(ctx, created.ID, &UpdateAppRequest{Status: "invalid"})
	assert.Error(t, err)

	// Update non-existent
	_, err = mgr.UpdateApp(ctx, "non-existent", &UpdateAppRequest{Name: "test"})
	assert.Error(t, err)
}

func TestMemoryTokenManager_DeleteApp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	created, err := mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	require.NoError(t, err)

	// Delete
	err = mgr.DeleteApp(ctx, created.ID)
	require.NoError(t, err)

	// Verify deleted
	_, err = mgr.GetApp(ctx, created.ID)
	assert.Error(t, err)

	// Token should be invalid
	_, err = mgr.GetAppByToken(ctx, created.Token)
	assert.Error(t, err)

	// Delete non-existent
	err = mgr.DeleteApp(ctx, "non-existent")
	assert.Error(t, err)
}

func TestMemoryTokenManager_ListApps(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Initially empty
	apps, err := mgr.ListApps(ctx)
	require.NoError(t, err)
	assert.Empty(t, apps)

	// Create apps
	_, err = mgr.CreateApp(ctx, &CreateAppRequest{Name: "app-1"})
	require.NoError(t, err)
	_, err = mgr.CreateApp(ctx, &CreateAppRequest{Name: "app-2"})
	require.NoError(t, err)

	// List
	apps, err = mgr.ListApps(ctx)
	require.NoError(t, err)
	assert.Len(t, apps, 2)
}

func TestMemoryTokenManager_ValidateToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	created, err := mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	require.NoError(t, err)

	// Valid token
	result, err := mgr.ValidateToken(ctx, created.Token)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, created.ID, result.AppID)
	assert.Equal(t, "test-app", result.AppName)

	// Invalid token
	result, err = mgr.ValidateToken(ctx, "invalid-token")
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "token not found", result.Reason)

	// Empty token
	result, err = mgr.ValidateToken(ctx, "")
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "token is empty", result.Reason)

	// Disabled app
	_, err = mgr.UpdateApp(ctx, created.ID, &UpdateAppRequest{Status: "disabled"})
	require.NoError(t, err)

	result, err = mgr.ValidateToken(ctx, created.Token)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "app is disabled", result.Reason)
}

func TestMemoryTokenManager_RegenerateToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewMemoryTokenManager(logger, DefaultConfig())

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Create app
	created, err := mgr.CreateApp(ctx, &CreateAppRequest{Name: "test-app"})
	require.NoError(t, err)
	oldToken := created.Token

	// Regenerate
	updated, err := mgr.RegenerateToken(ctx, created.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldToken, updated.Token)

	// Old token should be invalid
	result, err := mgr.ValidateToken(ctx, oldToken)
	require.NoError(t, err)
	assert.False(t, result.Valid)

	// New token should be valid
	result, err = mgr.ValidateToken(ctx, updated.Token)
	require.NoError(t, err)
	assert.True(t, result.Valid)

	// Regenerate non-existent
	_, err = mgr.RegenerateToken(ctx, "non-existent")
	assert.Error(t, err)
}
