// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tenantmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

type stubTenantManager struct {
	tenantmanager.TenantManager
	tenants map[string]*tenantmanager.Tenant
	err     error
}

func (s *stubTenantManager) GetTenant(_ context.Context, id string) (*tenantmanager.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	tenant, ok := s.tenants[id]
	if !ok {
		return nil, errors.New("tenant not found")
	}
	return tenant, nil
}

func tenantWithQueryLimits(cfg *tenantmanager.TenantObservabilityQueryConfig) *tenantmanager.Tenant {
	return &tenantmanager.Tenant{
		ID:           "tn-a",
		Status:       tenantmanager.StatusActive,
		Capabilities: &tenantmanager.TenantCapabilities{ObservabilityQuery: cfg},
	}
}

func TestResolveObservabilitySettingsInheritance(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	base := deploymentObservabilitySettings(w.ext.config.Observability)
	tenantCtx := queryIdentity("tn-a", metricReadScope)

	// No tenant manager available → deployment defaults.
	got := w.resolveObservabilitySettings(tenantCtx)
	assert.Equal(t, base, got)

	w.ext.tenantManager = &stubTenantManager{tenants: map[string]*tenantmanager.Tenant{
		"tn-a": {ID: "tn-a", Status: tenantmanager.StatusActive},
	}}
	assert.Equal(t, base, w.resolveObservabilitySettings(tenantCtx), "tenant without capability inherits")

	// Global key has no tenant scope; impersonating super keys must not be
	// constrained by the target tenant's limits.
	assert.Equal(t, base, w.resolveObservabilitySettings(queryIdentity("")))
	impersonating := tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType: tenantauth.KeyTypeSuper, TenantID: "tn-a", Impersonating: true,
	})
	assert.Equal(t, base, w.resolveObservabilitySettings(impersonating))
}

func TestResolveObservabilitySettingsOverridesMayExceedDefaults(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	base := deploymentObservabilitySettings(w.ext.config.Observability)
	timeout := 90
	rangeText := "72h"
	metricSeries := 500
	traces := 200
	logs := 800
	contextLines := 300
	w.ext.tenantManager = &stubTenantManager{tenants: map[string]*tenantmanager.Tenant{
		"tn-a": tenantWithQueryLimits(&tenantmanager.TenantObservabilityQueryConfig{
			QueryTimeout:    &timeout,
			MaxTimeRange:    &rangeText,
			MetricSeries:    &metricSeries,
			Traces:          &traces,
			Logs:            &logs,
			LogContextLines: &contextLines,
		}),
	}}

	got := w.resolveObservabilitySettings(queryIdentity("tn-a", metricReadScope))
	assert.Equal(t, 90*time.Second, got.QueryTimeout, "tenant may enlarge query timeout")
	assert.Equal(t, 72*time.Hour, got.MaxTimeRange)
	assert.Equal(t, "72h", got.MaxTimeRangeText)
	assert.Equal(t, 500, got.Limits.MetricSeries)
	assert.Equal(t, 200, got.Limits.Traces)
	assert.Equal(t, 800, got.Limits.Logs)
	assert.Equal(t, 300, got.Limits.LogContextLines)
	assert.Equal(t, 50, base.Limits.MetricSeries, "deployment defaults remain the baseline")
}

func TestResolveObservabilitySettingsPartialOverrideAndFallbacks(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	base := deploymentObservabilitySettings(w.ext.config.Observability)
	traces := 3
	badRange := "tomorrow"
	w.ext.tenantManager = &stubTenantManager{tenants: map[string]*tenantmanager.Tenant{
		"tn-a": tenantWithQueryLimits(&tenantmanager.TenantObservabilityQueryConfig{
			Traces:       &traces,
			MaxTimeRange: &badRange,
		}),
	}}

	got := w.resolveObservabilitySettings(queryIdentity("tn-a", metricReadScope))
	assert.Equal(t, 3, got.Limits.Traces)
	assert.Equal(t, base.Limits.MetricSeries, got.Limits.MetricSeries, "omitted fields inherit")
	assert.Equal(t, base.QueryTimeout, got.QueryTimeout)
	assert.Equal(t, base.MaxTimeRange, got.MaxTimeRange, "invalid duration falls back")
	assert.Equal(t, base.MaxTimeRangeText, got.MaxTimeRangeText)

	w.ext.tenantManager = &stubTenantManager{err: errors.New("backend down")}
	assert.Equal(t, base, w.resolveObservabilitySettings(queryIdentity("tn-a", metricReadScope)),
		"an unavailable tenant store must not break queries")
}

func TestObservabilitySettingsFromContext(t *testing.T) {
	w, _, _, _ := observabilityTestWrapper()
	ctx := withObservabilitySettings(context.Background(), observabilitySettings{
		QueryTimeout: 5 * time.Second,
		Limits:       ObservabilityLimits{Traces: 7},
	})
	got := w.observabilitySettings(ctx)
	assert.Equal(t, 5*time.Second, got.QueryTimeout)
	assert.Equal(t, 7, got.Limits.Traces)

	fallback := w.observabilitySettings(context.Background())
	assert.Equal(t, deploymentObservabilitySettings(w.ext.config.Observability), fallback)
}
