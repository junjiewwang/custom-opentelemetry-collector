// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════
// Resolver Chain Tests
// ═══════════════════════════════════════════════════

func TestResolver_PlatformDefault_WhenNoAppID(t *testing.T) {
	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace:  30 * 24 * time.Hour,
		MaxMetric: 90 * 24 * time.Hour,
		MaxLog:    30 * 24 * time.Hour,
	}

	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	tests := []struct {
		signal   SignalType
		expected time.Duration
		source   RetentionSource
	}{
		{SignalTrace, 7 * 24 * time.Hour, SourcePlatformDefault},
		{SignalMetric, 30 * 24 * time.Hour, SourcePlatformDefault},
		{SignalLog, 14 * 24 * time.Hour, SourcePlatformDefault},
	}

	for _, tt := range tests {
		t.Run(string(tt.signal), func(t *testing.T) {
			eff, err := resolver.Resolve(ctx, tt.signal, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eff.Duration != tt.expected {
				t.Errorf("expected duration %v, got %v", tt.expected, eff.Duration)
			}
			if eff.Source != tt.source {
				t.Errorf("expected source %s, got %s", tt.source, eff.Source)
			}
			if eff.Clamped {
				t.Error("should not be clamped")
			}
		})
	}
}

func TestResolver_AppOverride_TakesPrecedence(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	// Set per-app override: 3 days for traces
	appRetention := 3 * 24 * time.Hour
	_ = store.SetForApp(ctx, "app-001", SignalTrace, appRetention)

	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace:  30 * 24 * time.Hour,
		MaxMetric: 90 * 24 * time.Hour,
		MaxLog:    30 * 24 * time.Hour,
	}

	resolver := NewRetentionResolver(store, defaults, limits)

	eff, err := resolver.Resolve(ctx, SignalTrace, "app-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != appRetention {
		t.Errorf("expected app override %v, got %v", appRetention, eff.Duration)
	}
	if eff.Source != SourceAppOverride {
		t.Errorf("expected source %s, got %s", SourceAppOverride, eff.Source)
	}
}

func TestResolver_AppOverrideNotFound_FallsToPlatformDefault(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	// No override for app-002
	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace:  30 * 24 * time.Hour,
		MaxMetric: 90 * 24 * time.Hour,
		MaxLog:    30 * 24 * time.Hour,
	}

	resolver := NewRetentionResolver(store, defaults, limits)

	eff, err := resolver.Resolve(ctx, SignalTrace, "app-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 7*24*time.Hour {
		t.Errorf("expected platform default 7d, got %v", eff.Duration)
	}
	if eff.Source != SourcePlatformDefault {
		t.Errorf("expected source %s, got %s", SourcePlatformDefault, eff.Source)
	}
}

