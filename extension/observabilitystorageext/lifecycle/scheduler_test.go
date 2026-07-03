// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Mock implementations for testing
// ═══════════════════════════════════════════════════

// mockPurger records all calls for verification.
type mockPurger struct {
	mu              sync.Mutex
	purgeExpiredCalls []purgeExpiredCall
	purgeByAppCalls   []purgeByAppCall
	estimateCalls     []estimateCall
	boundaryCalls     []SignalType

	// Configurable return values
	purgeResult   *PurgeResult
	purgeErr      error
	estimateResult *PurgeEstimate
	estimateErr   error
	boundary      *DataBoundary
	boundaryErr   error
}

type purgeExpiredCall struct {
	Signal SignalType
	Before time.Time
}

type purgeByAppCall struct {
	AppID  string
	Signal SignalType
	Before time.Time
}

type estimateCall struct {
	Signal SignalType
	Before time.Time
}

func (m *mockPurger) PurgeExpired(_ context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredCalls = append(m.purgeExpiredCalls, purgeExpiredCall{Signal: signal, Before: before})
	return m.purgeResult, m.purgeErr
}

func (m *mockPurger) PurgeByApp(_ context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeByAppCalls = append(m.purgeByAppCalls, purgeByAppCall{AppID: appID, Signal: signal, Before: before})
	return m.purgeResult, m.purgeErr
}

func (m *mockPurger) EstimatePurge(_ context.Context, signal SignalType, before time.Time) (*PurgeEstimate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.estimateCalls = append(m.estimateCalls, estimateCall{Signal: signal, Before: before})
	return m.estimateResult, m.estimateErr
}

func (m *mockPurger) GetDataBoundary(_ context.Context, signal SignalType) (*DataBoundary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.boundaryCalls = append(m.boundaryCalls, signal)
	return m.boundary, m.boundaryErr
}

func (m *mockPurger) getPurgeExpiredCalls() []purgeExpiredCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]purgeExpiredCall, len(m.purgeExpiredCalls))
	copy(result, m.purgeExpiredCalls)
	return result
}

func (m *mockPurger) getEstimateCalls() []estimateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]estimateCall, len(m.estimateCalls))
	copy(result, m.estimateCalls)
	return result
}

// mockResolver returns configurable retention.
type mockResolver struct {
	retention EffectiveRetention
	err       error
}

func (m *mockResolver) Resolve(_ context.Context, _ SignalType, _ string) (EffectiveRetention, error) {
	return m.retention, m.err
}

func (m *mockResolver) ResolveAll(_ context.Context, _ string) (map[SignalType]EffectiveRetention, error) {
	result := make(map[SignalType]EffectiveRetention)
	for _, s := range AllSignals() {
		result[s] = m.retention
	}
	return result, m.err
}

func (m *mockResolver) ListAppOverrides(_ context.Context) ([]AppRetentionEntry, error) {
	return nil, nil
}

// mockUsageReporter returns configurable usage.
type mockUsageReporter struct {
	mu    sync.Mutex
	usage *StorageUsage
	err   error
	calls int
}

func (m *mockUsageReporter) GetUsage(_ context.Context) (*StorageUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.usage, m.err
}

func (m *mockUsageReporter) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockAuditEmitter captures emitted events for assertion.
type mockAuditEmitter struct {
	mu     sync.Mutex
	events []LifecycleEvent
}

func (m *mockAuditEmitter) Emit(_ context.Context, event LifecycleEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockAuditEmitter) getEvents() []LifecycleEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]LifecycleEvent, len(m.events))
	copy(result, m.events)
	return result
}

// ═══════════════════════════════════════════════════
// Tests
// ═══════════════════════════════════════════════════

func TestNewScheduler_DefaultsApplied(t *testing.T) {
	s := NewScheduler()

	if s.config.Interval != time.Hour {
		t.Errorf("expected default interval 1h, got %v", s.config.Interval)
	}
	if s.config.UsageWarningRatio != 0.75 {
		t.Errorf("expected default warning ratio 0.75, got %v", s.config.UsageWarningRatio)
	}
	if s.config.UsageCriticalRatio != 0.90 {
		t.Errorf("expected default critical ratio 0.90, got %v", s.config.UsageCriticalRatio)
	}
	if s.config.TrendBufferSize != 168 {
		t.Errorf("expected default trend buffer size 168, got %v", s.config.TrendBufferSize)
	}
	if s.trendBuf == nil {
		t.Error("expected trend buffer to be initialized")
	}
	if s.logger == nil {
		t.Error("expected logger to be initialized")
	}
}

