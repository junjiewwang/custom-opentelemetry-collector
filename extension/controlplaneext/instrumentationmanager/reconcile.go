// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

type AuditSource string

type AuditAction string

type AuditStatus string

const (
	AuditSourceManual    AuditSource = "manual"
	AuditSourceReconcile AuditSource = "reconcile"
)

const (
	AuditActionApply          AuditAction = "apply"
	AuditActionRemove         AuditAction = "remove"
	AuditActionTargetDiscover AuditAction = "target_discovered"
	AuditActionTargetPrune    AuditAction = "target_pruned"
)

const (
	AuditStatusSuccess AuditStatus = "success"
	AuditStatusFailed  AuditStatus = "failed"
	AuditStatusSkipped AuditStatus = "skipped"
)

type RuleAuditEntry struct {
	AuditID         string      `json:"audit_id"`
	Source          AuditSource `json:"source"`
	Action          AuditAction `json:"action"`
	Status          AuditStatus `json:"status"`
	AgentID         string      `json:"agent_id,omitempty"`
	TaskID          string      `json:"task_id,omitempty"`
	Message         string      `json:"message,omitempty"`
	CreatedAtMillis int64       `json:"created_at_millis"`
}

func (s *InstrumentationService) startReconcileWorker() {
	interval := s.reconcileInterval()
	if interval <= 0 || s.reconcileCancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.reconcileCancel = cancel
	s.reconcileWG.Add(1)
	go func() {
		defer s.reconcileWG.Done()
		s.reconcileOnce(ctx)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.reconcileOnce(ctx)
			}
		}
	}()
}

func (s *InstrumentationService) stopReconcileWorker() {
	if s.reconcileCancel != nil {
		s.reconcileCancel()
		s.reconcileCancel = nil
	}
	s.reconcileWG.Wait()
}

func (s *InstrumentationService) reconcileOnce(ctx context.Context) {
	rules, err := s.store.ListRules(ctx, ListRulesQuery{IncludeDeleted: true})
	if err != nil {
		s.logger.Warn("Instrumentation reconcile skipped: list rules failed", zap.Error(err))
		return
	}

	for _, rule := range rules {
		if rule == nil || rule.ScopeType != ScopeTypeService {
			continue
		}
		if err := s.reconcileRule(ctx, rule); err != nil {
			s.logger.Warn("Instrumentation reconcile rule failed",
				zap.String("rule_id", rule.ID),
				zap.String("service_name", rule.ServiceName),
				zap.Error(err),
			)
		}
	}
}

func (s *InstrumentationService) reconcileRule(ctx context.Context, rule *Rule) error {
	if rule == nil || rule.ScopeType != ScopeTypeService {
		return nil
	}

	refreshedRule, err := s.refreshRule(ctx, rule)
	if err != nil {
		return err
	}
	targets, err := s.store.ListTargetStatuses(ctx, refreshedRule.ID)
	if err != nil {
		return err
	}
	agents, err := s.resolveTargets(ctx, refreshedRule)
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	agentsByID := indexAgentsByID(agents)
	nextTargets := make([]*RuleTargetStatus, 0, len(agentsByID)+len(targets))
	audits := make([]*RuleAuditEntry, 0)
	dirtyAgentIDs := make([]string, 0)

	for _, current := range targets {
		if current == nil {
			continue
		}
		agentID := strings.TrimSpace(current.AgentID)
		agent, ok := agentsByID[agentID]
		if !ok {
			audits = append(audits, newAuditEntry(AuditSourceReconcile, AuditActionTargetPrune, AuditStatusSuccess, agentID, "", "target left current service scope", now))
			continue
		}
		delete(agentsByID, agentID)

		next, targetAudits, dirty := s.reconcileExistingTarget(ctx, refreshedRule, current, agent, now)
		if next != nil {
			nextTargets = append(nextTargets, next)
		}
		audits = append(audits, targetAudits...)
		if dirty {
			dirtyAgentIDs = append(dirtyAgentIDs, agentID)
		}
	}

	if refreshedRule.DesiredState == RuleDesiredStateActive {
		for _, agent := range agentsByID {
			next, audit, dirty := s.dispatchOperationToAgent(ctx, refreshedRule, OperationTypeApply, nil, agent, now, AuditSourceReconcile, "discovered new service instance")
			if next != nil {
				nextTargets = append(nextTargets, next)
			}
			if audit != nil {
				audits = append(audits, audit)
			}
			if dirty && agent != nil {
				dirtyAgentIDs = append(dirtyAgentIDs, agent.AgentID)
			}
		}
	}

	summary := summarizeTargets(nextTargets)
	lastOperation := cloneOperation(refreshedRule.LastOperation)
	if lastOperation != nil {
		lastOperation.Status = summary.Status
		lastOperation.TotalTargets = summary.TotalTargets
		lastOperation.AppliedTargets = summary.AppliedTargets
		lastOperation.RunningTargets = summary.RunningTargets
		lastOperation.PendingTargets = summary.PendingTargets
		lastOperation.FailedTargets = summary.FailedTargets
		lastOperation.OfflineTargets = summary.OfflineTargets
		if isTerminalOperationStatus(lastOperation.Status) && lastOperation.CompletedAtMillis == 0 {
			lastOperation.CompletedAtMillis = now
		}
	}

	if !sameTargetStatuses(targets, nextTargets) || !sameRuleSummary(refreshedRule.Summary, summary) || !sameOperation(refreshedRule.LastOperation, lastOperation) || len(audits) > 0 {
		refreshedRule.Summary = summary
		refreshedRule.LastOperation = lastOperation
		refreshedRule.UpdatedAtMillis = now
		refreshedRule.RecentAudits = appendRuleAudits(refreshedRule.RecentAudits, audits, s.auditRetention())
		if err := s.store.SaveTargetStatuses(ctx, refreshedRule.ID, nextTargets); err != nil {
			return err
		}
		if err := s.store.SaveRule(ctx, refreshedRule, false); err != nil {
			return err
		}
	}

	s.markRuntimeSnapshotsDirty(ctx, uniqueStrings(dirtyAgentIDs))
	return nil
}

