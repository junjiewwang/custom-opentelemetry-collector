// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/taskengine"
)

// LifecycleScheduler orchestrates periodic data lifecycle operations.
// It depends ONLY on abstractions (DIP), making it testable and provider-agnostic.
//
// Responsibilities (SRP):
//  1. Periodic tick management (start/stop)
//  2. Orchestrate: resolve retention → compute cutoff → invoke purger → audit
//
// NOT responsible for: HOW to purge, WHERE policies are stored, HOW to measure usage.
//
// For distributed purge mode, the scheduler uses the unified taskengine.Engine
// via DistributedPurgeOrchestrator. The engine instance may be shared from
// controlplaneext (same process) or created locally (independent deployment).
type LifecycleScheduler struct {
	resolver     RetentionResolver
	purger       LifecyclePurger
	usage        UsageReporter
	audit        AuditEmitter
	orchestrator *DistributedPurgeOrchestrator // Engine-based distributed purge
	config       SchedulerConfig
	nodeID       string
	logger       *zap.Logger

	// Usage trend buffer
	trendMu  sync.RWMutex
	trendBuf *TrendBuffer

	// Lifecycle control
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// SchedulerOption configures the LifecycleScheduler via functional options.
type SchedulerOption func(*LifecycleScheduler)

// WithResolver sets the retention resolver.
func WithResolver(r RetentionResolver) SchedulerOption {
	return func(s *LifecycleScheduler) { s.resolver = r }
}

// WithPurger sets the lifecycle purger.
func WithPurger(p LifecyclePurger) SchedulerOption {
	return func(s *LifecycleScheduler) { s.purger = p }
}

// WithUsageReporter sets the usage reporter.
func WithUsageReporter(u UsageReporter) SchedulerOption {
	return func(s *LifecycleScheduler) { s.usage = u }
}

// WithAuditEmitter sets the audit emitter.
func WithAuditEmitter(a AuditEmitter) SchedulerOption {
	return func(s *LifecycleScheduler) { s.audit = a }
}

// WithConfig sets the scheduler configuration.
func WithConfig(cfg SchedulerConfig) SchedulerOption {
	return func(s *LifecycleScheduler) { s.config = cfg }
}

// WithLogger sets the logger.
func WithLogger(l *zap.Logger) SchedulerOption {
	return func(s *LifecycleScheduler) { s.logger = l }
}

// WithEngine sets the unified task engine for distributed purge.
// The engine may be shared from controlplaneext or created locally.
// Also requires a LeaderElector for epoch coordination.
func WithEngine(engine taskengine.Engine, elector LeaderElector) SchedulerOption {
	return func(s *LifecycleScheduler) {
		// orchestrator will be created in NewScheduler after config is applied
		s.orchestrator = &DistributedPurgeOrchestrator{
			adapter: nil, // will be set in NewScheduler
			elector: elector,
			logger:  nil, // will be set in NewScheduler
		}
		// Store engine temporarily in orchestrator; full initialization in NewScheduler
		s.orchestrator.adapter = &EngineAdapter{engine: engine}
	}
}

// NewScheduler creates a new LifecycleScheduler with the given options.
func NewScheduler(opts ...SchedulerOption) *LifecycleScheduler {
	s := &LifecycleScheduler{
		stopCh: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}

	// Apply config defaults
	s.config.ApplyDefaults()

	// Initialize nodeID from config or generate one
	if s.config.NodeID != "" {
		s.nodeID = s.config.NodeID
	} else {
		s.nodeID = fmt.Sprintf("node-%d", time.Now().UnixNano())
	}

	// Initialize trend buffer
	s.trendBuf = NewTrendBuffer(s.config.TrendBufferSize)

	// Default logger
	if s.logger == nil {
		s.logger = zap.NewNop()
	}

	// Default no-op audit if not provided
	if s.audit == nil {
		s.audit = noOpAuditEmitter{}
	}

	// Finalize orchestrator initialization if engine was provided
	if s.orchestrator != nil && s.orchestrator.adapter != nil {
		engine := s.orchestrator.adapter.engine
		elector := s.orchestrator.elector
		s.orchestrator = NewDistributedPurgeOrchestrator(
			engine, elector, s.config, s.nodeID, s.logger,
		)
	}

	return s
}

// Start begins the background scheduling goroutine.
func (s *LifecycleScheduler) Start(_ context.Context) {
	if !s.config.Enabled {
		s.logger.Info("Lifecycle scheduler is disabled")
		return
	}

	s.logger.Info("Starting lifecycle scheduler",
		zap.Duration("interval", s.config.Interval),
		zap.Bool("dry_run", s.config.DryRun),
		zap.Bool("distributed", s.config.Distributed),
		zap.String("node_id", s.nodeID),
	)

	s.wg.Add(1)
	go s.loop()
}

// Stop gracefully stops the scheduler and waits for the loop to exit.
func (s *LifecycleScheduler) Stop() {
	if !s.config.Enabled {
		return
	}

	s.logger.Info("Stopping lifecycle scheduler")
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("Lifecycle scheduler stopped")
}

// GetTrend returns the usage trend snapshots (thread-safe).
func (s *LifecycleScheduler) GetTrend() []UsageSnapshot {
	s.trendMu.RLock()
	defer s.trendMu.RUnlock()
	return s.trendBuf.All()
}

// TrendReader returns the underlying UsageHistoryReader for trend aggregation.
// Used by the extension layer to wire TrendAggregator without directly depending on TrendBuffer.
func (s *LifecycleScheduler) TrendReader() UsageHistoryReader {
	return s.trendBuf
}

// loop is the main scheduling goroutine.
func (s *LifecycleScheduler) loop() {
	defer s.wg.Done()

	// Run immediately on start
	s.runCycle(context.Background())

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.runCycle(context.Background())
		}
	}
}

