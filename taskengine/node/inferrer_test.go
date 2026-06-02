// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import "testing"

func TestInferRoles_FromComponents(t *testing.T) {
	tests := []struct {
		name       string
		components InferredComponents
		expected   []Role
	}{
		{
			name:       "storage provider → writer + reader",
			components: InferredComponents{HasStorageProvider: true},
			expected:   []Role{RoleWriter, RoleReader},
		},
		{
			name:       "purger → purger role",
			components: InferredComponents{HasPurger: true},
			expected:   []Role{RolePurger},
		},
		{
			name:       "admin ext → UI role",
			components: InferredComponents{HasAdminExt: true},
			expected:   []Role{RoleUI},
		},
		{
			name: "full stack → writer + reader + purger + UI",
			components: InferredComponents{
				HasStorageProvider: true,
				HasPurger:          true,
				HasAdminExt:        true,
			},
			expected: []Role{RoleWriter, RoleReader, RolePurger, RoleUI},
		},
		{
			name:       "nothing loaded but has storage → fallback writer + reader",
			components: InferredComponents{HasStorageProvider: true},
			expected:   []Role{RoleWriter, RoleReader},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roles := InferRoles(tt.components, nil)
			if len(roles) != len(tt.expected) {
				t.Fatalf("expected %d roles, got %d: %v", len(tt.expected), len(roles), roles)
			}
			for i, role := range roles {
				if role != tt.expected[i] {
					t.Errorf("role[%d] = %s, want %s", i, role, tt.expected[i])
				}
			}
		})
	}
}

func TestInferRoles_ConfiguredOverride(t *testing.T) {
	components := InferredComponents{
		HasStorageProvider: true,
		HasPurger:          true,
		HasAdminExt:        true,
	}
	configured := []Role{RolePurger}

	roles := InferRoles(components, configured)
	if len(roles) != 1 {
		t.Fatalf("configured override should return 1 role, got %d", len(roles))
	}
	if roles[0] != RolePurger {
		t.Errorf("expected RolePurger, got %s", roles[0])
	}
}

func TestBuildDescriptor_AutoInfer(t *testing.T) {
	desc := BuildDescriptor("node-build", InferredComponents{
		HasStorageProvider: true,
		HasPurger:          true,
	}, nil, nil)

	if desc.ID != "node-build" {
		t.Errorf("expected ID 'node-build', got %q", desc.ID)
	}
	if len(desc.Roles) != 3 { // writer, reader, purger
		t.Errorf("expected 3 roles, got %d: %v", len(desc.Roles), desc.Roles)
	}
	// Capabilities derived from roles
	if !desc.CanExecute(CapPurgeExecute) {
		t.Error("should be able to execute purge")
	}
	if !desc.CanExecute(CapStorageWrite) {
		t.Error("should have storage write")
	}
}

func TestBuildDescriptor_ExplicitCaps(t *testing.T) {
	desc := BuildDescriptor("node-explicit",
		InferredComponents{HasStorageProvider: true},
		nil,
		[]Capability{CapStorageRead}, // Only read, even though storage provider implies write too
	)

	if desc.Capabilities.Len() != 1 {
		t.Errorf("expected 1 explicit cap, got %d", desc.Capabilities.Len())
	}
	if !desc.Capabilities.Has(CapStorageRead) {
		t.Error("should have explicit CapStorageRead")
	}
	if desc.Capabilities.Has(CapStorageWrite) {
		t.Error("should NOT have CapStorageWrite when explicitly overridden")
	}
}

func TestNodeFilter_Matches(t *testing.T) {
	purgerNode := NewNodeDescriptor("purger-1", []Role{RolePurger})
	writerNode := NewNodeDescriptor("writer-1", []Role{RoleWriter})
	agentNode := NewNodeDescriptor("agent-1", []Role{RoleAgent})

	tests := []struct {
		name    string
		filter  *NodeFilter
		node    *NodeDescriptor
		matches bool
	}{
		{
			name:    "nil filter matches all",
			filter:  nil,
			node:    purgerNode,
			matches: true,
		},
		{
			name:    "empty filter matches all",
			filter:  &NodeFilter{},
			node:    writerNode,
			matches: true,
		},
		{
			name:    "capability match",
			filter:  &NodeFilter{RequiredCapabilities: []Capability{CapPurgeExecute}},
			node:    purgerNode,
			matches: true,
		},
		{
			name:    "capability mismatch",
			filter:  &NodeFilter{RequiredCapabilities: []Capability{CapPurgeExecute}},
			node:    writerNode,
			matches: false,
		},
		{
			name:    "role match",
			filter:  &NodeFilter{RequiredRoles: []Role{RoleAgent}},
			node:    agentNode,
			matches: true,
		},
		{
			name:    "role mismatch",
			filter:  &NodeFilter{RequiredRoles: []Role{RoleAgent}},
			node:    purgerNode,
			matches: false,
		},
		{
			name:    "exclude by ID",
			filter:  &NodeFilter{ExcludeNodeIDs: []string{"purger-1"}},
			node:    purgerNode,
			matches: false,
		},
		{
			name:    "not excluded",
			filter:  &NodeFilter{ExcludeNodeIDs: []string{"other-node"}},
			node:    purgerNode,
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Matches(tt.node); got != tt.matches {
				t.Errorf("Matches() = %v, want %v", got, tt.matches)
			}
		})
	}
}

func TestLocalRegistry_BasicOperations(t *testing.T) {
	reg := NewLocalRegistry()
	ctx := t.Context()

	desc := NewNodeDescriptor("test-node", []Role{RolePurger, RoleWriter})

	// Register
	if err := reg.Register(ctx, desc, 0); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Get
	got, err := reg.GetNode(ctx, "test-node")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.ID != "test-node" {
		t.Error("expected to find test-node")
	}

	// List
	all, err := reg.ListNodes(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 node, got %d", len(all))
	}

	// Count by capability
	count, err := reg.CountByCapability(ctx, CapPurgeExecute)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}

	// Deregister
	if err := reg.Deregister(ctx, "test-node"); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	got, _ = reg.GetNode(ctx, "test-node")
	if got != nil {
		t.Error("expected node to be gone after deregister")
	}
}
