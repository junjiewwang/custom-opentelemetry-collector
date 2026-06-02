// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import "testing"

func TestValidateTransition_ValidPaths(t *testing.T) {
	validPaths := []struct {
		from TaskStatus
		to   TaskStatus
	}{
		{StatusPending, StatusRunning},
		{StatusPending, StatusCancelled},
		{StatusPending, StatusTimeout},
		{StatusRunning, StatusSuccess},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusTimeout},
		{StatusRunning, StatusSkipped},
		{StatusRunning, StatusCancelled},
	}

	for _, p := range validPaths {
		if err := ValidateTransition(p.from, p.to); err != nil {
			t.Errorf("expected valid transition %s → %s, got error: %v", p.from, p.to, err)
		}
	}
}

func TestValidateTransition_InvalidPaths(t *testing.T) {
	invalidPaths := []struct {
		from TaskStatus
		to   TaskStatus
	}{
		// Cannot go backwards
		{StatusRunning, StatusPending},
		// Cannot transition from terminal states
		{StatusSuccess, StatusRunning},
		{StatusSuccess, StatusFailed},
		{StatusFailed, StatusRunning},
		{StatusFailed, StatusSuccess},
		{StatusTimeout, StatusRunning},
		{StatusSkipped, StatusRunning},
		{StatusCancelled, StatusRunning},
		// Cannot skip steps
		{StatusPending, StatusSuccess},
		{StatusPending, StatusFailed},
		{StatusPending, StatusSkipped},
	}

	for _, p := range invalidPaths {
		err := ValidateTransition(p.from, p.to)
		if err == nil {
			t.Errorf("expected invalid transition %s → %s to fail", p.from, p.to)
		}
		if !IsInvalidTransition(err) {
			t.Errorf("expected InvalidTransitionError for %s → %s, got: %T", p.from, p.to, err)
		}
	}
}

func TestValidateTransition_SameState_Idempotent(t *testing.T) {
	states := []TaskStatus{StatusPending, StatusRunning, StatusSuccess, StatusFailed}
	for _, s := range states {
		if err := ValidateTransition(s, s); err != nil {
			t.Errorf("same state transition %s → %s should be idempotent, got: %v", s, s, err)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []TaskStatus{StatusSuccess, StatusFailed, StatusTimeout, StatusSkipped, StatusCancelled}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}

	nonTerminal := []TaskStatus{StatusPending, StatusRunning}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}

func TestAllowedTransitions(t *testing.T) {
	// Pending can go to Running, Cancelled, Timeout
	allowed := AllowedTransitions(StatusPending)
	if len(allowed) != 3 {
		t.Errorf("expected 3 transitions from Pending, got %d", len(allowed))
	}

	// Terminal states have no outgoing transitions
	allowed = AllowedTransitions(StatusSuccess)
	if len(allowed) != 0 {
		t.Errorf("expected 0 transitions from Success, got %d", len(allowed))
	}
}