// runCycle executes one full lifecycle management cycle.
func (s *LifecycleScheduler) runCycle(ctx context.Context) {
	start := time.Now()
	s.logger.Debug("Starting lifecycle cycle")

	// Phase 1: Collect usage snapshot (observe)
	s.collectUsageSnapshot(ctx)

	// Phase 2: Evaluate capacity alerts
	s.evaluateAlerts(ctx)

	// Phase 3: Purge expired data
	if s.config.Distributed && s.orchestrator != nil {
		// Engine-based distributed purge (shared or local engine)
		s.distributedPurgeViaEngine(ctx)
	} else {
		// Single-node mode
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
	}

	// Phase 4: Per-app purge — apps with stricter retention than platform default
	s.purgeAppsWithOverrides(ctx)

	s.logger.Debug("Lifecycle cycle completed", zap.Duration("elapsed", time.Since(start)))
}

// distributedPurgeViaEngine uses the unified task engine for distributed purge.
func (s *LifecycleScheduler) distributedPurgeViaEngine(ctx context.Context) {
	// Check if purger supports distributed mode
	lister, hasLister := s.purger.(IndexLister)
	singlePurger, hasSingle := s.purger.(SingleIndexPurger)
	if !hasLister || !hasSingle {
		s.logger.Debug("Purger does not support distributed mode, falling back to single-node")
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
		return
	}

	// Delegate to orchestrator
	handled := s.orchestrator.Execute(ctx, lister, singlePurger, s.resolver, s.audit, s.stopCh)
	if !handled {
		// Orchestrator decided to fallback (threshold, election failure, etc.)
		for _, signal := range AllSignals() {
			s.purgeSignal(ctx, signal)
		}
	}
}

