// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"encoding/json"
	"testing"
)

func TestCapabilitySet_Basic(t *testing.T) {
	cs := NewCapabilitySet(CapStorageRead, CapStorageWrite)

	if cs.Len() != 2 {
		t.Errorf("expected len 2, got %d", cs.Len())
	}
	if !cs.Has(CapStorageRead) {
		t.Error("expected to have CapStorageRead")
	}
	if !cs.Has(CapStorageWrite) {
		t.Error("expected to have CapStorageWrite")
	}
	if cs.Has(CapStorageDelete) {
		t.Error("should not have CapStorageDelete")
	}
}

func TestCapabilitySet_Add(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Add(CapPurgeExecute, CapPurgePlan)

	if cs.Len() != 2 {
		t.Errorf("expected len 2, got %d", cs.Len())
	}
	if !cs.Has(CapPurgeExecute) {
		t.Error("expected to have CapPurgeExecute")
	}
}

func TestCapabilitySet_Remove(t *testing.T) {
	cs := NewCapabilitySet(CapStorageRead, CapStorageWrite, CapStorageDelete)
	cs.Remove(CapStorageDelete)

	if cs.Len() != 2 {
		t.Errorf("expected len 2, got %d", cs.Len())
	}
	if cs.Has(CapStorageDelete) {
		t.Error("should not have CapStorageDelete after removal")
	}
}

func TestCapabilitySet_HasAll(t *testing.T) {
	cs := NewCapabilitySet(CapStorageRead, CapStorageWrite, CapStorageDelete)

	if !cs.HasAll(CapStorageRead, CapStorageWrite) {
		t.Error("expected HasAll to return true for subset")
	}
	if cs.HasAll(CapStorageRead, CapArthasExec) {
		t.Error("expected HasAll to return false when one cap is missing")
	}
	// Empty list should always return true
	if !cs.HasAll() {
		t.Error("expected HasAll() with no args to return true")
	}
}

func TestCapabilitySet_HasAny(t *testing.T) {
	cs := NewCapabilitySet(CapStorageRead)

	if !cs.HasAny(CapStorageRead, CapArthasExec) {
		t.Error("expected HasAny to return true when one matches")
	}
	if cs.HasAny(CapArthasExec, CapPurgeExecute) {
		t.Error("expected HasAny to return false when none match")
	}
	if cs.HasAny() {
		t.Error("expected HasAny() with no args to return false")
	}
}

func TestCapabilitySet_List_Sorted(t *testing.T) {
	cs := NewCapabilitySet(CapUIServe, CapArthasExec, CapStorageRead)
	list := cs.List()

	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}
	// Should be alphabetically sorted
	for i := 1; i < len(list); i++ {
		if list[i-1] >= list[i] {
			t.Errorf("list not sorted: %v", list)
			break
		}
	}
}

func TestCapabilitySet_Union(t *testing.T) {
	a := NewCapabilitySet(CapStorageRead, CapStorageWrite)
	b := NewCapabilitySet(CapStorageWrite, CapStorageDelete)

	union := a.Union(b)

	if union.Len() != 3 {
		t.Errorf("expected union len 3, got %d", union.Len())
	}
	if !union.HasAll(CapStorageRead, CapStorageWrite, CapStorageDelete) {
		t.Error("union should have all caps from both sets")
	}
	// Original sets should be unchanged
	if a.Len() != 2 {
		t.Error("original set a should be unchanged")
	}
}

func TestCapabilitySet_Union_NilOther(t *testing.T) {
	a := NewCapabilitySet(CapStorageRead)
	union := a.Union(nil)
	if union.Len() != 1 {
		t.Errorf("union with nil should return copy of self, got len %d", union.Len())
	}
}

func TestCapabilitySet_JSON(t *testing.T) {
	cs := NewCapabilitySet(CapStorageRead, CapPurgeExecute)

	data, err := json.Marshal(cs)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored CapabilitySet
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if restored.Len() != cs.Len() {
		t.Errorf("restored len %d != original %d", restored.Len(), cs.Len())
	}
	if !restored.Has(CapStorageRead) || !restored.Has(CapPurgeExecute) {
		t.Error("restored set should have original capabilities")
	}
}

func TestCapabilitySet_Empty(t *testing.T) {
	cs := NewCapabilitySet()
	if cs.Len() != 0 {
		t.Errorf("expected empty set, got len %d", cs.Len())
	}
	if cs.Has(CapStorageRead) {
		t.Error("empty set should not have any capability")
	}
	if cs.HasAny(CapStorageRead) {
		t.Error("empty set HasAny should return false")
	}
	if !cs.HasAll() {
		t.Error("empty set HasAll with no args should return true")
	}
}
