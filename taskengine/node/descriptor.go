// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import "time"

// NodeDescriptor fully describes a node participating in distributed task execution.
// It is a value object — immutable after creation, safe to serialize and transmit.
type NodeDescriptor struct {
	// ID is the unique identifier for this node.
	ID string `json:"id"`

	// Roles is the list of roles this node fulfills.
	// Effective capabilities are derived from roles (union) unless
	// ExplicitCapabilities is set.
	Roles []Role `json:"roles"`

	// Capabilities is the effective capability set for this node.
	// Computed from Roles via ExpandRoles, or overridden explicitly.
	Capabilities *CapabilitySet `json:"capabilities"`

	// Labels are optional key-value metadata (az, rack, version, etc.).
	Labels map[string]string `json:"labels,omitempty"`

	// StartedAt records when this node started.
	StartedAt time.Time `json:"startedAt"`
}

// NewNodeDescriptor creates a NodeDescriptor with capabilities derived from roles.
func NewNodeDescriptor(id string, roles []Role) *NodeDescriptor {
	return &NodeDescriptor{
		ID:           id,
		Roles:        roles,
		Capabilities: ExpandRoles(roles),
		StartedAt:    time.Now(),
	}
}

// NewNodeDescriptorWithCaps creates a NodeDescriptor with explicit capabilities
// (overriding role-based derivation). Used for advanced/custom configurations.
func NewNodeDescriptorWithCaps(id string, roles []Role, caps *CapabilitySet) *NodeDescriptor {
	return &NodeDescriptor{
		ID:           id,
		Roles:        roles,
		Capabilities: caps,
		StartedAt:    time.Now(),
	}
}

// CanExecute returns true if this node has ALL the required capabilities.
func (n *NodeDescriptor) CanExecute(required ...Capability) bool {
	if n.Capabilities == nil {
		return false
	}
	return n.Capabilities.HasAll(required...)
}

// HasRole returns true if this node has the given role.
func (n *NodeDescriptor) HasRole(role Role) bool {
	for _, r := range n.Roles {
		if r == role {
			return true
		}
	}
	return false
}