// purgeAppsWithOverrides scans all apps that have per-app retention overrides
// and purges their data using the app-specific cutoff (which may be stricter than platform default).
// This runs after the platform-level purge and handles per-app retention correctly.
func (s *LifecycleScheduler) purgeAppsWithOverrides(ctx context.Context) {
	if s.resolver == nil || s.purger == nil {
		return
	}

	overrides, err := s.resolver.ListAppOverrides(ctx)
	if err != nil {
		s.logger.Warn("Failed to list app retention overrides", zap.Error(err))
		return
	}
	if len(overrides) == 0 {
		return
	}

	s.logger.Info("Checking per-app retention overrides", zap.Int("app_count", len(overrides)))

	for _, entry := range overrides {
		for signal, perAppDur := range entry.Overrides {
			if perAppDur <= 0 {
				continue
			}

			// Resolve platform default for comparison
			platformRet, err := s.resolver.Resolve(ctx, signal, "")
			if err != nil {
				s.logger.Error("Failed to resolve platform retention for per-app check",
					zap.String("appID", entry.AppID),
					zap.String("signal", string(signal)),
					zap.Error(err),
				)
				continue
			}

			// Only purge if per-app retention is STRICTER than platform default
			if perAppDur >= platformRet.Duration {
				continue
			}

			// Align to UTC midnight: date-based indices cover full days.
			// E.g., "keep 1 day" = keep today + yesterday, delete the day before yesterday and earlier.
			today := time.Now().UTC().Truncate(24 * time.Hour)
			cutoff := today.Add(-perAppDur)

			if s.config.DryRun {
				s.logger.Info("[DRY-RUN] Would purge per-app expired data",
					zap.String("appID", entry.AppID),
					zap.String("signal", string(signal)),
					zap.Time("cutoff", cutoff),
				)
				continue
			}

			result, err := s.purger.PurgeByApp(ctx, entry.AppID, signal, cutoff)
			if err != nil {
				s.logger.Error("Failed to purge per-app expired data",
					zap.String("appID", entry.AppID),
					zap.String("signal", string(signal)),
					zap.Error(err),
				)
				s.audit.Emit(ctx, LifecycleEvent{
					Timestamp: time.Now(),
					Action:    ActionAutoPurge,
					Signal:    signal,
					Operator:  "scheduler:per-app",
					Result:    map[string]any{"appID": entry.AppID, "cutoff": cutoff},
					Error:     err.Error(),
				})
				continue
			}

			if result.DeletedUnits > 0 {
				s.logger.Info("Purged per-app expired data",
					zap.String("appID", entry.AppID),
					zap.String("signal", string(signal)),
					zap.Duration("per_app_retention", perAppDur),
					zap.Time("cutoff", cutoff),
					zap.Int("deleted_indices", result.DeletedUnits),
				)
				s.audit.Emit(ctx, LifecycleEvent{
					Timestamp: time.Now(),
					Action:    ActionAutoPurge,
					Signal:    signal,
					Operator:  "scheduler:per-app:" + entry.AppID,
					Result:    map[string]any{"appID": entry.AppID, "cutoff": cutoff, "deleted_units": result.DeletedUnits},
				})
			}
		}
	}
}

// collectUsageSnapshot queries current usage and stores in the trend buffer.
func (s *LifecycleScheduler) collectUsageSnapshot(ctx context.Context) {
	if s.usage == nil {
		return
	}

	usage, err := s.usage.GetUsage(ctx)
	if err != nil {
		s.logger.Warn("Failed to collect usage snapshot", zap.Error(err))
		return
	}

	snapshot := UsageSnapshot{
		Timestamp:  time.Now(),
		TotalBytes: usage.TotalBytes,
		UsedBytes:  usage.UsedBytes,
		BySignal:   usage.BySignal,
		ByApp:      usage.ByApp,
	}

	s.trendMu.Lock()
	s.trendBuf.Push(snapshot)
	s.trendMu.Unlock()
}

// evaluateAlerts checks if usage exceeds configured thresholds.
func (s *LifecycleScheduler) evaluateAlerts(ctx context.Context) {
	if s.usage == nil {
		return
	}

	usage, err := s.usage.GetUsage(ctx)
	if err != nil {
		return
	}

	if usage.TotalBytes == 0 {
		return
	}

	ratio := float64(usage.UsedBytes) / float64(usage.TotalBytes)

	if ratio >= s.config.UsageCriticalRatio {
		s.logger.Error("Storage usage CRITICAL",
			zap.Float64("ratio", ratio),
			zap.Int64("used_bytes", usage.UsedBytes),
			zap.Int64("total_bytes", usage.TotalBytes),
		)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionAlert,
			Operator:  "scheduler",
			Result:    map[string]any{"level": "critical", "ratio": ratio},
		})
	} else if ratio >= s.config.UsageWarningRatio {
		s.logger.Warn("Storage usage WARNING",
			zap.Float64("ratio", ratio),
			zap.Int64("used_bytes", usage.UsedBytes),
			zap.Int64("total_bytes", usage.TotalBytes),
		)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionAlert,
			Operator:  "scheduler",
			Result:    map[string]any{"level": "warning", "ratio": ratio},
		})
	}
}

