// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"go.uber.org/zap"
)

// evidenceBatchLeaseKey is a pseudo-agent ID used with the existing
// TryAcquireRefreshLease primitive to elect one replica per scan cycle.
// Memory stores always grant the lease (single-replica mode).
const evidenceBatchLeaseKey = "__evidence_scan_batch__"

func (s *InstrumentationService) evidenceScanInterval() time.Duration {
	if s.config.GCEvidenceScanInterval <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(s.config.GCEvidenceScanInterval) * time.Millisecond
}

func (s *InstrumentationService) evidenceBatchLeaseTTL() time.Duration {
	if s.config.GCEvidenceBatchLeaseTTL <= 0 {
		return 2 * time.Minute
	}
	return time.Duration(s.config.GCEvidenceBatchLeaseTTL) * time.Millisecond
}

func (s *InstrumentationService) evidenceMaxRulesPerScan() int {
	if s.config.GCEvidenceMaxRulesPerScan <= 0 {
		return 50
	}
	return s.config.GCEvidenceMaxRulesPerScan
}

func (s *InstrumentationService) startEvidenceVerifier() {
	if !s.config.GCUninstallEvidenceRequired || s.evidenceCancel != nil {
		return
	}
	interval := s.evidenceScanInterval()
	ctx, cancel := context.WithCancel(context.Background())
	s.evidenceCancel = cancel
	s.evidenceWG.Add(1)
	go func() {
		defer s.evidenceWG.Done()
		for {
			// Jitter avoids all replicas racing for the batch lease at the
			// same instant. The lease itself elects exactly one winner.
			jittered := time.Duration(float64(interval) * (0.9 + 0.2*rand.Float64()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(jittered):
				s.verifyEvidenceOnce(ctx)
			}
		}
	}()
	s.logger.Info("Evidence verifier started",
		zap.Duration("interval", interval),
		zap.Duration("batch_lease_ttl", s.evidenceBatchLeaseTTL()),
		zap.Int("max_rules_per_scan", s.evidenceMaxRulesPerScan()),
	)
}

func (s *InstrumentationService) stopEvidenceVerifier() {
	if s.evidenceCancel != nil {
		s.evidenceCancel()
		s.evidenceCancel = nil
	}
	s.evidenceWG.Wait()
}

// verifyEvidenceOnce runs a globally-elected scan cycle. The count limits are
// global (per winning replica), not per replica.
func (s *InstrumentationService) verifyEvidenceOnce(ctx context.Context) {
	acquired, err := s.runtimeSnapshots.TryAcquireRefreshLease(
		ctx, evidenceBatchLeaseKey, s.runtimeInstanceID, s.evidenceBatchLeaseTTL())
	if err != nil {
		s.logger.Warn("Evidence verifier: batch lease failed", zap.Error(err))
		return
	}
	if !acquired {
		s.logger.Info("Evidence verifier: skipped cycle, another replica owns the batch lease",
			zap.String("instance", s.runtimeInstanceID))
		return
	}
	s.logger.Info("Evidence verifier: batch lease acquired",
		zap.String("instance", s.runtimeInstanceID))

	rules, err := s.store.ListRules(ctx, ListRulesQuery{IncludeDeleted: true})
	if err != nil {
		s.logger.Warn("Evidence verifier: failed to list rules", zap.Error(err))
		return
	}
	candidates := make([]*Rule, 0, len(rules))
	for _, rule := range rules {
		if rule != nil && rule.DesiredState == RuleDesiredStateDeleted && !rule.UninstallConfirmed {
			candidates = append(candidates, rule)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].UpdatedAtMillis < candidates[j].UpdatedAtMillis
	})
	if limit := s.evidenceMaxRulesPerScan(); len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for _, rule := range candidates {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.VerifyUninstall(ctx, rule.ID); err != nil {
			s.logger.Warn("Evidence verifier: verification failed",
				zap.String("rule_id", rule.ID), zap.Error(err))
		}
	}
}

// VerifyUninstall records strong evidence that a deleted rule no longer has
// live instrumentation. It is safe to call repeatedly and is also exposed for
// operator-triggered verification.
func (s *InstrumentationService) VerifyUninstall(ctx context.Context, ruleID string) (*Rule, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.DesiredState != RuleDesiredStateDeleted {
		return nil, fmt.Errorf("rule %s is not deleted", ruleID)
	}
	if rule.UninstallConfirmed {
		return rule, nil
	}

	targets, err := s.store.ListTargetStatuses(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()

	// Branch 1: never instrumented and no target records. There is no probe
	// state to query, so the absence of any apply attempt is the evidence.
	if len(targets) == 0 {
		if rule.EverApplySucceeded {
			s.logger.Warn("Evidence verifier: deleted rule had successful apply but no targets; manual review required",
				zap.String("rule_id", ruleID))
			return rule, nil
		}
		rule.UninstallConfirmed = true
		rule.UninstallEvidenceSource = EvidenceSourceNeverInstrumented
		rule.UninstallConfirmedAtMillis = now
		if err := s.commitRuleBestEffort(ctx, rule, nil); err != nil {
			return nil, err
		}
		return s.store.GetRule(ctx, ruleID)
	}

	// Branch 2: query the probe. RefreshRuleRuntimeSnapshot also refreshes
	// target statuses, so re-read them afterwards before writing evidence.
	snapshot, err := s.RefreshRuleRuntimeSnapshot(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	latestTargets, err := s.store.ListTargetStatuses(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	latestRule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if latestRule.UninstallConfirmed {
		return latestRule, nil
	}

	byAgent := make(map[string]*RuleRuntimeSnapshotTarget, len(snapshot.Targets))
	for _, item := range snapshot.Targets {
		if item != nil {
			byAgent[item.AgentID] = item
		}
	}

	changed := false
	for _, target := range latestTargets {
		if target == nil || target.RuntimeClean {
			continue
		}
		state := byAgent[target.AgentID]
		if state == nil || !state.SnapshotAvailable || state.IsStale || state.Dirty ||
			state.LastRefreshStatus != RuntimeRefreshStatusSuccess {
			continue // unknown is never treated as "clean"
		}
		if state.IsApplied || state.IsEffective || hasDeletedResidual(state.DriftReasons) {
			if hasDeletedResidual(state.DriftReasons) {
				s.logger.Warn("Evidence verifier: runtime residual detected; reconcile will re-dispatch remove",
					zap.String("rule_id", ruleID), zap.String("agent_id", target.AgentID))
			}
			continue
		}
		target.RuntimeClean = true
		target.RuntimeCleanAtMillis = now
		target.EvidenceSource = EvidenceSourceRuntimeSnapshot
		changed = true
	}

	allClean := len(latestTargets) > 0
	for _, target := range latestTargets {
		if target == nil || !target.RuntimeClean {
			allClean = false
			break
		}
	}
	if allClean {
		latestRule.UninstallConfirmed = true
		latestRule.UninstallEvidenceSource = EvidenceSourceRuntimeSnapshot
		latestRule.UninstallConfirmedAtMillis = now
		changed = true
	}
	if changed {
		if err := s.commitRuleBestEffort(ctx, latestRule, latestTargets); err != nil {
			return nil, err
		}
	}
	return s.store.GetRule(ctx, ruleID)
}

func hasDeletedResidual(reasons []RuntimeDriftReason) bool {
	for _, reason := range reasons {
		if reason == RuntimeDriftReasonDeletedResidual {
			return true
		}
	}
	return false
}