func TestNewScheduler_WithOptions(t *testing.T) {
	purger := &mockPurger{}
	resolver := &mockResolver{}
	usage := &mockUsageReporter{}
	audit := &mockAuditEmitter{}
	logger := zaptest.NewLogger(t)

	cfg := SchedulerConfig{
		Enabled:            true,
		Interval:           5 * time.Minute,
		DryRun:             true,
		UsageWarningRatio:  0.80,
		UsageCriticalRatio: 0.95,
		TrendBufferSize:    24,
	}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithUsageReporter(usage),
		WithAuditEmitter(audit),
		WithConfig(cfg),
		WithLogger(logger),
	)

	if s.purger != purger {
		t.Error("purger not set correctly")
	}
	if s.resolver != resolver {
		t.Error("resolver not set correctly")
	}
	if s.usage != usage {
		t.Error("usage reporter not set correctly")
	}
	if s.audit != audit {
		t.Error("audit emitter not set correctly")
	}
	if s.config.Interval != 5*time.Minute {
		t.Errorf("expected interval 5m, got %v", s.config.Interval)
	}
	if !s.config.DryRun {
		t.Error("expected dry_run to be true")
	}
	if s.config.TrendBufferSize != 24 {
		t.Errorf("expected trend buffer size 24, got %v", s.config.TrendBufferSize)
	}
}

func TestScheduler_DisabledDoesNotStart(t *testing.T) {
	purger := &mockPurger{}

	s := NewScheduler(
		WithPurger(purger),
		WithConfig(SchedulerConfig{Enabled: false}),
	)

	s.Start(context.Background())
	// Give a brief moment for goroutine to potentially run (it shouldn't)
	time.Sleep(50 * time.Millisecond)
	s.Stop()

	calls := purger.getPurgeExpiredCalls()
	if len(calls) > 0 {
		t.Errorf("expected no purge calls when disabled, got %d", len(calls))
	}
}

func TestScheduler_RunCycle_PurgesExpiredData(t *testing.T) {
	oldestTime := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago
	newestTime := time.Now().Add(-1 * time.Hour)

	purger := &mockPurger{
		boundary: &DataBoundary{
			Signal:   SignalTrace,
			OldestAt: &oldestTime,
			NewestAt: &newestTime,
			IsEmpty:  false,
		},
		purgeResult: &PurgeResult{
			Signal:       SignalTrace,
			DeletedDocs:  1000,
			DeletedUnits: 3,
			Duration:     100 * time.Millisecond,
		},
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration:   7 * 24 * time.Hour, // 7 days
			Source:     SourcePlatformDefault,
			MaxAllowed: 30 * 24 * time.Hour,
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{
			Enabled:  true,
			Interval: time.Hour,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	// Run one cycle directly (not via Start/loop to avoid timing issues)
	s.runCycle(context.Background())

	// Verify purge was called for all 3 signals
	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 purge calls (trace, metric, log), got %d", len(calls))
	}

	// Verify audit events emitted
	events := audit.getEvents()
	if len(events) != 3 {
		t.Fatalf("expected 3 audit events, got %d", len(events))
	}
	for _, e := range events {
		if e.Action != ActionAutoPurge {
			t.Errorf("expected action %s, got %s", ActionAutoPurge, e.Action)
		}
		if e.Operator != "scheduler" {
			t.Errorf("expected operator 'scheduler', got %s", e.Operator)
		}
	}
}

func TestScheduler_RunCycle_DryRunEstimatesOnly(t *testing.T) {
	oldestTime := time.Now().Add(-10 * 24 * time.Hour)
	newestTime := time.Now().Add(-1 * time.Hour)

	purger := &mockPurger{
		boundary: &DataBoundary{
			Signal:   SignalTrace,
			OldestAt: &oldestTime,
			NewestAt: &newestTime,
			IsEmpty:  false,
		},
		estimateResult: &PurgeEstimate{
			Signal:         SignalTrace,
			EstimatedDocs:  5000,
			EstimatedBytes: 1024 * 1024 * 100,
			AffectedUnits:  []string{"otel-traces-2026.05.20", "otel-traces-2026.05.21"},
		},
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 7 * 24 * time.Hour,
			Source:   SourcePlatformDefault,
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{
			Enabled:  true,
			Interval: time.Hour,
			DryRun:   true,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	// Verify NO actual purge was called
	purgeCalls := purger.getPurgeExpiredCalls()
	if len(purgeCalls) != 0 {
		t.Errorf("expected 0 purge calls in dry-run mode, got %d", len(purgeCalls))
	}

	// Verify estimate was called instead
	estimateCalls := purger.getEstimateCalls()
	if len(estimateCalls) != 3 {
		t.Errorf("expected 3 estimate calls, got %d", len(estimateCalls))
	}

	// Verify audit events are marked as dry_run
	events := audit.getEvents()
	for _, e := range events {
		if e.Action != ActionEstimate {
			t.Errorf("expected action %s, got %s", ActionEstimate, e.Action)
		}
		if !e.DryRun {
			t.Error("expected dry_run to be true in audit event")
		}
	}
}

func TestScheduler_RunCycle_NoDataToPurge(t *testing.T) {
	// Boundary shows data is newer than cutoff (oldest = 3 days ago, retention = 7 days)
	recentTime := time.Now().Add(-3 * 24 * time.Hour)
	newestTime := time.Now().Add(-1 * time.Hour)

	purger := &mockPurger{
		boundary: &DataBoundary{
			Signal:   SignalTrace,
			OldestAt: &recentTime,
			NewestAt: &newestTime,
			IsEmpty:  false,
		},
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 7 * 24 * time.Hour, // 7 days → cutoff = 7 days ago
			Source:   SourcePlatformDefault,
		},
	}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	// Verify purge was NOT called (data is too recent)
	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 purge calls (no expired data), got %d", len(calls))
	}
}

func TestScheduler_RunCycle_EmptyData(t *testing.T) {
	purger := &mockPurger{
		boundary: &DataBoundary{
			Signal:  SignalTrace,
			IsEmpty: true,
		},
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 7 * 24 * time.Hour,
			Source:   SourcePlatformDefault,
		},
	}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 purge calls (empty data), got %d", len(calls))
	}
}

