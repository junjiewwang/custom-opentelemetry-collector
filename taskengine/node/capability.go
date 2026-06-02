// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package node provides the node identity and capability model for the
// unified task engine. It answers three questions for every participant:
//   - Who am I? (Identity)
//   - What can I do? (Capabilities)
//   - Am I still alive? (Registry/Heartbeat)
package node

import (
	"encoding/json"
	"sort"
)

// Capability represents an atomic unit of functionality a node can provide.
// Format convention: "{domain}:{action}" (e.g., "purge:execute", "arthas:execute").
type Capability string

// ─── Storage Capabilities ───

const (
	// CapStorageRead indicates the node can read from storage backends.
	CapStorageRead Capability = "storage:read"
	// CapStorageWrite indicates the node can write to storage backends.
	CapStorageWrite Capability = "storage:write"
	// CapStorageDelete indicates the node can delete from storage backends.
	CapStorageDelete Capability = "storage:delete"
)

// ─── Lifecycle/Purge Capabilities ───

const (
	// CapPurgeExecute indicates the node can execute single-index deletions.
	CapPurgeExecute Capability = "purge:execute"
	// CapPurgePlan indicates the node can scan expired indices and plan tasks (Leader).
	CapPurgePlan Capability = "purge:plan"
)

// ─── Controlplane Capabilities ───

const (
	// CapArthasExec indicates the node can execute Arthas diagnostic tasks.
	CapArthasExec Capability = "arthas:execute"
	// CapConfigPush indicates the node can push configuration to agents.
	CapConfigPush Capability = "config:push"
)

// ─── Query/UI Capabilities ───

const (
	// CapQueryServe indicates the node can serve query API requests.
	CapQueryServe Capability = "query:serve"
	// CapUIServe indicates the node can serve the admin UI.
	CapUIServe Capability = "ui:serve"
)

// ═══════════════════════════════════════════════════
// CapabilitySet — efficient set operations on capabilities
// ═══════════════════════════════════════════════════

// CapabilitySet is an unordered set of capabilities with O(1) lookup.
type CapabilitySet struct {
	caps map[Capability]struct{}
}

// NewCapabilitySet creates a CapabilitySet from the given capabilities.
func NewCapabilitySet(caps ...Capability) *CapabilitySet {
	s := &CapabilitySet{caps: make(map[Capability]struct{}, len(caps))}
	for _, c := range caps {
		s.caps[c] = struct{}{}
	}
	return s
}

// Add inserts one or more capabilities into the set.
func (s *CapabilitySet) Add(caps ...Capability) {
	for _, c := range caps {
		s.caps[c] = struct{}{}
	}
}

// Remove deletes a capability from the set.
func (s *CapabilitySet) Remove(cap Capability) {
	delete(s.caps, cap)
}

// Has returns true if the set contains the given capability.
func (s *CapabilitySet) Has(cap Capability) bool {
	_, ok := s.caps[cap]
	return ok
}

// HasAll returns true if the set contains ALL of the given capabilities.
func (s *CapabilitySet) HasAll(caps ...Capability) bool {
	for _, c := range caps {
		if _, ok := s.caps[c]; !ok {
			return false
		}
	}
	return true
}

// HasAny returns true if the set contains at least one of the given capabilities.
func (s *CapabilitySet) HasAny(caps ...Capability) bool {
	for _, c := range caps {
		if _, ok := s.caps[c]; ok {
			return true
		}
	}
	return false
}

// Len returns the number of capabilities in the set.
func (s *CapabilitySet) Len() int {
	return len(s.caps)
}

// List returns all capabilities as a sorted slice (deterministic output).
func (s *CapabilitySet) List() []Capability {
	result := make([]Capability, 0, len(s.caps))
	for c := range s.caps {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}

// Union returns a new set that is the union of this set and other.
func (s *CapabilitySet) Union(other *CapabilitySet) *CapabilitySet {
	result := NewCapabilitySet(s.List()...)
	if other != nil {
		result.Add(other.List()...)
	}
	return result
}

// MarshalJSON implements json.Marshaler.
func (s *CapabilitySet) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.List())
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *CapabilitySet) UnmarshalJSON(data []byte) error {
	var caps []Capability
	if err := json.Unmarshal(data, &caps); err != nil {
		return err
	}
	s.caps = make(map[Capability]struct{}, len(caps))
	for _, c := range caps {
		s.caps[c] = struct{}{}
	}
	return nil
}
