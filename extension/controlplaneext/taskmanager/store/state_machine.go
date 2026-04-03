// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"fmt"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

const (
	taskStatusPendingCode        = int64(model.TaskStatusPending)
	taskStatusRunningCode        = int64(model.TaskStatusRunning)
	taskStatusSuccessCode        = int64(model.TaskStatusSuccess)
	taskStatusFailedCode         = int64(model.TaskStatusFailed)
	taskStatusTimeoutCode        = int64(model.TaskStatusTimeout)
	taskStatusCancelledCode      = int64(model.TaskStatusCancelled)
	taskStatusResultTooLargeCode = int64(model.TaskStatusResultTooLarge)
)

// StateTransitionResult describes the outcome of a state transition validation.
type StateTransitionResult struct {
	// Allowed indicates whether the transition is permitted.
	Allowed bool
	// Idempotent indicates the transition is a no-op (same state).
	Idempotent bool
	// Conflict indicates a terminal state conflict (e.g. SUCCESS → FAILED).
	Conflict bool
	// Reason provides a human-readable explanation.
	Reason string
}

// ValidateStateTransition checks whether transitioning from currentStatus to
// newStatus is valid for the shared task lifecycle model.
func ValidateStateTransition(currentStatus, newStatus model.TaskStatus) StateTransitionResult {
	if currentStatus == newStatus {
		return StateTransitionResult{
			Allowed:    true,
			Idempotent: true,
			Reason:     "idempotent: same status",
		}
	}

	if IsTerminalStatus(currentStatus) {
		if IsTerminalStatus(newStatus) {
			return StateTransitionResult{
				Allowed:  false,
				Conflict: true,
				Reason:   fmt.Sprintf("terminal conflict: %d → %d", currentStatus, newStatus),
			}
		}
		return StateTransitionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("cannot transition from terminal state %d to %d", currentStatus, newStatus),
		}
	}

	if currentStatus == model.TaskStatusRunning && newStatus == model.TaskStatusPending {
		return StateTransitionResult{
			Allowed: false,
			Reason:  "cannot transition from RUNNING back to PENDING",
		}
	}

	return StateTransitionResult{
		Allowed: true,
		Reason:  "allowed",
	}
}

// StateTransitionError represents a rejected state transition.
type StateTransitionError struct {
	TaskID        string
	CurrentStatus model.TaskStatus
	NewStatus     model.TaskStatus
	Reason        string
	IsConflict    bool
}

func (e *StateTransitionError) Error() string {
	if e.IsConflict {
		return fmt.Sprintf("state conflict for task %s: %d → %d (%s)",
			e.TaskID, e.CurrentStatus, e.NewStatus, e.Reason)
	}
	return fmt.Sprintf("invalid state transition for task %s: %d → %d (%s)",
		e.TaskID, e.CurrentStatus, e.NewStatus, e.Reason)
}

// NewStateTransitionError creates a StateTransitionError from a validation result.
func NewStateTransitionError(taskID string, currentStatus, newStatus model.TaskStatus, result StateTransitionResult) *StateTransitionError {
	return &StateTransitionError{
		TaskID:        taskID,
		CurrentStatus: currentStatus,
		NewStatus:     newStatus,
		Reason:        result.Reason,
		IsConflict:    result.Conflict,
	}
}

// IsStateTransitionError reports whether err is a StateTransitionError.
func IsStateTransitionError(err error) bool {
	_, ok := err.(*StateTransitionError)
	return ok
}

// IsTerminalConflict reports whether err is a terminal-state conflict.
func IsTerminalConflict(err error) bool {
	if e, ok := err.(*StateTransitionError); ok {
		return e.IsConflict
	}
	return false
}

func applyTaskResultUpdate(info *TaskInfo, result *model.TaskResult, nowMillis int64) (ApplyTaskUpdateResult, error) {
	if info == nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("task info cannot be nil")
	}
	if result == nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("result cannot be nil")
	}

	cur := info.Status
	newStatus := result.Status

	// Once terminal, everything is a no-op (first terminal wins).
	if IsTerminalStatus(cur) {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}

	transition := ValidateStateTransition(cur, newStatus)
	if transition.Idempotent {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}
	if !transition.Allowed {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	info.Status = newStatus
	info.Result = result
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	if result.AgentID != "" && info.AgentID == "" {
		info.AgentID = result.AgentID
	}

	if newStatus == model.TaskStatusRunning && info.StartedAtMillis == 0 {
		info.StartedAtMillis = nowMillis
	}

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

func applyCancelUpdate(info *TaskInfo, nowMillis int64) (ApplyTaskUpdateResult, error) {
	if info == nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("task info cannot be nil")
	}

	cur := info.Status
	transition := ValidateStateTransition(cur, model.TaskStatusCancelled)
	if transition.Idempotent {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}
	if !transition.Allowed {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	info.Status = model.TaskStatusCancelled
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

func applySetRunningUpdate(info *TaskInfo, agentID string, nowMillis int64) (ApplyTaskUpdateResult, error) {
	if info == nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("task info cannot be nil")
	}

	cur := info.Status
	transition := ValidateStateTransition(cur, model.TaskStatusRunning)
	if transition.Idempotent {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}
	if !transition.Allowed {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	info.Status = model.TaskStatusRunning
	info.AgentID = agentID
	if info.StartedAtMillis == 0 {
		info.StartedAtMillis = nowMillis
	}
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

func luaTerminalStatusExpr(statusVar string) string {
	return fmt.Sprintf(
		"%s == %d or %s == %d or %s == %d or %s == %d or %s == %d",
		statusVar, taskStatusSuccessCode,
		statusVar, taskStatusFailedCode,
		statusVar, taskStatusTimeoutCode,
		statusVar, taskStatusCancelledCode,
		statusVar, taskStatusResultTooLargeCode,
	)
}
