// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/tokenmanager"
)

const runtimeSnapshotTaskType = "dynamic_instrument_list"

type agentRuntimeSnapshotCacheEntry struct {
	AgentID             string                       `json:"agent_id"`
	Summary             dynamicInstrumentListSummary `json:"summary"`
	Items               []dynamicInstrumentListItem  `json:"items,omitempty"`
	HasPayload          bool                         `json:"has_payload"`
	SourceTaskID        string                       `json:"source_task_id,omitempty"`
	RefreshedAtMillis   int64                        `json:"refreshed_at_millis,omitempty"`
	ExpiresAtMillis     int64                        `json:"expires_at_millis,omitempty"`
	Dirty               bool                         `json:"dirty"`
	LastRefreshStatus   RuntimeRefreshStatus         `json:"last_refresh_status,omitempty"`
	LastErrorMessage    string                       `json:"last_error_message,omitempty"`
	LastRequestedAtMil  int64                        `json:"last_requested_at_millis,omitempty"`
	UpdatedAtMillis     int64                        `json:"updated_at_millis,omitempty"`
	OwnerInstanceID     string                       `json:"owner_instance_id,omitempty"`
	LocalSyncedAtMillis int64                        `json:"-"`
}

type dynamicInstrumentListResponse struct {
	Summary dynamicInstrumentListSummary `json:"summary"`
	Items   []dynamicInstrumentListItem  `json:"items"`
}

type dynamicInstrumentListSummary struct {
	RegisteredTotal          int    `json:"registered_total,omitempty"`
	Total                    int    `json:"total,omitempty"`
	Pending                  int    `json:"pending,omitempty"`
	Active                   int    `json:"active,omitempty"`
	Reverting                int    `json:"reverting,omitempty"`
	Reverted                 int    `json:"reverted,omitempty"`
	Failed                   int    `json:"failed,omitempty"`
	Effective                int    `json:"effective,omitempty"`
	ActiveTransformerCount   int    `json:"active_transformer_count,omitempty"`
	InstrumentationAvailable bool   `json:"instrumentation_available"`
	EnhancementCapability    bool   `json:"enhancement_capability"`
	SupportsRetransform      bool   `json:"supports_retransform,omitempty"`
	SupportsRedefine         bool   `json:"supports_redefine,omitempty"`
	InstrumentationSource    string `json:"instrumentation_source,omitempty"`
	DiagnosticMessage        string `json:"diagnostic_message,omitempty"`
}

type dynamicInstrumentListItem struct {
	RuleID        string `json:"rule_id"`
	ClassName     string `json:"class_name,omitempty"`
	MethodName    string `json:"method_name,omitempty"`
	Type          string `json:"type,omitempty"`
	Status        string `json:"status,omitempty"`
	RuntimeStatus string `json:"runtime_status,omitempty"`
	IsApplied     bool   `json:"is_applied"`
	IsEffective   bool   `json:"is_effective"`
	ErrorMessage  string `json:"error_message,omitempty"`
	EnhancedClass string `json:"enhanced_class_name,omitempty"`
}

func (s *InstrumentationService) GetRuleRuntimeSnapshot(ctx context.Context, ruleID string) (*RuleRuntimeSnapshot, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	refreshedRule, err := s.refreshRule(ctx, rule)
	if err != nil {
		return nil, err
	}
	return s.buildRuleRuntimeSnapshot(ctx, refreshedRule, false)
}