func TestScheduler_RunCycle_ResolverError(t *testing.T) {
	purger := &mockPurger{}
	resolver := &mockResolver{err: errors.New("resolver failed")}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	// No purge should be called when resolver fails
	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 purge calls on resolver error, got %d", len(calls))
	}
}

func TestScheduler_RunCycle_PurgerError(t *testing.T) {
	oldestTime := time.Now().Add(-10 * 24 * time.Hour)
	newestTime := time.Now().Add(-1 * time.Hour)

	purger := &mockPurger{
		boundary: &DataBoundary{
			OldestAt: &oldestTime,
			NewestAt: &newestTime,
			IsEmpty:  false,
		},
		purgeErr: errors.New("purge failed"),
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 7 * 24 * time.Hour,
			Source:   SourcePlatformDefault,
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	// Verify purge was called but error was handled gracefully
	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 3 {
		t.Errorf("expected 3 purge calls (one per signal), got %d", len(calls))
	}

	// Verify error was recorded in audit
	events := audit.getEvents()
	for _, e := range events {
		if e.Error != "purge failed" {
			t.Errorf("expected error 'purge failed' in audit, got %q", e.Error)
		}
	}
}

func TestScheduler_RunCycle_BoundaryError_StillPurges(t *testing.T) {
	purger := &mockPurger{
		boundaryErr: errors.New("boundary failed"),
		purgeResult: &PurgeResult{
			DeletedDocs:  100,
			DeletedUnits: 1,
			Duration:     50 * time.Millisecond,
		},
	}

	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 7 * 24 * time.Hour,
			Source:   SourcePlatformDefault,
		},
	}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	// When boundary fails, scheduler should still proceed with purge
	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 3 {
		t.Errorf("expected 3 purge calls (boundary error should not block), got %d", len(calls))
	}
}

