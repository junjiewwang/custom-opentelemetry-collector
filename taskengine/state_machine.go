// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import "fmt"

// validTransitions defines the allowed state transitions for tasks.
// This is the single source of truth for the task lifecycle state machine.
//
// State machine diagram:
//
//	               ┌─────────────┐
//	               │   Pending   │◄───────────── retry ───────────┐
//	               └──────┬──────┘                                │
//	                      │                                       │
//	          ┌───────────┼───────────┐                           │
//	          │           │           │                           │
//	          ▼           ▼           ▼                           │
//	    ┌──────────┐ ┌─────────┐ ┌──────────┐                    │
//	    │ Running  │ │Cancelled│ │ Timeout  │                    │
//	    └────┬─────┘ └─────────┘ └─────┬────┘                    │
//	         │                         │                          │
//	    ┌────┼─────┬──────────┬────────┤                          │
//	    │    │     │          │        │                          │
//	    ▼    ▼     ▼          ▼        ▼                          │
//	┌───────┐┌──────┐┌───────┐┌────────┐┌──────────┐            │
//	│Success││Failed││Timeout││Skipped ││Cancelled │            │
//	└───────┘└──┬───┘└───┬───┘└────────┘└──────────┘            │
//	             │        │                                       │
//	             └────────┴───────────────────────────────────────┘
//	                     retry (if RetryCount < MaxRetries)
//
var validTransitions = map[TaskStatus][]TaskStatus{
	StatusPending: {StatusRunning, StatusCancelled, StatusTimeout},
	StatusRunning: {StatusSuccess, StatusFailed, StatusTimeout, StatusSkipped, StatusCancelled},
	// Failed and Timeout support retry → Pending transition
	StatusFailed:    {StatusPending},
	StatusTimeout:   {StatusPending},
	// Fully terminal states — no outgoing transitions
	StatusSuccess:   {},
	StatusSkipped:   {},
	StatusCancelled: {},
}

// ValidateTransition checks if transitioning from `from` to `to` is allowed.
// Returns nil if the transition is valid, an error otherwise.
func ValidateTransition(from, to TaskStatus) error {
	// Same state = idempotent no-op (not an error)
	if from == to {
		return nil
	}

	allowed, exists := validTransitions[from]
	if !exists {
		return fmt.Errorf("unknown source status: %q", from)
	}

	for _, valid := range allowed {
		if valid == to {
			return nil
		}
	}

	return &InvalidTransitionError{From: from, To: to}
}

// InvalidTransitionError is returned when a state transition violates the state machine rules.
type InvalidTransitionError struct {
	From TaskStatus
	To   TaskStatus
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid state transition: %s → %s", e.From, e.To)
}

// IsInvalidTransition returns true if err is an InvalidTransitionError.
func IsInvalidTransition(err error) bool {
	_, ok := err.(*InvalidTransitionError)
	return ok
}

// AllowedTransitions returns the list of valid next states from the given status.
func AllowedTransitions(from TaskStatus) []TaskStatus {
	return validTransitions[from]
}