func (s *InstrumentationService) RefreshRuleRuntimeSnapshot(ctx context.Context, ruleID string) (*RuleRuntimeSnapshot, error) {
	rule, err := s.store.GetRule(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	refreshedRule, err := s.refreshRule(ctx, rule)
	if err != nil {
		return nil, err
	}
	return s.buildRuleRuntimeSnapshot(ctx, refreshedRule, true)
}

func (s *InstrumentationService) buildRuleRuntimeSnapshot(ctx context.Context, rule *Rule, force bool) (*RuleRuntimeSnapshot, error) {
	if rule == nil {
		return nil, ErrRuleNotFound
	}
	targets, err := s.store.ListTargetStatuses(ctx, rule.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	out := make([]*RuleRuntimeSnapshotTarget, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, targetStatus *RuleTargetStatus) {
			defer wg.Done()
			out[idx] = s.buildRuleRuntimeSnapshotTarget(ctx, rule, targetStatus, force, now)
		}(i, target)
	}
	wg.Wait()

	snapshot := &RuleRuntimeSnapshot{
		RuleID:            rule.ID,
		DesiredState:      rule.DesiredState,
		GeneratedAtMillis: now,
		Targets:           compactRuleRuntimeSnapshotTargets(out),
	}
	snapshot.Summary = summarizeRuleRuntimeSnapshotTargets(snapshot.Targets)
	return snapshot, nil
}

func (s *InstrumentationService) buildRuleRuntimeSnapshotTarget(ctx context.Context, rule *Rule, target *RuleTargetStatus, force bool, now int64) *RuleRuntimeSnapshotTarget {
	if rule == nil || target == nil {
		return nil
	}

	out := &RuleRuntimeSnapshotTarget{
		RuleID:                 rule.ID,
		AgentID:                target.AgentID,
		Hostname:               target.Hostname,
		IP:                     target.IP,
		DesiredState:           rule.DesiredState,
		ControlplaneState:      target.State,
		ControlplaneTaskStatus: strings.TrimSpace(target.TaskStatus),
		LastRefreshStatus:      RuntimeRefreshStatusIdle,
		IsStale:                true,
	}

	agent, _ := s.agentReg.GetAgent(ctx, target.AgentID)
	if agent != nil {
		out.Hostname = chooseString(strings.TrimSpace(agent.Hostname), out.Hostname)
		out.IP = chooseString(strings.TrimSpace(agent.IP), out.IP)
	}

	entry := s.ensureAgentRuntimeSnapshot(ctx, agent, target.AgentID, force)
	if entry == nil {
		if agent == nil || !isAgentOnline(agent) {
			out.LastRefreshStatus = RuntimeRefreshStatusSkipped
			out.LastErrorMessage = "agent is offline"
		}
		out.Dirty = true
		return finalizeRuleRuntimeSnapshotTarget(rule, out, nil, now)
	}

	out.SnapshotAvailable = entry.HasPayload
	out.InstrumentationAvailable = entry.Summary.InstrumentationAvailable
	out.EnhancementCapability = entry.Summary.EnhancementCapability
	out.ActiveTransformerCount = entry.Summary.ActiveTransformerCount
	out.DiagnosticMessage = strings.TrimSpace(entry.Summary.DiagnosticMessage)
	out.InstrumentationSource = strings.TrimSpace(entry.Summary.InstrumentationSource)
	out.RefreshedAtMillis = entry.RefreshedAtMillis
	out.ExpiresAtMillis = entry.ExpiresAtMillis
	out.Dirty = entry.Dirty
	out.LastRefreshStatus = entry.LastRefreshStatus
	out.LastErrorMessage = strings.TrimSpace(entry.LastErrorMessage)
	out.IsStale = entry.Dirty || entry.ExpiresAtMillis <= now || entry.LastRefreshStatus != RuntimeRefreshStatusSuccess

	if item := findRuntimeSnapshotItem(entry.Items, rule.ID); item != nil {
		out.RuntimeFound = true
		out.RuntimeStatus = chooseString(strings.TrimSpace(item.RuntimeStatus), strings.TrimSpace(item.Status))
		out.IsApplied = item.IsApplied
		out.IsEffective = item.IsEffective
		if out.LastErrorMessage == "" {
			out.LastErrorMessage = strings.TrimSpace(item.ErrorMessage)
		}
	}

	return finalizeRuleRuntimeSnapshotTarget(rule, out, findRuntimeSnapshotItem(entry.Items, rule.ID), now)
}

func finalizeRuleRuntimeSnapshotTarget(rule *Rule, target *RuleRuntimeSnapshotTarget, item *dynamicInstrumentListItem, now int64) *RuleRuntimeSnapshotTarget {
	if target == nil || rule == nil {
		return target
	}
	if target.ExpiresAtMillis > 0 && target.ExpiresAtMillis <= now {
		target.IsStale = true
	}
	target.DriftReasons = detectRuntimeDrift(rule, target, item)
	return target
}

func (s *InstrumentationService) ensureAgentRuntimeSnapshot(ctx context.Context, agent *agentregistry.AgentInfo, agentID string, force bool) *agentRuntimeSnapshotCacheEntry {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}

	now := time.Now().UnixMilli()
	cached, err := s.runtimeSnapshots.Get(ctx, agentID)
	if err != nil {
		s.logger.Warn("Read runtime snapshot cache failed",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
	}
	if !force && !shouldRefreshRuntimeSnapshot(cached, now) {
		return cached
	}

	value, _, _ := s.runtimeRefreshGroup.Do(agentID, func() (any, error) {
		latest, err := s.runtimeSnapshots.Get(ctx, agentID)
		if err != nil {
			s.logger.Warn("Refresh path read runtime snapshot cache failed",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
		if !force && !shouldRefreshRuntimeSnapshot(latest, time.Now().UnixMilli()) {
			return latest, nil
		}

		if agent == nil || !isAgentOnline(agent) {
			return s.recordSkippedRuntimeSnapshot(ctx, agentID, "agent is offline"), nil
		}

		acquired, leaseErr := s.runtimeSnapshots.TryAcquireRefreshLease(ctx, agentID, s.runtimeInstanceID, s.runtimeSnapshotLeaseTTL())
		if leaseErr != nil {
			s.logger.Warn("Acquire runtime snapshot refresh lease failed",
				zap.String("agent_id", agentID),
				zap.Error(leaseErr),
			)
			acquired = true
		}
		if !acquired {
			waited := s.waitForSharedRuntimeSnapshot(ctx, agentID)
			if waited != nil && !shouldRefreshRuntimeSnapshot(waited, time.Now().UnixMilli()) {
				return waited, nil
			}
			acquired, leaseErr = s.runtimeSnapshots.TryAcquireRefreshLease(ctx, agentID, s.runtimeInstanceID, s.runtimeSnapshotLeaseTTL())
			if leaseErr != nil {
				s.logger.Warn("Re-acquire runtime snapshot refresh lease failed",
					zap.String("agent_id", agentID),
					zap.Error(leaseErr),
				)
				return waited, nil
			}
			if !acquired {
				return waited, nil
			}
		}
		return s.queryAgentRuntimeSnapshot(ctx, agent), nil
	})

	entry, _ := value.(*agentRuntimeSnapshotCacheEntry)
	return cloneAgentRuntimeSnapshotCacheEntry(entry)
}

func (s *InstrumentationService) queryAgentRuntimeSnapshot(ctx context.Context, agent *agentregistry.AgentInfo) *agentRuntimeSnapshotCacheEntry {
	if agent == nil {
		return nil
	}
	requestedAt := time.Now().UnixMilli()
	taskID, err := tokenmanager.GenerateID()
	if err != nil {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, "", RuntimeRefreshStatusFailed, fmt.Sprintf("generate snapshot task id: %v", err), requestedAt, requestedAt)
	}

	params, err := buildRuntimeSnapshotQueryParameters()
	if err != nil {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, RuntimeRefreshStatusFailed, fmt.Sprintf("marshal snapshot query parameters: %v", err), requestedAt, requestedAt)
	}

	task := &model.Task{
		ID:              taskID,
		TypeName:        runtimeSnapshotTaskType,
		ParametersJSON:  params,
		TargetAgentID:   agent.AgentID,
		TimeoutMillis:   s.runtimeSnapshotQueryTimeoutMillis(),
		CreatedAtMillis: requestedAt,
	}
	agentMeta := &taskmanager.AgentMeta{AgentID: agent.AgentID, AppID: agent.AppID, ServiceName: agent.ServiceName}
	if err := s.taskMgr.SubmitTaskForAgent(ctx, agentMeta, task); err != nil {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, RuntimeRefreshStatusFailed, fmt.Sprintf("submit runtime snapshot task: %v", err), requestedAt, time.Now().UnixMilli())
	}

	result := s.waitForRuntimeSnapshotResult(ctx, agent.AgentID, taskID)
	if result == nil {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, RuntimeRefreshStatusTimeout, "runtime snapshot task returned no result", requestedAt, time.Now().UnixMilli())
	}

	refreshStatus := runtimeRefreshStatusFromTaskStatus(result.Status)
	if refreshStatus != RuntimeRefreshStatusSuccess {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, refreshStatus, taskResultErrorMessage(result), requestedAt, time.Now().UnixMilli())
	}
	if len(result.ResultJSON) == 0 {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, RuntimeRefreshStatusFailed, "runtime snapshot task result_json is empty", requestedAt, time.Now().UnixMilli())
	}

	var payload dynamicInstrumentListResponse
	if err := json.Unmarshal(result.ResultJSON, &payload); err != nil {
		return s.recordFailedRuntimeSnapshot(ctx, agent.AgentID, taskID, RuntimeRefreshStatusFailed, fmt.Sprintf("parse runtime snapshot result_json: %v", err), requestedAt, time.Now().UnixMilli())
	}

	finishedAt := time.Now().UnixMilli()
	entry := &agentRuntimeSnapshotCacheEntry{
		AgentID:            agent.AgentID,
		Summary:            payload.Summary,
		Items:              cloneDynamicInstrumentListItems(payload.Items),
		HasPayload:         true,
		SourceTaskID:       taskID,
		RefreshedAtMillis:  finishedAt,
		ExpiresAtMillis:    finishedAt + s.runtimeSnapshotTTL().Milliseconds(),
		Dirty:              false,
		LastRefreshStatus:  RuntimeRefreshStatusSuccess,
		LastErrorMessage:   "",
		LastRequestedAtMil: requestedAt,
		UpdatedAtMillis:    finishedAt,
		OwnerInstanceID:    s.runtimeInstanceID,
	}
	return s.saveSuccessfulRuntimeSnapshot(ctx, entry)
}