func (s *InstrumentationService) reconcileExistingTarget(ctx context.Context, rule *Rule, current *RuleTargetStatus, agent *agentregistry.AgentInfo, now int64) (*RuleTargetStatus, []*RuleAuditEntry, bool) {
	next := cloneTargetStatus(current)
	if next == nil {
		return nil, nil, false
	}
	next.Hostname = chooseString(strings.TrimSpace(agent.Hostname), next.Hostname)
	next.IP = chooseString(strings.TrimSpace(agent.IP), next.IP)
	next.DesiredState = rule.DesiredState

	if !isAgentOnline(agent) {
		if next.State != TargetStateOffline {
			next.State = TargetStateOffline
			next.UpdatedAtMillis = now
		}
		return next, nil, false
	}

	switch rule.DesiredState {
	case RuleDesiredStateActive:
		if shouldDispatch, reason := s.shouldDispatchReconcileApply(rule, next, now); shouldDispatch {
			updated, audit, dirty := s.dispatchOperationToAgent(ctx, rule, OperationTypeApply, next, agent, now, AuditSourceReconcile, reason)
			return updated, compactAudits(audit), dirty
		}
	case RuleDesiredStatePaused, RuleDesiredStateDeleted:
		if shouldDispatch, reason := s.shouldDispatchReconcileRemove(rule, next, now); shouldDispatch {
			updated, audit, dirty := s.dispatchOperationToAgent(ctx, rule, OperationTypeRemove, next, agent, now, AuditSourceReconcile, reason)
			return updated, compactAudits(audit), dirty
		}
	}

	return next, nil, false
}

func (s *InstrumentationService) shouldDispatchReconcileApply(rule *Rule, target *RuleTargetStatus, now int64) (bool, string) {
	if target == nil {
		return true, "missing target state"
	}
	if isOperationInFlight(target, OperationTypeApply) {
		return false, "apply already in flight"
	}
	if target.State == TargetStateApplied {
		if s.hasFreshRuntimeApplyDrift(rule, target.AgentID, now) && s.canRetryDispatch(target, now) {
			return true, "runtime drift detected"
		}
		return false, "target already applied"
	}
	if !s.canRetryDispatch(target, now) {
		return false, "retry interval not reached"
	}

	switch target.State {
	case TargetStateRemoved:
		return true, "target recovered to active desired state"
	case TargetStateFailed:
		return true, "retry failed apply"
	case TargetStateOffline:
		return true, "offline target is online again"
	case TargetStatePending, TargetStateDispatched, TargetStateRunning:
		if strings.TrimSpace(target.TaskType) != taskTypeForOperation(OperationTypeApply) {
			return true, "current task type does not match active desired state"
		}
		return false, "apply already pending"
	default:
		return true, "target requires apply reconciliation"
	}
}

