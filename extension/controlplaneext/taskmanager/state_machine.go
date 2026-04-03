// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
)

// StateTransitionResult is kept in the taskmanager package as a compatibility
// alias for callers, while the authoritative rule definition lives in `store`.
type StateTransitionResult = store.StateTransitionResult

// StateTransitionError is kept in the taskmanager package as a compatibility
// alias for callers, while the authoritative error model lives in `store`.
type StateTransitionError = store.StateTransitionError

// isTerminal returns true if the status is a terminal state.
func isTerminal(status model.TaskStatus) bool {
	return store.IsTerminalStatus(status)
}

// ValidateStateTransition delegates to the shared store-level state machine.
func ValidateStateTransition(currentStatus, newStatus model.TaskStatus) StateTransitionResult {
	return store.ValidateStateTransition(currentStatus, newStatus)
}

// NewStateTransitionError creates a StateTransitionError from a validation result.
func NewStateTransitionError(taskID string, currentStatus, newStatus model.TaskStatus, result StateTransitionResult) *StateTransitionError {
	return store.NewStateTransitionError(taskID, currentStatus, newStatus, result)
}

// IsStateTransitionError checks if an error is a StateTransitionError.
func IsStateTransitionError(err error) bool {
	return store.IsStateTransitionError(err)
}

// IsTerminalConflict checks if an error is a terminal state conflict.
func IsTerminalConflict(err error) bool {
	return store.IsTerminalConflict(err)
}