func (s *InstrumentationService) waitForRuntimeSnapshotResult(ctx context.Context, agentID, taskID string) *model.TaskResult {
	waitCtx, cancel := context.WithTimeout(ctx, s.runtimeSnapshotQueryTimeout())
	defer cancel()

	ticker := time.NewTicker(s.runtimeSnapshotPollInterval())
	defer ticker.Stop()

	for {
		if result, found, err := s.taskMgr.GetTaskResult(waitCtx, taskID); err == nil && found && result != nil {
			return result
		}

		if info, err := s.taskMgr.GetTaskStatus(waitCtx, taskID); err == nil && info != nil && isTerminalTaskStatus(info.Status) {
			if info.Result != nil {
				return info.Result
			}
			if result, found, err := s.taskMgr.GetTaskResult(waitCtx, taskID); err == nil && found && result != nil {
				return result
			}
			return &model.TaskResult{
				TaskID:       taskID,
				AgentID:      agentID,
				Status:       info.Status,
				ErrorMessage: taskInfoErrorMessage(info),
			}
		}

		select {
		case <-waitCtx.Done():
			return &model.TaskResult{
				TaskID:       taskID,
				AgentID:      agentID,
				Status:       model.TaskStatusTimeout,
				ErrorMessage: "runtime snapshot query timed out",
			}
		case <-ticker.C:
		}
	}
}