func (s *InstrumentationService) shouldDispatchReconcileRemove(rule *Rule, target *RuleTargetStatus, now int64) (bool, string) {
	if target == nil {
		return false, "target state is missing"
	}
	if isOperationInFlight(target, OperationTypeRemove) {
		return false, "remove already in flight"
	}
	if target.State == TargetStateRemoved {
		if s.hasFreshRuntimeResidual(rule, target.AgentID, now) && s.canRetryDispatch(target, now) {
			return true, "runtime residual detected"
		}
		return false, "target already removed"
	}
	if !s.canRetryDispatch(target, now) {
		return false, "retry interval not reached"
	}

	switch target.State {
	case TargetStateApplied:
		return true, "target still applied while rule is inactive"
	case TargetStateFailed:
		return true, "retry failed remove"
	case TargetStateOffline:
		return true, "offline target is online again"
	case TargetStatePending, TargetStateDispatched, TargetStateRunning:
		if strings.TrimSpace(target.TaskType) != taskTypeForOperation(OperationTypeRemove) {
			return true, "current task type does not match inactive desired state"
		}
		return false, "remove already pending"
	default:
		return true, "target requires remove reconciliation"
	}
}

func (s *InstrumentationService) dispatchOperationToAgent(ctx context.Context, rule *Rule, operationType OperationType, current *RuleTargetStatus, agent *agentregistry.AgentInfo, now int64, source AuditSource, reason string) (*RuleTargetStatus, *RuleAuditEntry, bool) {
	desiredState := desiredStateForOperation(rule.DesiredState, operationType)
	action := auditActionForOperation(operationType)
	next := cloneTargetStatus(current)
	if next == nil {
		next = &RuleTargetStatus{}
	}
	next.RuleID = rule.ID
	if agent != nil {
		next.AgentID = agent.AgentID
		next.Hostname = strings.TrimSpace(agent.Hostname)
		next.IP = strings.TrimSpace(agent.IP)
	} else if next.AgentID == "" {
		return nil, newAuditEntry(source, action, AuditStatusFailed, "", "", "agent is required", now), false
	}
	next.DesiredState = desiredState
	next.TaskType = taskTypeForOperation(operationType)
	next.UpdatedAtMillis = now

	if agent == nil || !isAgentOnline(agent) {
		next.State = TargetStateOffline
		if strings.TrimSpace(reason) == "" {
			reason = "agent is offline"
		}
		status := AuditStatusSkipped
		if current != nil {
			status = AuditStatusSkipped
		}
		if current == nil {
			return next, newAuditEntry(source, AuditActionTargetDiscover, status, next.AgentID, "", reason, now), false
		}
		return next, newAuditEntry(source, action, status, next.AgentID, "", reason, now), false
	}

	taskID, err := tokenmanager.GenerateID()
	if err != nil {
		next.State = TargetStateFailed
		next.LastErrorMessage = fmt.Sprintf("generate task id: %v", err)
		return next, newAuditEntry(source, action, AuditStatusFailed, next.AgentID, "", next.LastErrorMessage, now), false
	}
	params, err := buildTaskParameters(rule, operationType)
	if err != nil {
		next.State = TargetStateFailed
		next.LastErrorMessage = fmt.Sprintf("marshal task parameters: %v", err)
		return next, newAuditEntry(source, action, AuditStatusFailed, next.AgentID, "", next.LastErrorMessage, now), false
	}

	task := &model.Task{
		ID:              taskID,
		TypeName:        taskTypeForOperation(operationType),
		ParametersJSON:  params,
		TargetAgentID:   agent.AgentID,
		TimeoutMillis:   60000,
		CreatedAtMillis: now,
	}
	agentMeta := &taskmanager.AgentMeta{AgentID: agent.AgentID, AppID: agent.AppID, ServiceName: agent.ServiceName}
	if err := s.taskMgr.SubmitTaskForAgent(ctx, agentMeta, task); err != nil {
		next.State = TargetStateFailed
		next.LastErrorMessage = err.Error()
		next.LastDispatchAtMillis = now
		return next, newAuditEntry(source, action, AuditStatusFailed, next.AgentID, taskID, err.Error(), now), false
	}

	next.TaskID = taskID
	next.TaskStatus = "pending"
	next.LastErrorMessage = ""
	next.State = TargetStateDispatched
	next.LastDispatchAtMillis = now
	if strings.TrimSpace(reason) == "" {
		reason = "task dispatched"
	}
	if current == nil && source == AuditSourceReconcile {
		return next, newAuditEntry(source, AuditActionTargetDiscover, AuditStatusSuccess, next.AgentID, taskID, reason, now), true
	}
	return next, newAuditEntry(source, action, AuditStatusSuccess, next.AgentID, taskID, reason, now), true
}

func (s *InstrumentationService) canRetryDispatch(target *RuleTargetStatus, now int64) bool {
	if target == nil || target.LastDispatchAtMillis == 0 {
		return true
	}
	return now-target.LastDispatchAtMillis >= s.reconcileRetryInterval().Milliseconds()
}

