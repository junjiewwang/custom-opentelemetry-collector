// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

// mockRetentionProvider implements appmanager.AppRetentionProvider.
type mockRetentionProvider struct {
	policies map[string]appmanager.RetentionPolicy
	err      error
}

func (m *mockRetentionProvider) GetRetention(_ context.Context, appID string) (appmanager.RetentionPolicy, error) {
	if m.err != nil {
		return appmanager.RetentionPolicy{}, m.err
	}
	if p, ok := m.policies[appID]; ok {
		return p, nil
	}
	return appmanager.RetentionPolicy{}, nil
}
func (m *mockRetentionProvider) SetRetention(_ context.Context, _ string, _ appmanager.SignalType, _ time.Duration) error {
	return nil
}
func (m *mockRetentionProvider) DeleteRetention(_ context.Context, _ string, _ appmanager.SignalType) error {
	return nil
}

// mockAppManager implements appmanager.AppManager for testing.
type mockAppManager struct {
	apps []*appmanager.AppInfo
	err  error
}

func (m *mockAppManager) CreateApp(_ context.Context, _ *appmanager.CreateAppRequest) (*appmanager.AppInfo, error) {
	return nil, nil
}
func (m *mockAppManager) GetApp(_ context.Context, _ string) (*appmanager.AppInfo, error) {
	return nil, nil
}
func (m *mockAppManager) UpdateApp(_ context.Context, _ string, _ *appmanager.UpdateAppRequest) (*appmanager.AppInfo, error) {
	return nil, nil
}
func (m *mockAppManager) DeleteApp(_ context.Context, _ string) error { return nil }
func (m *mockAppManager) ListApps(_ context.Context) ([]*appmanager.AppInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.apps, nil
}
func (m *mockAppManager) RegenerateToken(_ context.Context, _ string) (*appmanager.AppInfo, error) {
	return nil, nil
}
func (m *mockAppManager) SetToken(_ context.Context, _ string, _ *appmanager.SetTokenRequest) (*appmanager.AppInfo, error) {
	return nil, nil
}

func TestAppRetentionStoreAdapter_NoProvider(t *testing.T) {
	adapter := newAppRetentionStoreAdapter()
	ctx := context.Background()

	d, err := adapter.GetForApp(ctx, "app-1", lifecycle.SignalTrace)
	require.NoError(t, err)
	assert.Nil(t, d)

	entries, err := adapter.ListAppOverrides(ctx)
	require.NoError(t, err)
	assert.Nil(t, entries)
}

func TestAppRetentionStoreAdapter_GetForApp(t *testing.T) {
	adapter := newAppRetentionStoreAdapter()
	ctx := context.Background()

	retMock := &mockRetentionProvider{
		policies: map[string]appmanager.RetentionPolicy{
			"app-1": {Trace: 720 * time.Hour, Metric: 168 * time.Hour},
		},
	}
	adapter.SetProviders(retMock, nil)

	t.Run("trace_override", func(t *testing.T) {
		d, err := adapter.GetForApp(ctx, "app-1", lifecycle.SignalTrace)
		require.NoError(t, err)
		require.NotNil(t, d)
		assert.Equal(t, 720*time.Hour, *d)
	})

	t.Run("log_default", func(t *testing.T) {
		d, err := adapter.GetForApp(ctx, "app-1", lifecycle.SignalLog)
		require.NoError(t, err)
		assert.Nil(t, d)
	})
}

func TestAppRetentionStoreAdapter_ListAppOverrides(t *testing.T) {
	adapter := newAppRetentionStoreAdapter()
	ctx := context.Background()

	retMock := &mockRetentionProvider{
		policies: map[string]appmanager.RetentionPolicy{
			"app-001": {Trace: 24 * time.Hour},
			"app-002": {Trace: 72 * time.Hour, Metric: 168 * time.Hour},
			"app-003": {}, // zero retention → should be filtered out
		},
	}
	appsMock := &mockAppManager{
		apps: []*appmanager.AppInfo{
			{ID: "app-001", Name: "one"},
			{ID: "app-002", Name: "two"},
			{ID: "app-003", Name: "three"},
		},
	}
	adapter.SetProviders(retMock, appsMock)

	entries, err := adapter.ListAppOverrides(ctx)
	require.NoError(t, err)
	assert.Len(t, entries, 2) // app-003 filtered out (zero retention)

	appIDs := make(map[string]bool)
	for _, e := range entries {
		appIDs[e.AppID] = true
	}
	assert.True(t, appIDs["app-001"])
	assert.True(t, appIDs["app-002"])
	assert.False(t, appIDs["app-003"])
}

func TestAppRetentionStoreAdapter_ListAndWriteAreNoop(t *testing.T) {
	adapter := newAppRetentionStoreAdapter()

	err := adapter.SetForApp(context.Background(), "app-1", lifecycle.SignalTrace, 24*time.Hour)
	assert.NoError(t, err)

	err = adapter.DeleteForApp(context.Background(), "app-1", lifecycle.SignalTrace)
	assert.NoError(t, err)
}