func (s *InstrumentationService) recordFailedRuntimeSnapshot(ctx context.Context, agentID, taskID string, status RuntimeRefreshStatus, message string, requestedAt, completedAt int64) *agentRuntimeSnapshotCacheEntry {
	fallback := &agentRuntimeSnapshotCacheEntry{
		AgentID:            agentID,
		SourceTaskID:       strings.TrimSpace(taskID),
		ExpiresAtMillis:    completedAt + s.runtimeSnapshotFailureTTL().Milliseconds(),
		Dirty:              false,
		LastRefreshStatus:  status,
		LastErrorMessage:   strings.TrimSpace(message),
		LastRequestedAtMil: requestedAt,
		UpdatedAtMillis:    completedAt,
		OwnerInstanceID:    s.runtimeInstanceID,
	}
	entry, err := s.runtimeSnapshots.Upsert(ctx, agentID, func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		next := current
		if next == nil {
			next = &agentRuntimeSnapshotCacheEntry{AgentID: agentID}
		}
		next.SourceTaskID = chooseString(strings.TrimSpace(taskID), next.SourceTaskID)
		next.ExpiresAtMillis = completedAt + s.runtimeSnapshotFailureTTL().Milliseconds()
		next.Dirty = current != nil && current.Dirty && current.UpdatedAtMillis > requestedAt
		next.LastRefreshStatus = status
		next.LastErrorMessage = strings.TrimSpace(message)
		next.LastRequestedAtMil = requestedAt
		next.UpdatedAtMillis = completedAt
		next.OwnerInstanceID = s.runtimeInstanceID
		return next
	})
	if err != nil {
		s.logger.Warn("Persist failed runtime snapshot state failed",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
		return fallback
	}
	return entry
}

