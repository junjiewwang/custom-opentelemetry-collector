// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

type InstrumentationService struct {
	logger              *zap.Logger
	config              Config
	store               RuleStore
	agentReg            agentregistry.AgentRegistry
	taskMgr             taskmanager.TaskManager
	runtimeSnapshots    RuntimeSnapshotStore
	runtimeRefreshGroup singleflight.Group
	runtimeInstanceID   string
	reconcileCancel     context.CancelFunc
	reconcileWG         sync.WaitGroup
	gcCancel            context.CancelFunc
	gcWG                sync.WaitGroup
}

var _ InstrumentationManager = (*InstrumentationService)(nil)

func NewInstrumentationService(logger *zap.Logger, config Config, store RuleStore, agentReg agentregistry.AgentRegistry, taskMgr taskmanager.TaskManager) *InstrumentationService {
	return newInstrumentationServiceWithRuntimeSnapshotStore(logger, config, store, agentReg, taskMgr, newMemoryRuntimeSnapshotStore(), newRuntimeSnapshotInstanceID())
}

func newInstrumentationServiceWithRuntimeSnapshotStore(logger *zap.Logger, config Config, store RuleStore, agentReg agentregistry.AgentRegistry, taskMgr taskmanager.TaskManager, runtimeSnapshots RuntimeSnapshotStore, runtimeInstanceID string) *InstrumentationService {
	if runtimeSnapshots == nil {
		runtimeSnapshots = newMemoryRuntimeSnapshotStore()
	}
	if strings.TrimSpace(runtimeInstanceID) == "" {
		runtimeInstanceID = newRuntimeSnapshotInstanceID()
	}
	return &InstrumentationService{
		logger:            logger,
		config:            config,
		store:             store,
		agentReg:          agentReg,
		taskMgr:           taskMgr,
		runtimeSnapshots:  runtimeSnapshots,
		runtimeInstanceID: strings.TrimSpace(runtimeInstanceID),
	}
}

func (s *InstrumentationService) Start(ctx context.Context) error {
	s.logger.Info("Starting instrumentation manager")
	if err := s.store.Start(ctx); err != nil {
		return err
	}
	if err := s.runtimeSnapshots.Start(ctx); err != nil {
		_ = s.store.Close()
		return err
	}
	s.startReconcileWorker()
	s.startGCWorker()
	return nil
}

func (s *InstrumentationService) Close() error {
	s.stopGCWorker()
	s.stopReconcileWorker()
	return errors.Join(s.runtimeSnapshots.Close(), s.store.Close())
}

func (s *InstrumentationService) CreateRule(ctx context.Context, req *CreateRuleRequest) (*Rule, error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}

	ruleID, err := tokenmanager.GenerateID()
	if err != nil {
		return nil, fmt.Errorf("generate rule id: %w", err)
	}
	now := time.Now().UnixMilli()
	rule := &Rule{
		ID:               ruleID,
		Name:             strings.TrimSpace(req.Name),
		Description:      strings.TrimSpace(req.Description),
		AppID:            strings.TrimSpace(req.AppID),
		ServiceName:      strings.TrimSpace(req.ServiceName),
		ScopeType:        normalizeScopeType(req.ScopeType),
		TargetAgentIDs:   cloneStringSlice(req.TargetAgentIDs),
		ClassName:        strings.TrimSpace(req.ClassName),
		MethodName:       strings.TrimSpace(req.MethodName),
		ParameterTypes:   strings.TrimSpace(req.ParameterTypes),
		MethodDescriptor: strings.TrimSpace(req.MethodDescriptor),
		InstrumentType:   req.InstrumentType,
		SpanName:         strings.TrimSpace(req.SpanName),
		CaptureArgs:      strings.TrimSpace(req.CaptureArgs),
		CaptureReturn:    strings.TrimSpace(req.CaptureReturn),
		CaptureMaxLength: normalizeCaptureMaxLength(req.CaptureMaxLength),
		Force:            req.Force,
		DesiredState:     RuleDesiredStateActive,
		CreatedAtMillis:  now,
		UpdatedAtMillis:  now,
		CreatedBy:        strings.TrimSpace(req.CreatedBy),
		UpdatedBy:        strings.TrimSpace(req.CreatedBy),
	}
	if rule.Name == "" {
		rule.Name = defaultRuleName(rule)
	}

	if err := s.store.SaveRule(ctx, rule, true); err != nil {
		return nil, err
	}
	if err := s.dispatchRuleOperation(ctx, rule, OperationTypeApply); err != nil {
		return nil, err
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *InstrumentationService) GetRule(ctx context.Context, ruleID string) (*Rule, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	return s.refreshRule(ctx, rule)
}

