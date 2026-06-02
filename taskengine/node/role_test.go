// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import "testing"

func TestAllRoles(t *testing.T) {
	roles := AllRoles()
	if len(roles) != 5 {
		t.Errorf("expected 5 roles, got %d", len(roles))
	}
}

func TestIsValidRole(t *testing.T) {
	tests := []struct {
		role  Role
		valid bool
	}{
		{RoleWriter, true},
		{RoleReader, true},
		{RolePurger, true},
		{RoleUI, true},
		{RoleAgent, true},
		{Role("unknown"), false},
		{Role(""), false},
	}

	for _, tt := range tests {
		if got := IsValidRole(tt.role); got != tt.valid {
			t.Errorf("IsValidRole(%q) = %v, want %v", tt.role, got, tt.valid)
		}
	}
}

func TestExpandRoles_Single(t *testing.T) {
	caps := ExpandRoles([]Role{RoleWriter})
	if caps.Len() != 1 {
		t.Errorf("writer should have 1 cap, got %d", caps.Len())
	}
	if !caps.Has(CapStorageWrite) {
		t.Error("writer should have CapStorageWrite")
	}
}

func TestExpandRoles_Multiple(t *testing.T) {
	caps := ExpandRoles([]Role{RoleWriter, RolePurger})

	// Writer: CapStorageWrite
	// Purger: CapStorageRead, CapStorageDelete, CapPurgeExecute, CapPurgePlan
	// Union: 5 unique capabilities
	expected := []Capability{CapStorageWrite, CapStorageRead, CapStorageDelete, CapPurgeExecute, CapPurgePlan}

	if caps.Len() != len(expected) {
		t.Errorf("expected %d caps, got %d (caps: %v)", len(expected), caps.Len(), caps.List())
	}
	for _, c := range expected {
		if !caps.Has(c) {
			t.Errorf("expected cap %s to be present", c)
		}
	}
}

func TestExpandRoles_Empty(t *testing.T) {
	caps := ExpandRoles(nil)
	if caps.Len() != 0 {
		t.Errorf("expected 0 caps for nil roles, got %d", caps.Len())
	}
}

func TestExpandRoles_UnknownRole(t *testing.T) {
	caps := ExpandRoles([]Role{Role("nonexistent")})
	if caps.Len() != 0 {
		t.Errorf("unknown role should expand to 0 caps, got %d", caps.Len())
	}
}

func TestExpandRoles_Agent(t *testing.T) {
	caps := ExpandRoles([]Role{RoleAgent})
	if !caps.Has(CapArthasExec) {
		t.Error("agent should have CapArthasExec")
	}
	if caps.Len() != 1 {
		t.Errorf("agent should have exactly 1 cap, got %d", caps.Len())
	}
}

func TestRoleCapabilities_AllRolesCovered(t *testing.T) {
	for _, role := range AllRoles() {
		caps, ok := RoleCapabilities[role]
		if !ok {
			t.Errorf("role %s not in RoleCapabilities map", role)
		}
		if len(caps) == 0 {
			t.Errorf("role %s has no capabilities", role)
		}
	}
}
