// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"encoding/json"
	"testing"
)

func TestNewNodeDescriptor(t *testing.T) {
	desc := NewNodeDescriptor("node-1", []Role{RoleWriter, RolePurger})

	if desc.ID != "node-1" {
		t.Errorf("expected ID 'node-1', got %q", desc.ID)
	}
	if len(desc.Roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(desc.Roles))
	}
	if desc.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	// Writer: CapStorageWrite; Purger: CapStorageRead + CapStorageDelete + CapPurgeExecute + CapPurgePlan
	if desc.Capabilities.Len() != 5 {
		t.Errorf("expected 5 capabilities, got %d: %v", desc.Capabilities.Len(), desc.Capabilities.List())
	}
	if desc.StartedAt.IsZero() {
		t.Error("startedAt should be set")
	}
}

func TestNewNodeDescriptorWithCaps(t *testing.T) {
	explicitCaps := NewCapabilitySet(CapStorageRead, CapUIServe)
	desc := NewNodeDescriptorWithCaps("node-2", []Role{RoleReader}, explicitCaps)

	// Capabilities should be the explicit set, not derived from roles
	if desc.Capabilities.Len() != 2 {
		t.Errorf("expected 2 explicit caps, got %d", desc.Capabilities.Len())
	}
	if !desc.Capabilities.Has(CapUIServe) {
		t.Error("expected explicit CapUIServe")
	}
}

func TestNodeDescriptor_CanExecute(t *testing.T) {
	desc := NewNodeDescriptor("node-1", []Role{RolePurger})

	if !desc.CanExecute(CapPurgeExecute) {
		t.Error("purger should be able to execute purge tasks")
	}
	if !desc.CanExecute(CapStorageRead, CapStorageDelete) {
		t.Error("purger should have both read and delete")
	}
	if desc.CanExecute(CapArthasExec) {
		t.Error("purger should not have arthas capability")
	}
	if desc.CanExecute(CapPurgeExecute, CapArthasExec) {
		t.Error("purger should not have arthas + purge combined")
	}
}

func TestNodeDescriptor_CanExecute_NilCapabilities(t *testing.T) {
	desc := &NodeDescriptor{ID: "test", Capabilities: nil}
	if desc.CanExecute(CapStorageRead) {
		t.Error("nil capabilities should not match anything")
	}
}

func TestNodeDescriptor_HasRole(t *testing.T) {
	desc := NewNodeDescriptor("node-1", []Role{RoleWriter, RoleReader})

	if !desc.HasRole(RoleWriter) {
		t.Error("should have writer role")
	}
	if !desc.HasRole(RoleReader) {
		t.Error("should have reader role")
	}
	if desc.HasRole(RolePurger) {
		t.Error("should not have purger role")
	}
}

func TestNodeDescriptor_JSON(t *testing.T) {
	desc := NewNodeDescriptor("node-json", []Role{RoleWriter, RoleUI})
	desc.Labels = map[string]string{"az": "us-east-1", "version": "1.2.3"}

	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored NodeDescriptor
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if restored.ID != desc.ID {
		t.Errorf("ID mismatch: %s vs %s", restored.ID, desc.ID)
	}
	if len(restored.Roles) != len(desc.Roles) {
		t.Errorf("roles length mismatch: %d vs %d", len(restored.Roles), len(desc.Roles))
	}
	if restored.Capabilities.Len() != desc.Capabilities.Len() {
		t.Errorf("caps length mismatch: %d vs %d", restored.Capabilities.Len(), desc.Capabilities.Len())
	}
	if restored.Labels["az"] != "us-east-1" {
		t.Error("labels not preserved")
	}
}