func (s *InstrumentationService) ListRules(ctx context.Context, query ListRulesQuery) ([]*Rule, error) {
	rules, err := s.store.ListRules(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]*Rule, 0, len(rules))
	for _, rule := range rules {
		refreshed, refreshErr := s.refreshRule(ctx, rule)
		if refreshErr != nil {
			return nil, refreshErr
		}
		out = append(out, refreshed)
	}
	return out, nil
}

func (s *InstrumentationService) UpdateRule(ctx context.Context, ruleID string, req *UpdateRuleRequest) (*Rule, error) {
	if err := validateUpdateRequest(req); err != nil {
		return nil, err
	}
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.DesiredState == RuleDesiredStateDeleted {
		return nil, errors.New("deleted rule cannot be updated")
	}

	now := time.Now().UnixMilli()
	rule.Name = chooseString(strings.TrimSpace(req.Name), rule.Name)
	rule.Description = strings.TrimSpace(req.Description)
	rule.ScopeType = normalizeScopeType(req.ScopeType)
	rule.TargetAgentIDs = cloneStringSlice(req.TargetAgentIDs)
	rule.ClassName = strings.TrimSpace(req.ClassName)
	rule.MethodName = strings.TrimSpace(req.MethodName)
	rule.ParameterTypes = strings.TrimSpace(req.ParameterTypes)
	rule.MethodDescriptor = strings.TrimSpace(req.MethodDescriptor)
	rule.InstrumentType = req.InstrumentType
	rule.SpanName = strings.TrimSpace(req.SpanName)
	rule.CaptureArgs = strings.TrimSpace(req.CaptureArgs)
	rule.CaptureReturn = strings.TrimSpace(req.CaptureReturn)
	rule.CaptureMaxLength = normalizeCaptureMaxLength(req.CaptureMaxLength)
	rule.Force = req.Force
	rule.UpdatedAtMillis = now
	rule.UpdatedBy = strings.TrimSpace(req.UpdatedBy)
	if rule.Name == "" {
		rule.Name = defaultRuleName(rule)
	}

	if err := s.store.SaveRule(ctx, rule, false); err != nil {
		return nil, err
	}
	if rule.DesiredState == RuleDesiredStateActive {
		if err := s.dispatchRuleOperation(ctx, rule, OperationTypeApply); err != nil {
			return nil, err
		}
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *InstrumentationService) PauseRule(ctx context.Context, ruleID string) (*Rule, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.DesiredState == RuleDesiredStateDeleted {
		return nil, errors.New("deleted rule cannot be paused")
	}
	rule.DesiredState = RuleDesiredStatePaused
	rule.UpdatedAtMillis = time.Now().UnixMilli()
	if err := s.store.SaveRule(ctx, rule, false); err != nil {
		return nil, err
	}
	if err := s.dispatchRuleOperation(ctx, rule, OperationTypeRemove); err != nil {
		return nil, err
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *InstrumentationService) ResumeRule(ctx context.Context, ruleID string) (*Rule, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.DesiredState == RuleDesiredStateDeleted {
		return nil, errors.New("deleted rule cannot be resumed")
	}
	rule.DesiredState = RuleDesiredStateActive
	rule.UpdatedAtMillis = time.Now().UnixMilli()
	if err := s.store.SaveRule(ctx, rule, false); err != nil {
		return nil, err
	}
	if err := s.dispatchRuleOperation(ctx, rule, OperationTypeApply); err != nil {
		return nil, err
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *InstrumentationService) DeleteRule(ctx context.Context, ruleID string) (*Rule, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.DesiredState == RuleDesiredStateDeleted {
		return s.GetRule(ctx, rule.ID)
	}
	rule.DesiredState = RuleDesiredStateDeleted
	rule.UpdatedAtMillis = time.Now().UnixMilli()
	if err := s.store.SaveRule(ctx, rule, false); err != nil {
		return nil, err
	}
	if err := s.dispatchRuleOperation(ctx, rule, OperationTypeRemove); err != nil {
		return nil, err
	}
	return s.GetRule(ctx, rule.ID)
}

func (s *InstrumentationService) ListTargetStatuses(ctx context.Context, ruleID string) ([]*RuleTargetStatus, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	refreshed, err := s.refreshRule(ctx, rule)
	if err != nil {
		return nil, err
	}
	_ = refreshed
	return s.store.ListTargetStatuses(ctx, ruleID)
}

func (s *InstrumentationService) refreshRule(ctx context.Context, rule *Rule) (*Rule, error) {
	if rule == nil {
		return nil, ErrRuleNotFound
	}
	targets, err := s.store.ListTargetStatuses(ctx, rule.ID)
	if err != nil {
		return nil, err
	}
	updated := false
	now := time.Now().UnixMilli()
	for _, target := range targets {
		changed, refreshErr := s.refreshTargetStatus(ctx, rule, target, now)
		if refreshErr != nil {
			return nil, refreshErr
		}
		updated = updated || changed
	}
	summary := summarizeTargets(targets)
	lastOperation := cloneOperation(rule.LastOperation)
	if lastOperation != nil {
		lastOperation.Status = summary.Status
		lastOperation.TotalTargets = summary.TotalTargets
		lastOperation.AppliedTargets = summary.AppliedTargets
		lastOperation.RunningTargets = summary.RunningTargets
		lastOperation.PendingTargets = summary.PendingTargets
		lastOperation.FailedTargets = summary.FailedTargets
		lastOperation.OfflineTargets = summary.OfflineTargets
		lastOperation.ExpiredTargets = summary.ExpiredTargets
		if isTerminalOperationStatus(lastOperation.Status) && lastOperation.CompletedAtMillis == 0 {
			lastOperation.CompletedAtMillis = now
		}
	}
	if !sameRuleSummary(rule.Summary, summary) || !sameOperation(rule.LastOperation, lastOperation) {
		rule.Summary = summary
		rule.LastOperation = lastOperation
		rule.UpdatedAtMillis = now
		updated = true
	}
	if updated {
		if err := s.store.SaveTargetStatuses(ctx, rule.ID, targets); err != nil {
			return nil, err
		}
		if err := s.store.SaveRule(ctx, rule, false); err != nil {
			return nil, err
		}
	}
	return rule, nil
}

func (s *InstrumentationService) refreshTargetStatus(ctx context.Context, rule *Rule, target *RuleTargetStatus, now int64) (bool, error) {
	if target == nil || target.TaskID == "" {
		return false, nil
	}
	// Skip task result refresh for offline/expired targets. Their state is authoritative
	// and should only be changed by reconcileExistingTarget when the agent comes back online.
	// Without this guard, a stale TaskID (from a prior operation before the agent went offline)
	// could cause mapResultStatusToTargetState to incorrectly override the offline state
	// (e.g., mapping a prior SUCCESS to "applied" even though the agent is unreachable).
	if target.State == TargetStateOffline || target.State == TargetStateExpired {
		return false, nil
	}
	currentState := target.State
	currentTaskStatus := target.TaskStatus
	currentError := target.LastErrorMessage

	if result, found, err := s.taskMgr.GetTaskResult(ctx, target.TaskID); err != nil {
		return false, err
	} else if found && result != nil {
		target.TaskStatus = normalizeTaskStatus(result.Status)
		target.State = mapResultStatusToTargetState(result.Status, rule.DesiredState)
		target.LastErrorMessage = strings.TrimSpace(result.ErrorMessage)
		target.UpdatedAtMillis = now
		// Set the monotonic ever_apply_succeeded flag when any target reaches applied state.
		// Once true, this field never reverts to false, providing evidence for future
		// conditional hard-delete fast path decisions.
		if target.State == TargetStateApplied && !rule.EverApplySucceeded {
			rule.EverApplySucceeded = true
		}
		return currentState != target.State || currentTaskStatus != target.TaskStatus || currentError != target.LastErrorMessage, nil
	}

	info, err := s.taskMgr.GetTaskStatus(ctx, target.TaskID)
	if err != nil {
		return false, nil
	}
	if info == nil {
		return false, nil
	}
	target.TaskStatus = normalizeTaskStatus(info.Status)
	target.State = mapInfoStatusToTargetState(info.Status)
	target.UpdatedAtMillis = now
	return currentState != target.State || currentTaskStatus != target.TaskStatus, nil
}

func (s *InstrumentationService) dispatchRuleOperation(ctx context.Context, rule *Rule, operationType OperationType) error {
	now := time.Now().UnixMilli()
	operationID, err := tokenmanager.GenerateID()
	if err != nil {
		return fmt.Errorf("generate operation id: %w", err)
	}

	agents, err := s.resolveTargets(ctx, rule)
	if err != nil {
		return err
	}
	currentTargets, err := s.store.ListTargetStatuses(ctx, rule.ID)
	if err != nil {
		return err
	}
	currentByID := make(map[string]*RuleTargetStatus, len(currentTargets))
	for _, target := range currentTargets {
		if target == nil {
			continue
		}
		currentByID[strings.TrimSpace(target.AgentID)] = target
	}

	targets := make([]*RuleTargetStatus, 0, len(agents))
	audits := make([]*RuleAuditEntry, 0, len(agents))
	dirtyAgentIDs := make([]string, 0, len(agents))
	for _, agent := range agents {
		current := currentByID[strings.TrimSpace(agent.AgentID)]
		status, audit, dirty := s.dispatchOperationToAgent(ctx, rule, operationType, current, agent, now, AuditSourceManual, "manual rule operation")
		if status != nil {
			targets = append(targets, status)
		}
		if audit != nil {
			audits = append(audits, audit)
		}
		if dirty {
			dirtyAgentIDs = append(dirtyAgentIDs, agent.AgentID)
		}
	}

	summary := summarizeTargets(targets)
	rule.LastOperation = &OperationSummary{
		OperationID:       operationID,
		Type:              operationType,
		Status:            summary.Status,
		StartedAtMillis:   now,
		CompletedAtMillis: completedAt(summary.Status, now),
		TotalTargets:      summary.TotalTargets,
		AppliedTargets:    summary.AppliedTargets,
		RunningTargets:    summary.RunningTargets,
		PendingTargets:    summary.PendingTargets,
		FailedTargets:     summary.FailedTargets,
		OfflineTargets:    summary.OfflineTargets,
		ExpiredTargets:    summary.ExpiredTargets,
	}
	rule.Summary = summary
	rule.UpdatedAtMillis = now
	rule.RecentAudits = appendRuleAudits(rule.RecentAudits, audits, s.auditRetention())

	if err := s.store.SaveTargetStatuses(ctx, rule.ID, targets); err != nil {
		return err
	}
	if err := s.store.SaveRule(ctx, rule, false); err != nil {
		return err
	}
	s.markRuntimeSnapshotsDirty(ctx, uniqueStrings(dirtyAgentIDs))
	return nil
}

func (s *InstrumentationService) resolveTargets(ctx context.Context, rule *Rule) ([]*agentregistry.AgentInfo, error) {
	if rule.ScopeType == ScopeTypeInstance && len(rule.TargetAgentIDs) > 0 {
		out := make([]*agentregistry.AgentInfo, 0, len(rule.TargetAgentIDs))
		for _, agentID := range rule.TargetAgentIDs {
			agent, err := s.agentReg.GetAgent(ctx, agentID)
			if err != nil || agent == nil {
				continue
			}
			out = append(out, agent)
		}
		return out, nil
	}
	return s.agentReg.GetInstancesByService(ctx, rule.AppID, rule.ServiceName)
}

func validateCreateRequest(req *CreateRuleRequest) error {
	if req == nil {
		return errors.New("request cannot be nil")
	}
	if strings.TrimSpace(req.AppID) == "" {
		return errors.New("app_id is required")
	}
	if strings.TrimSpace(req.ServiceName) == "" {
		return errors.New("service_name is required")
	}
	if strings.TrimSpace(req.ClassName) == "" {
		return errors.New("class_name is required")
	}
	if strings.TrimSpace(req.MethodName) == "" {
		return errors.New("method_name is required")
	}
	if !isSupportedInstrumentType(req.InstrumentType) {
		return errors.New("instrument_type must be one of trace, metric, log")
	}
	if normalizeScopeType(req.ScopeType) == ScopeTypeInstance && len(req.TargetAgentIDs) == 0 {
		return errors.New("target_agent_ids is required when scope_type is instance")
	}
	return nil
}

func validateUpdateRequest(req *UpdateRuleRequest) error {
	if req == nil {
		return errors.New("request cannot be nil")
	}
	if strings.TrimSpace(req.ClassName) == "" {
		return errors.New("class_name is required")
	}
	if strings.TrimSpace(req.MethodName) == "" {
		return errors.New("method_name is required")
	}
	if !isSupportedInstrumentType(req.InstrumentType) {
		return errors.New("instrument_type must be one of trace, metric, log")
	}
	if normalizeScopeType(req.ScopeType) == ScopeTypeInstance && len(req.TargetAgentIDs) == 0 {
		return errors.New("target_agent_ids is required when scope_type is instance")
	}
	return nil
}

func buildTaskParameters(rule *Rule, operationType OperationType) (json.RawMessage, error) {
	params := map[string]string{
		"rule_id": rule.ID,
	}
	if operationType == OperationTypeApply {
		params["class_name"] = rule.ClassName
		params["method_name"] = rule.MethodName
		params["type"] = string(rule.InstrumentType)
		if rule.ParameterTypes != "" {
			params["parameter_types"] = rule.ParameterTypes
		}
		if rule.MethodDescriptor != "" {
			params["method_descriptor"] = rule.MethodDescriptor
		}
		if rule.SpanName != "" {
			params["span_name"] = rule.SpanName
		}
		if rule.CaptureArgs != "" {
			params["config.capture_args"] = rule.CaptureArgs
		}
		if rule.CaptureReturn != "" {
			params["config.capture_return"] = rule.CaptureReturn
		}
		if rule.CaptureMaxLength > 0 {
			params["config.capture_max_length"] = fmt.Sprintf("%d", rule.CaptureMaxLength)
		}
		if rule.Force {
			params["config.force"] = "true"
		}
	}
	return json.Marshal(params)
}

func summarizeTargets(targets []*RuleTargetStatus) RuleSummary {
	summary := RuleSummary{TotalTargets: len(targets)}
	for _, target := range targets {
		if target == nil {
			continue
		}
		switch target.State {
		case TargetStateApplied, TargetStateRemoved:
			summary.AppliedTargets++
		case TargetStateRunning:
			summary.RunningTargets++
		case TargetStatePending, TargetStateDispatched:
			summary.PendingTargets++
		case TargetStateFailed:
			summary.FailedTargets++
		case TargetStateOffline:
			summary.OfflineTargets++
		case TargetStateExpired:
			summary.ExpiredTargets++
		}
	}
	summary.Status = deriveOperationStatus(summary)
	return summary
}

func deriveOperationStatus(summary RuleSummary) OperationStatus {
	if summary.TotalTargets == 0 {
		return OperationStatusPending
	}
	completedTargets := summary.AppliedTargets + summary.ExpiredTargets
	if summary.FailedTargets > 0 {
		if completedTargets > 0 || summary.RunningTargets > 0 || summary.PendingTargets > 0 {
			return OperationStatusPartialSuccess
		}
		return OperationStatusFailed
	}
	if summary.PendingTargets > 0 || summary.RunningTargets > 0 {
		return OperationStatusRunning
	}
	if completedTargets > 0 {
		return OperationStatusSuccess
	}
	if summary.OfflineTargets > 0 {
		return OperationStatusPending
	}
	return OperationStatusPending
}

func mapResultStatusToTargetState(status model.TaskStatus, desired RuleDesiredState) TargetState {
	switch status {
	case model.TaskStatusSuccess:
		if desired == RuleDesiredStateActive {
			return TargetStateApplied
		}
		return TargetStateRemoved
	case model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled, model.TaskStatusResultTooLarge:
		return TargetStateFailed
	case model.TaskStatusRunning:
		return TargetStateRunning
	default:
		return TargetStatePending
	}
}

func mapInfoStatusToTargetState(status model.TaskStatus) TargetState {
	switch status {
	case model.TaskStatusRunning:
		return TargetStateRunning
	case model.TaskStatusPending, model.TaskStatusUnspecified:
		return TargetStateDispatched
	default:
		return TargetStatePending
	}
}

func normalizeTaskStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusPending:
		return "pending"
	case model.TaskStatusRunning:
		return "running"
	case model.TaskStatusSuccess:
		return "success"
	case model.TaskStatusFailed:
		return "failed"
	case model.TaskStatusTimeout:
		return "timeout"
	case model.TaskStatusCancelled:
		return "cancelled"
	case model.TaskStatusResultTooLarge:
		return "result_too_large"
	default:
		return "unknown"
	}
}

func normalizeScopeType(scope ScopeType) ScopeType {
	if scope == ScopeTypeInstance {
		return ScopeTypeInstance
	}
	return ScopeTypeService
}

func normalizeCaptureMaxLength(v int) int {
	if v <= 0 {
		return 256
	}
	return v
}

func defaultRuleName(rule *Rule) string {
	if rule == nil {
		return ""
	}
	return fmt.Sprintf("%s.%s [%s]", rule.ClassName, rule.MethodName, rule.InstrumentType)
}

func isSupportedInstrumentType(t InstrumentType) bool {
	switch t {
	case InstrumentTypeTrace, InstrumentTypeMetric, InstrumentTypeLog:
		return true
	default:
		return false
	}
}

func desiredStateForOperation(current RuleDesiredState, operationType OperationType) RuleDesiredState {
	if operationType == OperationTypeApply {
		return RuleDesiredStateActive
	}
	if current == RuleDesiredStateDeleted {
		return RuleDesiredStateDeleted
	}
	return RuleDesiredStatePaused
}

func taskTypeForOperation(operationType OperationType) string {
	if operationType == OperationTypeRemove {
		return "dynamic_uninstrument"
	}
	return "dynamic_instrument"
}

func isAgentOnline(agent *agentregistry.AgentInfo) bool {
	return agent != nil && agent.Status != nil && agent.Status.State == agentregistry.AgentStateOnline
}

func isTerminalOperationStatus(status OperationStatus) bool {
	return status == OperationStatusSuccess || status == OperationStatusFailed || status == OperationStatusPartialSuccess
}

func completedAt(status OperationStatus, now int64) int64 {
	if isTerminalOperationStatus(status) {
		return now
	}
	return 0
}

func cloneStringSlice(items []string) []string {
	if items == nil {
		return nil
	}
	return append([]string(nil), items...)
}

func chooseString(newValue, fallback string) string {
	if newValue != "" {
		return newValue
	}
	return fallback
}

func cloneOperation(op *OperationSummary) *OperationSummary {
	if op == nil {
		return nil
	}
	copied := *op
	return &copied
}

func sameRuleSummary(a, b RuleSummary) bool {
	return a == b
}

func sameOperation(a, b *OperationSummary) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
