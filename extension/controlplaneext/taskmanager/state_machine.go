// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"fmt"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// StateTransitionResult describes the outcome of a state transition validation.
type StateTransitionResult struct {
	// Allowed indicates whether the transition is permitted.
	Allowed bool
	// Idempotent indicates the transition is a no-op (same state).
	Idempotent bool
	// Conflict indicates a terminal state conflict (e.g., SUCCESS → FAILED).
	Conflict bool
	// Reason provides a human-readable explanation.
	Reason string
}

// isTerminal returns true if the status is a terminal state.
func isTerminal(status model.TaskStatus) bool {
	switch status {
	case model.TaskStatusSuccess, model.TaskStatusFailed,
		model.TaskStatusTimeout, model.TaskStatusCancelled, model.TaskStatusResultTooLarge:
		return true
	default:
		return false
	}
}

// ValidateStateTransition checks if transitioning from currentStatus to newStatus is valid.
//
// State machine rules:
//  1. Terminal states (SUCCESS/FAILED/TIMEOUT/CANCELLED) cannot transition to any other state.
//  2. RUNNING cannot go back to PENDING.
//  3. Same-state transitions are idempotent (allowed, no-op).
//  4. Terminal-to-different-terminal is a conflict (logged, rejected).
//
// State transition diagram:
//
//	                        ┌──────────┐
//	                        │ PENDING  │ (initial state)
//	                        └────┬─────┘
//	                             │
//	                             │ RUNNING (agent starts execution)
//	                             ▼
//	                        ┌──────────┐
//	              ┌─────────│ RUNNING  │─────────┐
//	              │         └──────────┘         │
//	              │              │               │
//	  SUCCESS     │   FAILED     │    TIMEOUT    │   CANCELLED
//	     │        │              │               │      │
//	     ▼        ▼              ▼               ▼      ▼
//	┌─────────┐  ┌─────────┐   ┌─────────┐    ┌───────────────┐
//	│ SUCCESS │  │ FAILED  │   │ TIMEOUT │    │   CANCELLED   │
//	└─────────┘  └─────────┘   └─────────┘    └───────────────┘
//	     │            │              │               │
//	     └────────────┴──────────────┴───────────────┘
//	                         │
//	                  ╔══════╧══════╗
//	                  ║  TERMINAL   ║ (final state, no rollback)
//	                  ╚═════════════╝
func ValidateStateTransition(currentStatus, newStatus model.TaskStatus) StateTransitionResult {
	// Rule 1: Same state = idempotent
	if currentStatus == newStatus {
		return StateTransitionResult{
			Allowed:    true,
			Idempotent: true,
			Reason:     "idempotent: same status",
		}
	}

	// Rule 2: Terminal state cannot transition
	if isTerminal(currentStatus) {
		if isTerminal(newStatus) {
			// Terminal → different Terminal = conflict
			return StateTransitionResult{
				Allowed:  false,
				Conflict: true,
				Reason:   fmt.Sprintf("terminal conflict: %d → %d", currentStatus, newStatus),
			}
		}
		// Terminal → non-Terminal (e.g., SUCCESS → RUNNING) = reject
		return StateTransitionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("cannot transition from terminal state %d to %d", currentStatus, newStatus),
		}
	}

	// Rule 3: RUNNING cannot go back to PENDING
	if currentStatus == model.TaskStatusRunning && newStatus == model.TaskStatusPending {
		return StateTransitionResult{
			Allowed: false,
			Reason:  "cannot transition from RUNNING back to PENDING",
		}
	}

	// All other transitions are allowed
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

// IsStateTransitionError checks if an error is a StateTransitionError.
func IsStateTransitionError(err error) bool {
	_, ok := err.(*StateTransitionError)
	return ok
}

// IsTerminalConflict checks if an error is a terminal state conflict.
func IsTerminalConflict(err error) bool {
	if e, ok := err.(*StateTransitionError); ok {
		return e.IsConflict
	}
	return false
}
