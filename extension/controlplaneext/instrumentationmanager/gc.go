// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// startGCWorker starts a background goroutine that periodically scans for deleted rules
// whose targets have all reached a terminal state and whose retention period has elapsed,
// then physically removes them from the store.
func (s *InstrumentationService) startGCWorker() {
	interval := s.gcInterval()
	if interval <= 0 || s.gcCancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.gcCancel = cancel
	s.gcWG.Add(1)
	go func() {
		defer s.gcWG.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.gcOnce(ctx)
			}
		}
	}()
	s.logger.Info("GC worker started",
		zap.Duration("interval", interval),
		zap.Duration("retention", s.deletedRuleRetention()),
	)
}

func (s *InstrumentationService) stopGCWorker() {
	if s.gcCancel != nil {
		s.gcCancel()
		s.gcCancel = nil
	}
	s.gcWG.Wait()
}

// gcOnce scans all deleted rules and physically removes those that are eligible for cleanup.
// A deleted rule is eligible when:
//  1. Its desired_state is "deleted"
//  2. All of its targets are in a terminal state (removed, failed, expired, or offline)
//  3. Its UpdatedAtMillis + retention period has elapsed
func (s *InstrumentationService) gcOnce(ctx context.Context) {
	rules, err := s.store.ListRules(ctx, ListRulesQuery{IncludeDeleted: true})
	if err != nil {
		s.logger.Warn("GC: failed to list rules", zap.Error(err))
		return
	}

	now := time.Now().UnixMilli()
	retention := s.deletedRuleRetention()
	var purgedCount int

	for _, rule := range rules {
		if rule == nil || rule.DesiredState != RuleDesiredStateDeleted {
			continue
		}

		// Check if the retention period has elapsed since the rule was last updated.
		if retention > 0 && now-rule.UpdatedAtMillis < retention.Milliseconds() {
			continue
		}

		// Check if all targets are in a terminal state.
		targets, err := s.store.ListTargetStatuses(ctx, rule.ID)
		if err != nil {
			s.logger.Warn("GC: failed to list target statuses",
				zap.String("rule_id", rule.ID),
				zap.Error(err),
			)
			continue
		}

		if !allTargetsTerminal(targets) {
			continue
		}

		// All conditions met — physically delete the rule and its targets.
		if err := s.store.PhysicalDeleteRule(ctx, rule.ID); err != nil {
			s.logger.Warn("GC: failed to physically delete rule",
				zap.String("rule_id", rule.ID),
				zap.Error(err),
			)
			continue
		}
		purgedCount++
		s.logger.Info("GC: physically deleted rule",
			zap.String("rule_id", rule.ID),
			zap.String("rule_name", rule.Name),
			zap.Int("target_count", len(targets)),
		)
	}

	if purgedCount > 0 {
		s.logger.Info("GC cycle completed", zap.Int("purged_rules", purgedCount))
	}
}

// allTargetsTerminal returns true if every target in the list is in a terminal state.
// Terminal states are: removed, failed, expired, offline.
// An empty target list is considered terminal (no targets to wait for).
func allTargetsTerminal(targets []*RuleTargetStatus) bool {
	for _, target := range targets {
		if target == nil {
			continue
		}
		if !isTerminalTargetState(target.State) {
			return false
		}
	}
	return true
}

// isTerminalTargetState returns true if the given target state is a terminal state
// from which no further reconciliation action is expected.
func isTerminalTargetState(state TargetState) bool {
	switch state {
	case TargetStateRemoved, TargetStateFailed, TargetStateExpired, TargetStateOffline:
		return true
	default:
		return false
	}
}

func (s *InstrumentationService) gcInterval() time.Duration {
	if s.config.GCInterval <= 0 {
		return 60 * time.Second // default: 60 seconds
	}
	return time.Duration(s.config.GCInterval) * time.Millisecond
}

func (s *InstrumentationService) deletedRuleRetention() time.Duration {
	if s.config.DeletedRuleRetention <= 0 {
		return 7 * 24 * time.Hour // default: 7 days
	}
	return time.Duration(s.config.DeletedRuleRetention) * time.Millisecond
}
