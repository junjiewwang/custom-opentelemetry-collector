// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"time"
)

// BuiltinDefaults are the hardcoded fallback retention durations
// used when neither per-app nor platform-level config provides a value.
var BuiltinDefaults = RetentionDefaults{
	Trace:  7 * 24 * time.Hour,  // 7 days
	Metric: 30 * 24 * time.Hour, // 30 days
	Log:    14 * 24 * time.Hour, // 14 days
}

// retentionResolverChain implements RetentionResolver using the
// Chain-of-Responsibility pattern:
//
//	Per-App Override → Platform Default → Builtin Fallback
//
// Each level is checked in order; the first non-nil result is used.
// All results are clamped to platform-level maximums.
type retentionResolverChain struct {
	store    RetentionStore  // per-app override store (may be nil)
	defaults RetentionDefaults
	limits   RetentionLimits
}

// NewRetentionResolver creates a resolver chain with the given configuration.
// Parameters:
//   - store: provides per-app retention overrides (may be nil for platform-only mode)
//   - defaults: platform-level default retention per signal
//   - limits: maximum allowed retention per signal (hard cap)
func NewRetentionResolver(store RetentionStore, defaults RetentionDefaults, limits RetentionLimits) RetentionResolver {
	return &retentionResolverChain{
		store:    store,
		defaults: defaults,
		limits:   limits,
	}
}

// Resolve returns the effective retention for the given signal and optional appID.
func (r *retentionResolverChain) Resolve(ctx context.Context, signal SignalType, appID string) (EffectiveRetention, error) {
	// Level 1: Per-app override
	if appID != "" && r.store != nil {
		override, err := r.store.GetForApp(ctx, appID, signal)
		if err == nil && override != nil && *override > 0 {
			return r.clamp(signal, *override, SourceAppOverride), nil
		}
		// If error or nil, fall through to defaults
	}

	// Level 2: Platform default
	dur := r.platformDefault(signal)
	if dur > 0 {
		return r.clamp(signal, dur, SourcePlatformDefault), nil
	}

	// Level 3: Builtin fallback
	return r.clamp(signal, builtinDefault(signal), SourceBuiltinDefault), nil
}

// ListAppOverrides returns all apps that have custom retention settings.
func (r *retentionResolverChain) ListAppOverrides(ctx context.Context) ([]AppRetentionEntry, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.ListAppOverrides(ctx)
}

// ResolveAll returns retention for all signal types.
func (r *retentionResolverChain) ResolveAll(ctx context.Context, appID string) (map[SignalType]EffectiveRetention, error) {
	result := make(map[SignalType]EffectiveRetention, 3)
	for _, signal := range AllSignals() {
		eff, err := r.Resolve(ctx, signal, appID)
		if err != nil {
			return nil, err
		}
		result[signal] = eff
	}
	return result, nil
}

// clamp ensures the requested duration does not exceed the platform maximum.
func (r *retentionResolverChain) clamp(signal SignalType, dur time.Duration, source RetentionSource) EffectiveRetention {
	max := r.platformMax(signal)
	clamped := max > 0 && dur > max
	if clamped {
		dur = max
	}
	return EffectiveRetention{
		Duration:   dur,
		Source:     source,
		MaxAllowed: max,
		Clamped:    clamped,
	}
}

// platformDefault returns the platform-level default retention for a signal.
func (r *retentionResolverChain) platformDefault(signal SignalType) time.Duration {
	switch signal {
	case SignalTrace:
		return r.defaults.Trace
	case SignalMetric:
		return r.defaults.Metric
	case SignalLog:
		return r.defaults.Log
	default:
		return 0
	}
}

// platformMax returns the platform-level maximum retention for a signal.
func (r *retentionResolverChain) platformMax(signal SignalType) time.Duration {
	switch signal {
	case SignalTrace:
		return r.limits.MaxTrace
	case SignalMetric:
		return r.limits.MaxMetric
	case SignalLog:
		return r.limits.MaxLog
	default:
		return 0
	}
}

// builtinDefault returns the hardcoded fallback retention.
func builtinDefault(signal SignalType) time.Duration {
	switch signal {
	case SignalTrace:
		return BuiltinDefaults.Trace
	case SignalMetric:
		return BuiltinDefaults.Metric
	case SignalLog:
		return BuiltinDefaults.Log
	default:
		return 7 * 24 * time.Hour // safe fallback: 7 days
	}
}