func (s *InstrumentationService) hasFreshRuntimeApplyDrift(rule *Rule, agentID string, now int64) bool {
	if rule == nil || strings.TrimSpace(agentID) == "" {
		return false
	}
	entry, _ := s.runtimeSnapshots.Get(context.Background(), agentID)
	if !isUsableRuntimeSnapshot(entry, now) {
		return false
	}
	item := findRuntimeSnapshotItem(entry.Items, rule.ID)
	target := &RuleRuntimeSnapshotTarget{
		SnapshotAvailable:        entry.HasPayload,
		RuntimeFound:            item != nil,
		IsEffective:             item != nil && item.IsEffective,
		InstrumentationAvailable: entry.Summary.InstrumentationAvailable,
		EnhancementCapability:    entry.Summary.EnhancementCapability,
	}
	for _, reason := range detectRuntimeDrift(rule, target, item) {
		if reason == RuntimeDriftReasonMissing || reason == RuntimeDriftReasonIneffective {
			return true
		}
	}
	return false
}

func (s *InstrumentationService) hasFreshRuntimeResidual(rule *Rule, agentID string, now int64) bool {
	if rule == nil || strings.TrimSpace(agentID) == "" {
		return false
	}
	entry, _ := s.runtimeSnapshots.Get(context.Background(), agentID)
	if !isUsableRuntimeSnapshot(entry, now) {
		return false
	}
	item := findRuntimeSnapshotItem(entry.Items, rule.ID)
	if item == nil {
		return false
	}
	return isRuntimeResidual(item)
}

func (s *InstrumentationService) reconcileInterval() time.Duration {
	if s.config.ReconcileInterval <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.config.ReconcileInterval) * time.Millisecond
}

func (s *InstrumentationService) reconcileRetryInterval() time.Duration {
	if s.config.ReconcileRetryInterval <= 0 {
		return 15 * time.Second
	}
	return time.Duration(s.config.ReconcileRetryInterval) * time.Millisecond
}

func (s *InstrumentationService) auditRetention() int {
	if s.config.AuditRetention <= 0 {
		return 20
	}
	return s.config.AuditRetention
}

func auditActionForOperation(operationType OperationType) AuditAction {
	if operationType == OperationTypeRemove {
		return AuditActionRemove
	}
	return AuditActionApply
}

func isOperationInFlight(target *RuleTargetStatus, operationType OperationType) bool {
	if target == nil {
		return false
	}
	if target.State != TargetStateDispatched && target.State != TargetStateRunning {
		return false
	}
	return strings.TrimSpace(target.TaskType) == taskTypeForOperation(operationType)
}

func isUsableRuntimeSnapshot(entry *agentRuntimeSnapshotCacheEntry, now int64) bool {
	if entry == nil {
		return false
	}
	if entry.Dirty || entry.LastRefreshStatus != RuntimeRefreshStatusSuccess {
		return false
	}
	return entry.ExpiresAtMillis > now
}

func indexAgentsByID(agents []*agentregistry.AgentInfo) map[string]*agentregistry.AgentInfo {
	out := make(map[string]*agentregistry.AgentInfo, len(agents))
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		agentID := strings.TrimSpace(agent.AgentID)
		if agentID == "" {
			continue
		}
		out[agentID] = agent
	}
	return out
}

func appendRuleAudits(existing []*RuleAuditEntry, additions []*RuleAuditEntry, limit int) []*RuleAuditEntry {
	if len(additions) == 0 && len(existing) == 0 {
		return nil
	}
	out := make([]*RuleAuditEntry, 0, len(additions)+len(existing))
	for _, item := range additions {
		if item != nil {
			out = append(out, cloneRuleAuditEntry(item))
		}
	}
	for _, item := range existing {
		if item != nil {
			out = append(out, cloneRuleAuditEntry(item))
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func compactAudits(items ...*RuleAuditEntry) []*RuleAuditEntry {
	out := make([]*RuleAuditEntry, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, item)
		}
	}
	return out
}

func cloneRuleAuditEntry(item *RuleAuditEntry) *RuleAuditEntry {
	if item == nil {
		return nil
	}
	copied := *item
	return &copied
}

func newAuditEntry(source AuditSource, action AuditAction, status AuditStatus, agentID, taskID, message string, now int64) *RuleAuditEntry {
	auditID, err := tokenmanager.GenerateID()
	if err != nil {
		auditID = fmt.Sprintf("audit-%d", now)
	}
	return &RuleAuditEntry{
		AuditID:         auditID,
		Source:          source,
		Action:          action,
		Status:          status,
		AgentID:         strings.TrimSpace(agentID),
		TaskID:          strings.TrimSpace(taskID),
		Message:         strings.TrimSpace(message),
		CreatedAtMillis: now,
	}
}

func sameTargetStatuses(a, b []*RuleTargetStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameTargetStatus(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameTargetStatus(a, b *RuleTargetStatus) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