func (s *InstrumentationService) recordSkippedRuntimeSnapshot(ctx context.Context, agentID, message string) *agentRuntimeSnapshotCacheEntry {
	now := time.Now().UnixMilli()
	fallback := &agentRuntimeSnapshotCacheEntry{
		AgentID:            agentID,
		ExpiresAtMillis:    now + s.runtimeSnapshotFailureTTL().Milliseconds(),
		Dirty:              false,
		LastRefreshStatus:  RuntimeRefreshStatusSkipped,
		LastErrorMessage:   strings.TrimSpace(message),
		LastRequestedAtMil: now,
		UpdatedAtMillis:    now,
		OwnerInstanceID:    s.runtimeInstanceID,
	}
	entry, err := s.runtimeSnapshots.Upsert(ctx, agentID, func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		next := current
		if next == nil {
			next = &agentRuntimeSnapshotCacheEntry{AgentID: agentID}
		}
		next.ExpiresAtMillis = now + s.runtimeSnapshotFailureTTL().Milliseconds()
		next.Dirty = current != nil && current.Dirty
		next.LastRefreshStatus = RuntimeRefreshStatusSkipped
		next.LastErrorMessage = strings.TrimSpace(message)
		next.LastRequestedAtMil = now
		next.UpdatedAtMillis = now
		next.OwnerInstanceID = s.runtimeInstanceID
		return next
	})
	if err != nil {
		s.logger.Warn("Persist skipped runtime snapshot state failed",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
		return fallback
	}
	return entry
}

func (s *InstrumentationService) saveSuccessfulRuntimeSnapshot(ctx context.Context, entry *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
	if entry == nil || strings.TrimSpace(entry.AgentID) == "" {
		return nil
	}
	requestedAt := entry.LastRequestedAtMil
	fallback := cloneAgentRuntimeSnapshotCacheEntry(entry)
	merged, err := s.runtimeSnapshots.Upsert(ctx, entry.AgentID, func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		next := cloneAgentRuntimeSnapshotCacheEntry(entry)
		next.OwnerInstanceID = s.runtimeInstanceID
		if current != nil && current.Dirty && current.UpdatedAtMillis > requestedAt {
			next.Dirty = true
			if current.ExpiresAtMillis > 0 {
				next.ExpiresAtMillis = current.ExpiresAtMillis
			} else {
				next.ExpiresAtMillis = time.Now().UnixMilli()
			}
		}
		return next
	})
	if err != nil {
		s.logger.Warn("Persist successful runtime snapshot failed",
			zap.String("agent_id", entry.AgentID),
			zap.Error(err),
		)
		return fallback
	}
	return merged
}

func (s *InstrumentationService) markRuntimeSnapshotsDirty(ctx context.Context, agentIDs []string) {
	agentIDs = uniqueStrings(agentIDs)
	if len(agentIDs) == 0 {
		return
	}
	if err := s.runtimeSnapshots.MarkDirty(ctx, agentIDs); err != nil {
		s.logger.Warn("Mark runtime snapshots dirty failed",
			zap.Strings("agent_ids", agentIDs),
			zap.Error(err),
		)
	}
}

func (s *InstrumentationService) runtimeSnapshotTTL() time.Duration {
	if s.config.RuntimeSnapshotTTL <= 0 {
		return 20 * time.Second
	}
	return time.Duration(s.config.RuntimeSnapshotTTL) * time.Millisecond
}

func (s *InstrumentationService) runtimeSnapshotFailureTTL() time.Duration {
	ttl := s.runtimeSnapshotTTL() / 4
	if ttl < time.Second {
		return time.Second
	}
	if ttl > 5*time.Second {
		return 5 * time.Second
	}
	return ttl
}

func (s *InstrumentationService) runtimeSnapshotQueryTimeout() time.Duration {
	if s.config.RuntimeSnapshotQueryTimeout <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.config.RuntimeSnapshotQueryTimeout) * time.Millisecond
}

func (s *InstrumentationService) runtimeSnapshotQueryTimeoutMillis() int64 {
	return s.runtimeSnapshotQueryTimeout().Milliseconds()
}