// purgeSignal handles the purge logic for a single signal type.
func (s *LifecycleScheduler) purgeSignal(ctx context.Context, signal SignalType) {
	if s.resolver == nil || s.purger == nil {
		return
	}

	// Step 1: Resolve effective retention
	retention, err := s.resolver.Resolve(ctx, signal, "" /* platform level */)
	if err != nil {
		s.logger.Error("Failed to resolve retention",
			zap.String("signal", string(signal)),
			zap.Error(err),
		)
		return
	}

	if retention.Duration <= 0 {
		s.logger.Debug("Retention is zero or negative, skipping purge",
			zap.String("signal", string(signal)),
		)
		return
	}

	// Step 2: Compute cutoff time
	cutoff := time.Now().Add(-retention.Duration)

	// Step 3: Check if there's data to purge (avoid unnecessary operations)
	boundary, err := s.purger.GetDataBoundary(ctx, signal)
	if err != nil {
		s.logger.Warn("Failed to get data boundary, proceeding with purge anyway",
			zap.String("signal", string(signal)),
			zap.Error(err),
		)
	} else if boundary.IsEmpty || boundary.OldestAt == nil || !boundary.OldestAt.Before(cutoff) {
		s.logger.Debug("No expired data to purge",
			zap.String("signal", string(signal)),
			zap.Time("cutoff", cutoff),
		)
		return
	}

	// Step 4: Execute or estimate
	if s.config.DryRun {
		estimate, err := s.purger.EstimatePurge(ctx, signal, cutoff)
		s.audit.Emit(ctx, LifecycleEvent{
			Timestamp: time.Now(),
			Action:    ActionEstimate,
			Signal:    signal,
			Operator:  "scheduler",
			DryRun:    true,
			Input:     map[string]any{"cutoff": cutoff, "retention": retention.Duration.String()},
			Result:    estimate,
			Error:     errStr(err),
		})
		if err != nil {
			s.logger.Warn("Estimate purge failed",
				zap.String("signal", string(signal)),
				zap.Error(err),
			)
		} else {
			s.logger.Info("[DRY-RUN] Would purge expired data",
				zap.String("signal", string(signal)),
				zap.Int64("estimated_docs", estimate.EstimatedDocs),
				zap.Int64("estimated_bytes", estimate.EstimatedBytes),
				zap.Int("affected_units", len(estimate.AffectedUnits)),
			)
		}
		return
	}

	// Execute actual purge
	result, err := s.purger.PurgeExpired(ctx, signal, cutoff)
	s.audit.Emit(ctx, LifecycleEvent{
		Timestamp: time.Now(),
		Action:    ActionAutoPurge,
		Signal:    signal,
		Operator:  "scheduler",
		Input:     map[string]any{"cutoff": cutoff, "retention": retention.Duration.String()},
		Result:    result,
		Error:     errStr(err),
	})

	if err != nil {
		s.logger.Error("Purge failed",
			zap.String("signal", string(signal)),
			zap.Time("cutoff", cutoff),
			zap.Error(err),
		)
		return
	}

	if result != nil && result.DeletedDocs > 0 {
		s.logger.Info("Purge completed",
			zap.String("signal", string(signal)),
			zap.Int64("deleted_docs", result.DeletedDocs),
			zap.Int("deleted_units", result.DeletedUnits),
			zap.Int64("freed_bytes", result.FreedBytes),
			zap.Duration("duration", result.Duration),
		)
	}
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

// errStr safely converts an error to string, returning empty for nil.
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// noOpAuditEmitter is a safe default that does nothing.
type noOpAuditEmitter struct{}

func (noOpAuditEmitter) Emit(_ context.Context, _ LifecycleEvent) {}
