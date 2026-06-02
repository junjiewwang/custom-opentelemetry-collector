// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"time"
)

// NodeRegistry manages node registration, heartbeat, and discovery.
// It answers: "Who else is out there, and what can they do?"
//
// Implementations:
//   - RedisRegistry: multi-node, uses HASH + TTL for heartbeat
//   - LocalRegistry: single-node, in-memory no-op for standalone deployments
type NodeRegistry interface {
	// Register announces this node to the cluster with a TTL.
	// The registration expires if Heartbeat is not called within the TTL.
	Register(ctx context.Context, node *NodeDescriptor, ttl time.Duration) error

	// Deregister removes this node from the cluster (graceful shutdown).
	Deregister(ctx context.Context, nodeID string) error

	// Heartbeat extends the node's TTL, signaling it is still alive.
	Heartbeat(ctx context.Context, nodeID string) error

	// GetNode returns a specific node's descriptor, or nil if not found/expired.
	GetNode(ctx context.Context, nodeID string) (*NodeDescriptor, error)

	// ListNodes returns all active nodes matching the filter.
	// If filter is nil/empty, returns all active nodes.
	ListNodes(ctx context.Context, filter *NodeFilter) ([]*NodeDescriptor, error)

	// CountByCapability returns the number of active nodes with a given capability.
	CountByCapability(ctx context.Context, cap Capability) (int, error)

	// Close releases resources held by the registry.
	Close() error
}

// NodeFilter specifies criteria for ListNodes queries.
// Fields are AND-combined: a node must satisfy ALL specified criteria.
type NodeFilter struct {
	// RequiredCapabilities: node must have ALL of these.
	RequiredCapabilities []Capability

	// RequiredRoles: node must have at least one of these roles.
	RequiredRoles []Role

	// ExcludeNodeIDs: exclude these node IDs from results.
	ExcludeNodeIDs []string
}

// Matches returns true if the given node satisfies this filter.
func (f *NodeFilter) Matches(node *NodeDescriptor) bool {
	if f == nil {
		return true
	}

	// Check excluded IDs
	for _, id := range f.ExcludeNodeIDs {
		if node.ID == id {
			return false
		}
	}

	// Check required capabilities (ALL must be present)
	if len(f.RequiredCapabilities) > 0 {
		if node.Capabilities == nil || !node.Capabilities.HasAll(f.RequiredCapabilities...) {
			return false
		}
	}

	// Check required roles (at least ONE must match)
	if len(f.RequiredRoles) > 0 {
		hasAny := false
		for _, requiredRole := range f.RequiredRoles {
			if node.HasRole(requiredRole) {
				hasAny = true
				break
			}
		}
		if !hasAny {
			return false
		}
	}

	return true
}