func (s *InstrumentationService) runtimeSnapshotPollInterval() time.Duration {
	if s.config.RuntimeSnapshotPollInterval <= 0 {
		return 200 * time.Millisecond
	}
	return time.Duration(s.config.RuntimeSnapshotPollInterval) * time.Millisecond
}

func (s *InstrumentationService) runtimeSnapshotLeaseTTL() time.Duration {
	return runtimeSnapshotLeaseTTLFromConfig(s.config)
}

func (s *InstrumentationService) runtimeSnapshotLeaseWaitTimeout() time.Duration {
	wait := s.runtimeSnapshotLeaseTTL()
	queryTimeout := s.runtimeSnapshotQueryTimeout()
	if queryTimeout > 0 && wait > queryTimeout {
		wait = queryTimeout
	}
	if wait < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	return wait
}

func (s *InstrumentationService) waitForSharedRuntimeSnapshot(ctx context.Context, agentID string) *agentRuntimeSnapshotCacheEntry {
	waitCtx, cancel := context.WithTimeout(ctx, s.runtimeSnapshotLeaseWaitTimeout())
	defer cancel()

	poll := s.runtimeSnapshotPollInterval()
	if poll <= 0 || poll > 100*time.Millisecond {
		poll = 100 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var last *agentRuntimeSnapshotCacheEntry
	for {
		entry, err := s.runtimeSnapshots.Get(waitCtx, agentID)
		if err == nil {
			last = entry
			if !shouldRefreshRuntimeSnapshot(entry, time.Now().UnixMilli()) {
				return entry
			}
		} else {
			s.logger.Warn("Poll shared runtime snapshot failed while waiting for lease owner",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}

		select {
		case <-waitCtx.Done():
			return last
		case <-ticker.C:
		}
	}
}

func buildRuntimeSnapshotQueryParameters() (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"include_config": false,
		"limit":          500,
		"offset":         0,
	})
}

func shouldRefreshRuntimeSnapshot(entry *agentRuntimeSnapshotCacheEntry, now int64) bool {
	if entry == nil {
		return true
	}
	if entry.Dirty {
		return true
	}
	return entry.ExpiresAtMillis <= now
}

func summarizeRuleRuntimeSnapshotTargets(targets []*RuleRuntimeSnapshotTarget) RuleRuntimeSnapshotSummary {
	summary := RuleRuntimeSnapshotSummary{TotalTargets: len(targets)}
	for _, target := range targets {
		if target == nil {
			continue
		}
		// Classify target as offline (unreachable) vs reachable.
		// A target is considered offline if its controlplane state is offline/expired
		// OR if it was skipped due to agent being offline.
		isOffline := target.ControlplaneState == TargetStateOffline ||
			target.ControlplaneState == TargetStateExpired ||
			(target.LastRefreshStatus == RuntimeRefreshStatusSkipped && target.LastErrorMessage == "agent is offline")
		if isOffline {
			summary.OfflineTargets++
		} else {
			summary.ReachableTargets++
		}

		if target.SnapshotAvailable {
			summary.SnapshotAvailableTargets++
		}
		if target.RuntimeFound {
			summary.RuntimeFoundTargets++
		}
		// Only count effective/drifted/missing for reachable targets to avoid
		// stale cache data from offline agents inflating metrics.
		if !isOffline {
			if target.IsEffective {
				summary.EffectiveTargets++
			}
			if containsRuntimeDriftReason(target.DriftReasons, RuntimeDriftReasonMissing) {
				summary.MissingTargets++
			}
			if len(target.DriftReasons) > 0 {
				summary.DriftedTargets++
			}
		}
		if target.IsStale {
			summary.StaleTargets++
		}
		if target.LastRefreshStatus == RuntimeRefreshStatusFailed || target.LastRefreshStatus == RuntimeRefreshStatusTimeout {
			summary.RefreshFailedTargets++
		}
		if target.SnapshotAvailable && !target.InstrumentationAvailable {
			summary.InstrumentationUnavailableTargets++
		}
		if target.SnapshotAvailable && !target.EnhancementCapability {
			summary.EnhancementUnavailableTargets++
		}
	}
	return summary
}

