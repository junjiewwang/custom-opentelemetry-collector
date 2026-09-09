// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
)

// fakeAppLister is a deterministic AppLister double for tests.
type fakeAppLister struct {
	apps []*appmanager.AppInfo
}

func (f fakeAppLister) ListApps(context.Context) ([]*appmanager.AppInfo, error) {
	return f.apps, nil
}

func newTestService(apps ...*appmanager.AppInfo) *TenantService {
	return NewTenantService(
		NewMemoryTenantRepository(),
		FixedIDGenerator("tn-1"),
		fakeAppLister{apps: apps},
		nil,
	)
}

func TestCreateTenant(t *testing.T) {
	svc := newTestService()
	tenant, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)
	assert.Equal(t, "tn-1", tenant.ID)
	assert.Equal(t, "acme", tenant.Name)
	assert.Equal(t, StatusActive, tenant.Status)

	got, err := svc.GetTenant(context.Background(), "tn-1")
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Name)
}

func TestCreateTenant_DuplicateName(t *testing.T) {
	svc := newTestService()
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)

	_, err = svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	assert.ErrorIs(t, err, ErrTenantNameExists)
}

func TestCreateTenant_EmptyName(t *testing.T) {
	svc := newTestService()
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: ""})
	require.Error(t, err)
}

func TestUpdateTenant(t *testing.T) {
	svc := newTestService()
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)
	require.NoError(t, svc.EnsureDefaultTenant(context.Background()))

	// Rename.
	updated, err := svc.UpdateTenant(context.Background(), "tn-1", &UpdateTenantRequest{Name: "acme-corp"})
	require.NoError(t, err)
	assert.Equal(t, "acme-corp", updated.Name)

	// Status change (name unchanged).
	updated, err = svc.UpdateTenant(context.Background(), "tn-1", &UpdateTenantRequest{Status: StatusDisabled})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, updated.Status)
	assert.Equal(t, "acme-corp", updated.Name, "name must persist when status-only update")

	// Rename to an existing name is rejected.
	_, err = svc.UpdateTenant(context.Background(), "tn-1", &UpdateTenantRequest{Name: "admin"})
	assert.ErrorIs(t, err, ErrTenantNameExists)
}

func TestUpdateTenant_InvalidStatus(t *testing.T) {
	svc := newTestService()
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)

	_, err = svc.UpdateTenant(context.Background(), "tn-1", &UpdateTenantRequest{Status: "bogus"})
	require.Error(t, err)
}

func TestDeleteTenant_Guards(t *testing.T) {
	// Non-empty tenant: delete is rejected.
	svc := newTestService(&appmanager.AppInfo{ID: "app-a", TenantID: "tn-1"})
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "acme"})
	require.NoError(t, err)
	err = svc.DeleteTenant(context.Background(), "tn-1")
	assert.ErrorIs(t, err, ErrTenantNotEmpty)

	// Default tenant is immutable.
	err = svc.DeleteTenant(context.Background(), DefaultTenantID)
	assert.ErrorIs(t, err, ErrDefaultTenantImmutable)
}

func TestDeleteTenant_EmptyTenantSucceeds(t *testing.T) {
	svc := newTestService() // no apps
	_, err := svc.CreateTenant(context.Background(), &CreateTenantRequest{Name: "solo"})
	require.NoError(t, err)

	require.NoError(t, svc.DeleteTenant(context.Background(), "tn-1"))
	_, err = svc.GetTenant(context.Background(), "tn-1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestListTenantApps(t *testing.T) {
	svc := newTestService(
		&appmanager.AppInfo{ID: "app-a", TenantID: "tn-1"},
		&appmanager.AppInfo{ID: "app-b", TenantID: "tn-1"},
		&appmanager.AppInfo{ID: "app-c", TenantID: "other"},
		&appmanager.AppInfo{ID: "app-d"}, // empty → default tenant
	)

	ids, err := svc.ListTenantApps(context.Background(), "tn-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app-a", "app-b"}, ids)

	// Empty TenantID is treated as the default tenant.
	ids, err = svc.ListTenantApps(context.Background(), DefaultTenantID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app-d"}, ids)
}

func TestEnsureDefaultTenant_Idempotent(t *testing.T) {
	svc := newTestService()
	require.NoError(t, svc.EnsureDefaultTenant(context.Background()))
	require.NoError(t, svc.EnsureDefaultTenant(context.Background())) // no error on second call

	tenant, err := svc.GetTenant(context.Background(), DefaultTenantID)
	require.NoError(t, err)
	assert.Equal(t, "admin", tenant.Name)
}
