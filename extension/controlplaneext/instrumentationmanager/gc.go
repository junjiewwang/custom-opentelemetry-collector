// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"errors"
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
	var purgedCount, alertableUnconfirmed int
	manualReview, evidencePending := s.updateUnconfirmedMetrics(ctx, rules)

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

		if !s.config.GCUninstallEvidenceRequired {
			// Rollback switch: legacy behaviour.
			if !allTargetsTerminal(targets) {
				continue
			}
		} else if !hasUninstallEvidence(rule, targets) {
			// Keeping the record is the precondition for self-healing: while it
			// exists, reconcile can keep re-dispatching dynamic_uninstrument.
			// Verified in isolation: purging an unconfirmed rule leaves an agent
			// with no compensate task when it comes back online.
			if s.config.GCUnconfirmedAlertAfter > 0 && now-rule.UpdatedAtMillis >= s.config.GCUnconfirmedAlertAfter {
				alertableUnconfirmed++
			}
			continue
		}

		// All conditions met — physically delete the rule and its targets.
		if err := s.store.PhysicalDeleteRule(ctx, rule.ID); err != nil {
			if errors.Is(err, ErrRuleNotFound) {
				continue // another replica already purged it
			}
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
	if alertableUnconfirmed > 0 {
		s.logger.Debug("GC: deleted rules still lack uninstall evidence; see instrumentation_unconfirmed_rules gauge",
			zap.Int("rule_count", alertableUnconfirmed),
			zap.Int64("manual_review", manualReview),
			zap.Int64("evidence_pending", evidencePending),
		)
	}
}

// updateUnconfirmedMetrics classifies deleted rules that still lack uninstall
// evidence and publishes the aggregate to the observable gauge. It runs on
// every replica during the GC sweep, so metric values do not depend on which
// replica won the evidence-verifier batch lease.
func (s *InstrumentationService) updateUnconfirmedMetrics(ctx context.Context, rules []*Rule) (int64, int64) {
	var manualReview, evidencePending int64
	for _, rule := range rules {
		if rule == nil || rule.DesiredState != RuleDesiredStateDeleted || rule.UninstallConfirmed {
			continue
		}
		targets, err := s.store.ListTargetStatuses(ctx, rule.ID)
		if err != nil {
			// Unknown breakdown still counts as an unconfirmed rule.
			evidencePending++
			continue
		}
		if len(targets) == 0 && rule.EverApplySucceeded {
			manualReview++
		} else {
			evidencePending++
		}
	}
	s.metrics.setUnconfirmedCounts(manualReview, evidencePending)
	return manualReview, evidencePending
}

// hasUninstallEvidence reports whether it is safe to forget a deleted rule.
// Rule-level evidence covers rules with no targets; otherwise every target
// must have a strong runtime-clean confirmation (not merely a task ACK).
func hasUninstallEvidence(rule *Rule, targets []*RuleTargetStatus) bool {
	if rule == nil {
		return false
	}
	if rule.UninstallConfirmed {
		return true
	}
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if target == nil || !target.RuntimeClean {
			return false
		}
	}
	return true
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
