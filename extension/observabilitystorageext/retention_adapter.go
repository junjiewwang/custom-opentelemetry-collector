// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

// appRetentionStoreAdapter bridges AppRetentionProvider + AppManager (admin-facing, Redis-persisted)
// into lifecycle.RetentionStore (purger-facing, in-process).
//
// It starts with no provider (all overrides return nil = use platform default).
// When the admin extension calls SetProviders, subsequent purger cycles resolve
// per-app retention from AppInfo.Retention in Redis.
type appRetentionStoreAdapter struct {
	mu        sync.RWMutex
	retention appmanager.AppRetentionProvider
	apps      appmanager.AppManager
}

var _ lifecycle.RetentionStore = (*appRetentionStoreAdapter)(nil)

func newAppRetentionStoreAdapter() *appRetentionStoreAdapter {
	return &appRetentionStoreAdapter{}
}

// SetProviders injects both the retention provider and app lister (from the same AppService instance).
func (a *appRetentionStoreAdapter) SetProviders(retention appmanager.AppRetentionProvider, apps appmanager.AppManager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retention = retention
	a.apps = apps
}

// GetForApp resolves per-app retention for the given signal.
func (a *appRetentionStoreAdapter) GetForApp(ctx context.Context, appID string, signal lifecycle.SignalType) (*time.Duration, error) {
	a.mu.RLock()
	p := a.retention
	a.mu.RUnlock()

	if p == nil {
		return nil, nil
	}
	policy, err := p.GetRetention(ctx, appID)
	if err != nil {
		return nil, err
	}
	d := policyDuration(policy, signal)
	if d <= 0 {
		return nil, nil
	}
	return &d, nil
}

// SetForApp is a no-op: admin writes go through AppService.SetRetention (Redis).
func (a *appRetentionStoreAdapter) SetForApp(_ context.Context, _ string, _ lifecycle.SignalType, _ time.Duration) error {
	return nil
}

// DeleteForApp is a no-op (same reason as SetForApp).
func (a *appRetentionStoreAdapter) DeleteForApp(_ context.Context, _ string, _ lifecycle.SignalType) error {
	return nil
}

// ListAppOverrides iterates all apps via AppManager and returns those with non-zero retention.
func (a *appRetentionStoreAdapter) ListAppOverrides(ctx context.Context) ([]lifecycle.AppRetentionEntry, error) {
	a.mu.RLock()
	apps := a.apps
	retention := a.retention
	a.mu.RUnlock()

	if apps == nil || retention == nil {
		return nil, nil
	}

	allApps, err := apps.ListApps(ctx)
	if err != nil {
		return nil, err
	}

	var entries []lifecycle.AppRetentionEntry
	for _, app := range allApps {
		policy, err := retention.GetRetention(ctx, app.ID)
		if err != nil {
			continue
		}
		if policy.IsZero() {
			continue
		}

		overrides := make(map[lifecycle.SignalType]time.Duration, 3)
		if policy.Trace > 0 {
			overrides[lifecycle.SignalTrace] = policy.Trace
		}
		if policy.Metric > 0 {
			overrides[lifecycle.SignalMetric] = policy.Metric
		}
		if policy.Log > 0 {
			overrides[lifecycle.SignalLog] = policy.Log
		}

		entries = append(entries, lifecycle.AppRetentionEntry{
			AppID:     app.ID,
			Overrides: overrides,
		})
	}
	return entries, nil
}

func policyDuration(p appmanager.RetentionPolicy, signal lifecycle.SignalType) time.Duration {
	switch signal {
	case lifecycle.SignalTrace:
		return p.Trace
	case lifecycle.SignalMetric:
		return p.Metric
	case lifecycle.SignalLog:
		return p.Log
	default:
		return 0
	}
}
