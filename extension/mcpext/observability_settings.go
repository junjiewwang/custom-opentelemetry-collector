// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// observabilitySettings is the effective per-call set of query limits. The
// deployment config is the default; an operator-managed tenant override may
// replace individual values (including enlarging them).
type observabilitySettings struct {
	QueryTimeout     time.Duration
	MaxTimeRange     time.Duration
	MaxTimeRangeText string
	Limits           ObservabilityLimits
}

type observabilitySettingsKey struct{}

func withObservabilitySettings(ctx context.Context, settings observabilitySettings) context.Context {
	return context.WithValue(ctx, observabilitySettingsKey{}, settings)
}

func observabilitySettingsFromContext(ctx context.Context) (observabilitySettings, bool) {
	settings, ok := ctx.Value(observabilitySettingsKey{}).(observabilitySettings)
	return settings, ok
}

func deploymentObservabilitySettings(cfg ObservabilityConfig) observabilitySettings {
	return observabilitySettings{
		QueryTimeout:     cfg.queryTimeout(),
		MaxTimeRange:     cfg.maxTimeRange(),
		MaxTimeRangeText: cfg.MaxTimeRange,
		Limits:           cfg.Limits,
	}
}

// resolveObservabilitySettings resolves limits for the *caller* tenant, not
// the tenant owning the queried data. Global keys and impersonating super keys
// keep deployment defaults. A missing/unreadable tenant falls back to the
// deployment defaults: these are cost controls, not an authorization gate.
func (w *mcpServerWrapper) resolveObservabilitySettings(ctx context.Context) observabilitySettings {
	base := deploymentObservabilitySettings(w.ext.config.Observability)
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok || identity.TenantID == "" || identity.Impersonating || w.ext.tenantManager == nil {
		return base
	}

	logger := w.logger
	if logger == nil {
		logger = zap.NewNop()
	}
	tenant, err := w.ext.tenantManager.GetTenant(ctx, identity.TenantID)
	if err != nil || tenant == nil {
		logger.Warn("tenant observability query settings unavailable, using deployment defaults",
			zap.String("tenant_id", identity.TenantID), zap.Error(err))
		return base
	}
	if tenant.Capabilities == nil || tenant.Capabilities.ObservabilityQuery == nil {
		return base
	}
	override := tenant.Capabilities.ObservabilityQuery

	// Values are allowed to exceed the deployment defaults (operator-managed,
	// risk accepted). Invalid stored values are ignored rather than failing
	// the query; the operator API validates them at save time.
	if override.QueryTimeout != nil && *override.QueryTimeout > 0 {
		base.QueryTimeout = time.Duration(*override.QueryTimeout) * time.Second
	}
	if override.MaxTimeRange != nil {
		if parsed, err := time.ParseDuration(*override.MaxTimeRange); err == nil && parsed > 0 {
			base.MaxTimeRange = parsed
			base.MaxTimeRangeText = *override.MaxTimeRange
		} else {
			logger.Warn("invalid tenant max_time_range, using deployment default",
				zap.String("tenant_id", identity.TenantID),
				zap.String("max_time_range", *override.MaxTimeRange))
		}
	}
	if override.MetricSeries != nil && *override.MetricSeries > 0 {
		base.Limits.MetricSeries = *override.MetricSeries
	}
	if override.Traces != nil && *override.Traces > 0 {
		base.Limits.Traces = *override.Traces
	}
	if override.Logs != nil && *override.Logs > 0 {
		base.Limits.Logs = *override.Logs
	}
	if override.LogContextLines != nil && *override.LogContextLines > 0 {
		base.Limits.LogContextLines = *override.LogContextLines
	}
	return base
}

// observabilitySettings returns the per-call settings injected by the tool
// guard, or the deployment defaults when called outside a tool invocation.
func (w *mcpServerWrapper) observabilitySettings(ctx context.Context) observabilitySettings {
	if settings, ok := observabilitySettingsFromContext(ctx); ok {
		return settings
	}
	return deploymentObservabilitySettings(w.ext.config.Observability)
}