func TestScheduler_RunCycle_NilResolverAndPurger_NoOp(t *testing.T) {
	s := NewScheduler(
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	// Should not panic with nil resolver/purger
	s.runCycle(context.Background())
}

func TestScheduler_UsageCollection(t *testing.T) {
	usage := &mockUsageReporter{
		usage: &StorageUsage{
			TotalBytes: 1000,
			UsedBytes:  500,
			BySignal: map[SignalType]int64{
				SignalTrace:  200,
				SignalMetric: 200,
				SignalLog:    100,
			},
		},
	}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithConfig(SchedulerConfig{
			Enabled:         true,
			Interval:        time.Hour,
			TrendBufferSize: 10,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.collectUsageSnapshot(context.Background())

	trend := s.GetTrend()
	if len(trend) != 1 {
		t.Fatalf("expected 1 trend snapshot, got %d", len(trend))
	}
	if trend[0].TotalBytes != 1000 {
		t.Errorf("expected total 1000, got %d", trend[0].TotalBytes)
	}
	if trend[0].UsedBytes != 500 {
		t.Errorf("expected used 500, got %d", trend[0].UsedBytes)
	}
}

func TestScheduler_UsageCollectionError(t *testing.T) {
	usage := &mockUsageReporter{
		err: errors.New("usage error"),
	}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.collectUsageSnapshot(context.Background())

	trend := s.GetTrend()
	if len(trend) != 0 {
		t.Errorf("expected 0 trend snapshots on error, got %d", len(trend))
	}
}

func TestScheduler_AlertWarning(t *testing.T) {
	usage := &mockUsageReporter{
		usage: &StorageUsage{
			TotalBytes: 1000,
			UsedBytes:  800, // 80% > 75% warning
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{
			Enabled:            true,
			Interval:           time.Hour,
			UsageWarningRatio:  0.75,
			UsageCriticalRatio: 0.90,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.evaluateAlerts(context.Background())

	events := audit.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 alert event, got %d", len(events))
	}
	if events[0].Action != ActionAlert {
		t.Errorf("expected action %s, got %s", ActionAlert, events[0].Action)
	}
	result, ok := events[0].Result.(map[string]any)
	if !ok {
		t.Fatal("expected result to be map[string]any")
	}
	if result["level"] != "warning" {
		t.Errorf("expected level 'warning', got %v", result["level"])
	}
}

func TestScheduler_AlertCritical(t *testing.T) {
	usage := &mockUsageReporter{
		usage: &StorageUsage{
			TotalBytes: 1000,
			UsedBytes:  950, // 95% > 90% critical
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{
			Enabled:            true,
			Interval:           time.Hour,
			UsageWarningRatio:  0.75,
			UsageCriticalRatio: 0.90,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.evaluateAlerts(context.Background())

	events := audit.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 alert event, got %d", len(events))
	}
	result, ok := events[0].Result.(map[string]any)
	if !ok {
		t.Fatal("expected result to be map[string]any")
	}
	if result["level"] != "critical" {
		t.Errorf("expected level 'critical', got %v", result["level"])
	}
}

func TestScheduler_NoAlertBelowThreshold(t *testing.T) {
	usage := &mockUsageReporter{
		usage: &StorageUsage{
			TotalBytes: 1000,
			UsedBytes:  500, // 50% < 75%
		},
	}

	audit := &mockAuditEmitter{}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithAuditEmitter(audit),
		WithConfig(SchedulerConfig{
			Enabled:            true,
			Interval:           time.Hour,
			UsageWarningRatio:  0.75,
			UsageCriticalRatio: 0.90,
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.evaluateAlerts(context.Background())

	events := audit.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 alert events below threshold, got %d", len(events))
	}
}

func TestScheduler_StartStop(t *testing.T) {
	usage := &mockUsageReporter{
		usage: &StorageUsage{TotalBytes: 1000, UsedBytes: 100},
	}

	s := NewScheduler(
		WithUsageReporter(usage),
		WithConfig(SchedulerConfig{
			Enabled:  true,
			Interval: 50 * time.Millisecond, // short interval for test
		}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.Start(context.Background())

	// Wait for at least one cycle + extra tick
	time.Sleep(150 * time.Millisecond)

	s.Stop()

	// Verify some usage calls were made
	calls := usage.getCalls()
	if calls < 2 {
		t.Errorf("expected at least 2 usage calls (initial + tick), got %d", calls)
	}
}

func TestScheduler_ZeroRetention_SkipsPurge(t *testing.T) {
	purger := &mockPurger{}
	resolver := &mockResolver{
		retention: EffectiveRetention{
			Duration: 0, // zero retention → skip
			Source:   SourceBuiltinDefault,
		},
	}

	s := NewScheduler(
		WithPurger(purger),
		WithResolver(resolver),
		WithConfig(SchedulerConfig{Enabled: true, Interval: time.Hour}),
		WithLogger(zaptest.NewLogger(t)),
	)

	s.runCycle(context.Background())

	calls := purger.getPurgeExpiredCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 purge calls with zero retention, got %d", len(calls))
	}
}

func TestScheduler_NoOpAuditEmitter(t *testing.T) {
	// Verify default no-op audit emitter doesn't panic
	s := NewScheduler(
		WithConfig(SchedulerConfig{Enabled: true}),
	)

	// Should not panic
	s.audit.Emit(context.Background(), LifecycleEvent{
		Action:   ActionAlert,
		Operator: "test",
	})
	_ = s
}

// ═══════════════════════════════════════════════════
// SchedulerConfig Tests
// ═══════════════════════════════════════════════════

func TestSchedulerConfig_ApplyDefaults(t *testing.T) {
	cfg := SchedulerConfig{}
	cfg.ApplyDefaults()

	if cfg.Interval != time.Hour {
		t.Errorf("expected interval 1h, got %v", cfg.Interval)
	}
	if cfg.UsageWarningRatio != 0.75 {
		t.Errorf("expected warning ratio 0.75, got %v", cfg.UsageWarningRatio)
	}
	if cfg.UsageCriticalRatio != 0.90 {
		t.Errorf("expected critical ratio 0.90, got %v", cfg.UsageCriticalRatio)
	}
	if cfg.TrendBufferSize != 168 {
		t.Errorf("expected trend buffer size 168, got %v", cfg.TrendBufferSize)
	}
}

func TestSchedulerConfig_Validate_FixesInvalid(t *testing.T) {
	tests := []struct {
		name     string
		input    SchedulerConfig
		checkFn  func(t *testing.T, cfg SchedulerConfig)
	}{
		{
			name:  "interval below minimum is clamped",
			input: SchedulerConfig{Interval: 10 * time.Second},
			checkFn: func(t *testing.T, cfg SchedulerConfig) {
				if cfg.Interval != time.Minute {
					t.Errorf("expected minimum interval 1m, got %v", cfg.Interval)
				}
			},
		},
		{
			name:  "warning ratio out of range is reset",
			input: SchedulerConfig{Interval: time.Hour, UsageWarningRatio: 1.5},
			checkFn: func(t *testing.T, cfg SchedulerConfig) {
				if cfg.UsageWarningRatio != 0.75 {
					t.Errorf("expected warning ratio to be reset to 0.75, got %v", cfg.UsageWarningRatio)
				}
			},
		},
		{
			name:  "critical ratio must exceed warning",
			input: SchedulerConfig{Interval: time.Hour, UsageWarningRatio: 0.80, UsageCriticalRatio: 0.70},
			checkFn: func(t *testing.T, cfg SchedulerConfig) {
				if cfg.UsageCriticalRatio <= cfg.UsageWarningRatio {
					t.Errorf("critical (%v) should exceed warning (%v)", cfg.UsageCriticalRatio, cfg.UsageWarningRatio)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.input
			_ = cfg.Validate()
			tt.checkFn(t, cfg)
		})
	}
}

// ═══════════════════════════════════════════════════
// errStr helper test
// ═══════════════════════════════════════════════════

func TestErrStr(t *testing.T) {
	if got := errStr(nil); got != "" {
		t.Errorf("expected empty string for nil error, got %q", got)
	}
	if got := errStr(errors.New("something")); got != "something" {
		t.Errorf("expected 'something', got %q", got)
	}
}

// ═══════════════════════════════════════════════════
// ZapAuditEmitter (basic smoke test)
// ═══════════════════════════════════════════════════

func TestZapAuditEmitter_DoesNotPanic(t *testing.T) {
	logger := zap.NewNop()
	emitter := NewZapAuditEmitter(logger)

	// Test various event types don't panic
	emitter.Emit(context.Background(), LifecycleEvent{
		Action:   ActionAutoPurge,
		Signal:   SignalTrace,
		Operator: "test",
	})
	emitter.Emit(context.Background(), LifecycleEvent{
		Action:   ActionAlert,
		Signal:   SignalMetric,
		Operator: "scheduler",
	})
	emitter.Emit(context.Background(), LifecycleEvent{
		Action:   ActionEstimate,
		Signal:   SignalLog,
		Operator: "api:admin",
		DryRun:   true,
		Error:    "some error",
	})
	emitter.Emit(context.Background(), LifecycleEvent{
		Action:   ActionAutoPurge,
		AppID:    "my-app",
		Operator: "scheduler",
		Input:    map[string]any{"cutoff": time.Now()},
		Result:   &PurgeResult{DeletedDocs: 100},
	})
}
