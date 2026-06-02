// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"sync"
	"time"
)

// LocalRegistry implements NodeRegistry for single-node deployments.
// It stores node descriptors in-memory and always considers all registered nodes as alive.
// This avoids Redis dependency for standalone (all-in-one) deployments.
type LocalRegistry struct {
	mu    sync.RWMutex
	nodes map[string]*NodeDescriptor
}

// NewLocalRegistry creates a new in-memory registry.
func NewLocalRegistry() *LocalRegistry {
	return &LocalRegistry{
		nodes: make(map[string]*NodeDescriptor),
	}
}

// Register stores the node descriptor in memory.
// The TTL is ignored for local registry (all nodes are always alive).
func (r *LocalRegistry) Register(_ context.Context, node *NodeDescriptor, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[node.ID] = node
	return nil
}

// Deregister removes the node from the local store.
func (r *LocalRegistry) Deregister(_ context.Context, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, nodeID)
	return nil
}

// Heartbeat is a no-op for local registry (always alive).
func (r *LocalRegistry) Heartbeat(_ context.Context, _ string) error {
	return nil
}

// GetNode returns the descriptor for a specific node.
func (r *LocalRegistry) GetNode(_ context.Context, nodeID string) (*NodeDescriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[nodeID]
	if !ok {
		return nil, nil
	}
	return node, nil
}

// ListNodes returns all registered nodes matching the filter.
func (r *LocalRegistry) ListNodes(_ context.Context, filter *NodeFilter) ([]*NodeDescriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*NodeDescriptor
	for _, node := range r.nodes {
		if filter.Matches(node) {
			result = append(result, node)
		}
	}
	return result, nil
}

// CountByCapability returns the number of nodes with the given capability.
func (r *LocalRegistry) CountByCapability(_ context.Context, cap Capability) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, node := range r.nodes {
		if node.Capabilities != nil && node.Capabilities.Has(cap) {
			count++
		}
	}
	return count, nil
}

// Close is a no-op for local registry.
func (r *LocalRegistry) Close() error {
	return nil
}