func detectRuntimeDrift(rule *Rule, target *RuleRuntimeSnapshotTarget, item *dynamicInstrumentListItem) []RuntimeDriftReason {
	if rule == nil || target == nil {
		return nil
	}
	var reasons []RuntimeDriftReason
	if target.SnapshotAvailable && !target.InstrumentationAvailable {
		reasons = append(reasons, RuntimeDriftReasonInstrumentationUnavailable)
	}
	if target.SnapshotAvailable && !target.EnhancementCapability {
		reasons = append(reasons, RuntimeDriftReasonEnhancementUnavailable)
	}

	switch rule.DesiredState {
	case RuleDesiredStateActive:
		if target.SnapshotAvailable && !target.RuntimeFound {
			reasons = append(reasons, RuntimeDriftReasonMissing)
		} else if target.RuntimeFound && !target.IsEffective {
			reasons = append(reasons, RuntimeDriftReasonIneffective)
		}
	case RuleDesiredStatePaused:
		if isRuntimeResidual(item) {
			reasons = append(reasons, RuntimeDriftReasonPausedResidual)
		}
	case RuleDesiredStateDeleted:
		if isRuntimeResidual(item) {
			reasons = append(reasons, RuntimeDriftReasonDeletedResidual)
		}
	}
	return uniqueRuntimeDriftReasons(reasons)
}

func isRuntimeResidual(item *dynamicInstrumentListItem) bool {
	if item == nil {
		return false
	}
	if item.IsApplied || item.IsEffective {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(chooseString(item.RuntimeStatus, item.Status)))
	switch status {
	case "", "reverted", "removed", "inactive":
		return false
	default:
		return true
	}
}

func uniqueRuntimeDriftReasons(items []RuntimeDriftReason) []RuntimeDriftReason {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[RuntimeDriftReason]struct{}, len(items))
	out := make([]RuntimeDriftReason, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func containsRuntimeDriftReason(items []RuntimeDriftReason, expected RuntimeDriftReason) bool {
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}

func findRuntimeSnapshotItem(items []dynamicInstrumentListItem, ruleID string) *dynamicInstrumentListItem {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return nil
	}
	for _, item := range items {
		if strings.TrimSpace(item.RuleID) == ruleID {
			copied := item
			return &copied
		}
	}
	return nil
}

func cloneAgentRuntimeSnapshotCacheEntry(entry *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
	if entry == nil {
		return nil
	}
	copied := *entry
	copied.Items = cloneDynamicInstrumentListItems(entry.Items)
	return &copied
}

func cloneDynamicInstrumentListItems(items []dynamicInstrumentListItem) []dynamicInstrumentListItem {
	if items == nil {
		return nil
	}
	return append([]dynamicInstrumentListItem(nil), items...)
}

func compactRuleRuntimeSnapshotTargets(items []*RuleRuntimeSnapshotTarget) []*RuleRuntimeSnapshotTarget {
	out := make([]*RuleRuntimeSnapshotTarget, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, item)
		}
	}
	return out
}

func runtimeRefreshStatusFromTaskStatus(status model.TaskStatus) RuntimeRefreshStatus {
	switch status {
	case model.TaskStatusSuccess:
		return RuntimeRefreshStatusSuccess
	case model.TaskStatusTimeout:
		return RuntimeRefreshStatusTimeout
	case model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusResultTooLarge:
		return RuntimeRefreshStatusFailed
	default:
		return RuntimeRefreshStatusFailed
	}
}

func isTerminalTaskStatus(status model.TaskStatus) bool {
	switch status {
	case model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled, model.TaskStatusResultTooLarge:
		return true
	default:
		return false
	}
}

func taskResultErrorMessage(result *model.TaskResult) string {
	if result == nil {
		return "runtime snapshot task returned nil result"
	}
	if msg := strings.TrimSpace(result.ErrorMessage); msg != "" {
		return msg
	}
	return fmt.Sprintf("runtime snapshot task finished with status %s", normalizeTaskStatus(result.Status))
}

func taskInfoErrorMessage(info *taskmanager.TaskInfo) string {
	if info == nil {
		return "runtime snapshot task info is unavailable"
	}
	if info.Result != nil {
		return taskResultErrorMessage(info.Result)
	}
	return fmt.Sprintf("runtime snapshot task finished with status %s", normalizeTaskStatus(info.Status))
}