func TestResolver_BuiltinFallback_WhenNoPlatformDefault(t *testing.T) {
	// Platform defaults all set to zero → fall through to builtin
	defaults := RetentionDefaults{
		Trace:  0,
		Metric: 0,
		Log:    0,
	}
	limits := RetentionLimits{}

	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	eff, err := resolver.Resolve(ctx, SignalTrace, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != BuiltinDefaults.Trace {
		t.Errorf("expected builtin trace %v, got %v", BuiltinDefaults.Trace, eff.Duration)
	}
	if eff.Source != SourceBuiltinDefault {
		t.Errorf("expected source %s, got %s", SourceBuiltinDefault, eff.Source)
	}
}

func TestResolver_Clamping_ExceedsMax(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	// Set per-app override: 60 days for traces (exceeds max of 30 days)
	longRetention := 60 * 24 * time.Hour
	_ = store.SetForApp(ctx, "greedy-app", SignalTrace, longRetention)

	defaults := RetentionDefaults{
		Trace: 7 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace: 30 * 24 * time.Hour, // hard cap: 30 days
	}

	resolver := NewRetentionResolver(store, defaults, limits)

	eff, err := resolver.Resolve(ctx, SignalTrace, "greedy-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 30*24*time.Hour {
		t.Errorf("expected clamped to 30d, got %v", eff.Duration)
	}
	if !eff.Clamped {
		t.Error("expected Clamped to be true")
	}
	if eff.MaxAllowed != 30*24*time.Hour {
		t.Errorf("expected MaxAllowed 30d, got %v", eff.MaxAllowed)
	}
	if eff.Source != SourceAppOverride {
		t.Errorf("expected source %s, got %s", SourceAppOverride, eff.Source)
	}
}

func TestResolver_NoClamping_WhenBelowMax(t *testing.T) {
	store := NewInMemoryRetentionStore()
	ctx := context.Background()

	// Set per-app override: 10 days (below max of 30 days)
	_ = store.SetForApp(ctx, "normal-app", SignalTrace, 10*24*time.Hour)

	defaults := RetentionDefaults{Trace: 7 * 24 * time.Hour}
	limits := RetentionLimits{MaxTrace: 30 * 24 * time.Hour}

	resolver := NewRetentionResolver(store, defaults, limits)

	eff, err := resolver.Resolve(ctx, SignalTrace, "normal-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 10*24*time.Hour {
		t.Errorf("expected 10d, got %v", eff.Duration)
	}
	if eff.Clamped {
		t.Error("expected Clamped to be false")
	}
}

func TestResolver_NoClamping_WhenNoLimitsSet(t *testing.T) {
	// When MaxTrace=0, no clamping is applied
	defaults := RetentionDefaults{Trace: 365 * 24 * time.Hour} // 1 year
	limits := RetentionLimits{MaxTrace: 0}                     // no limit

	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	eff, err := resolver.Resolve(ctx, SignalTrace, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 365*24*time.Hour {
		t.Errorf("expected 365d, got %v", eff.Duration)
	}
	if eff.Clamped {
		t.Error("expected Clamped to be false with no limit")
	}
}

func TestResolver_StoreError_FallsToPlatformDefault(t *testing.T) {
	store := &errorRetentionStore{err: errors.New("db connection failed")}

	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace:  30 * 24 * time.Hour,
		MaxMetric: 90 * 24 * time.Hour,
		MaxLog:    30 * 24 * time.Hour,
	}

	resolver := NewRetentionResolver(store, defaults, limits)
	ctx := context.Background()

	// When store errors, resolver should fall through to platform default (not fail)
	eff, err := resolver.Resolve(ctx, SignalTrace, "app-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 7*24*time.Hour {
		t.Errorf("expected platform default 7d, got %v", eff.Duration)
	}
	if eff.Source != SourcePlatformDefault {
		t.Errorf("expected source %s, got %s", SourcePlatformDefault, eff.Source)
	}
}

func TestResolver_ResolveAll(t *testing.T) {
	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{
		MaxTrace:  30 * 24 * time.Hour,
		MaxMetric: 90 * 24 * time.Hour,
		MaxLog:    30 * 24 * time.Hour,
	}

	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	result, err := resolver.ResolveAll(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}

	if result[SignalTrace].Duration != 7*24*time.Hour {
		t.Errorf("trace: expected 7d, got %v", result[SignalTrace].Duration)
	}
	if result[SignalMetric].Duration != 30*24*time.Hour {
		t.Errorf("metric: expected 30d, got %v", result[SignalMetric].Duration)
	}
	if result[SignalLog].Duration != 14*24*time.Hour {
		t.Errorf("log: expected 14d, got %v", result[SignalLog].Duration)
	}
}

func TestResolver_NilStore_UsesDefaults(t *testing.T) {
	defaults := RetentionDefaults{
		Trace:  7 * 24 * time.Hour,
		Metric: 30 * 24 * time.Hour,
		Log:    14 * 24 * time.Hour,
	}
	limits := RetentionLimits{}

	// nil store — platform-only mode
	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	// Even with appID, should use platform default (store is nil)
	eff, err := resolver.Resolve(ctx, SignalTrace, "some-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eff.Duration != 7*24*time.Hour {
		t.Errorf("expected platform default 7d, got %v", eff.Duration)
	}
	if eff.Source != SourcePlatformDefault {
		t.Errorf("expected source %s, got %s", SourcePlatformDefault, eff.Source)
	}
}

func TestResolver_UnknownSignal_UsesBuiltinFallback(t *testing.T) {
	defaults := RetentionDefaults{} // all zero
	limits := RetentionLimits{}

	resolver := NewRetentionResolver(nil, defaults, limits)
	ctx := context.Background()

	eff, err := resolver.Resolve(ctx, SignalType("unknown"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// builtinDefault for unknown returns 7d safe fallback
	if eff.Duration != 7*24*time.Hour {
		t.Errorf("expected safe fallback 7d, got %v", eff.Duration)
	}
	if eff.Source != SourceBuiltinDefault {
		t.Errorf("expected source %s, got %s", SourceBuiltinDefault, eff.Source)
	}
}

// ═══════════════════════════════════════════════════
// builtinDefault Tests
// ═══════════════════════════════════════════════════

func TestBuiltinDefaults(t *testing.T) {
	tests := []struct {
		signal   SignalType
		expected time.Duration
	}{
		{SignalTrace, 7 * 24 * time.Hour},
		{SignalMetric, 30 * 24 * time.Hour},
		{SignalLog, 14 * 24 * time.Hour},
		{SignalType("other"), 7 * 24 * time.Hour}, // safe fallback
	}

	for _, tt := range tests {
		t.Run(string(tt.signal), func(t *testing.T) {
			got := builtinDefault(tt.signal)
			if got != tt.expected {
				t.Errorf("builtinDefault(%s) = %v, want %v", tt.signal, got, tt.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════

// errorRetentionStore always returns an error on GetForApp.
type errorRetentionStore struct {
	err error
}

func (s *errorRetentionStore) GetForApp(_ context.Context, _ string, _ SignalType) (*time.Duration, error) {
	return nil, s.err
}

func (s *errorRetentionStore) SetForApp(_ context.Context, _ string, _ SignalType, _ time.Duration) error {
	return s.err
}

func (s *errorRetentionStore) DeleteForApp(_ context.Context, _ string, _ SignalType) error {
	return s.err
}

func (s *errorRetentionStore) ListAppOverrides(_ context.Context) ([]AppRetentionEntry, error) {
	return nil, s.err
}
