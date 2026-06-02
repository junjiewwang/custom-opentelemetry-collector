// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// Router determines how tasks are dispatched to queues and how consumers
// find queues to monitor. This is the core routing abstraction that separates
// task submission from task consumption.
//
// Queue ID conventions:
//   - "node:{nodeID}"          — Direct routing (task goes to a specific node)
//   - "cap:{capabilityName}"   — Capability routing (task goes to capability queue)
//   - "global"                 — Broadcast routing (any consumer can pick it up)
type Router interface {
	// Route determines which queue a task should be placed into.
	Route(task *Task) string

	// MatchQueues returns all queue IDs that a consumer should monitor,
	// based on its identity and capabilities.
	MatchQueues(consumer *ConsumerDescriptor) []string
}

// ─── Composite Router (Strategy Pattern) ───

// CompositeRouter delegates to sub-routers based on the task's RoutingStrategy.
// This is the default router used by the engine.
type CompositeRouter struct{}

// NewCompositeRouter creates the default composite router.
func NewCompositeRouter() *CompositeRouter {
	return &CompositeRouter{}
}

// Route dispatches to the appropriate sub-strategy based on task.Routing.Strategy.
func (r *CompositeRouter) Route(task *Task) string {
	switch task.Routing.Strategy {
	case RoutingDirect:
		return routeDirect(task)
	case RoutingCapability:
		return routeCapability(task)
	case RoutingBroadcast:
		return routeBroadcast()
	default:
		// Unknown strategy defaults to broadcast
		return routeBroadcast()
	}
}

// MatchQueues returns all queues a consumer should listen on.
// Order matters: direct queue first (highest priority), then capability queues, then global.
func (r *CompositeRouter) MatchQueues(consumer *ConsumerDescriptor) []string {
	var queues []string

	// 1. Direct queue — tasks targeted specifically at this consumer
	queues = append(queues, directQueueID(consumer.ID))

	// 2. Capability queues — tasks matching this consumer's capabilities
	if consumer.Capabilities != nil {
		for _, cap := range consumer.Capabilities.List() {
			queues = append(queues, capabilityQueueID(cap))
		}
	}

	// 3. Global broadcast queue — tasks for anyone
	queues = append(queues, globalQueueID())

	return queues
}

// ─── Queue ID builders ───

// directQueueID returns the queue ID for direct routing to a specific node.
func directQueueID(nodeID string) string {
	return fmt.Sprintf("node:%s", nodeID)
}

// capabilityQueueID returns the queue ID for a capability-based queue.
func capabilityQueueID(cap node.Capability) string {
	return fmt.Sprintf("cap:%s", cap)
}

// globalQueueID returns the global broadcast queue ID.
func globalQueueID() string {
	return "global"
}

// ─── Sub-routing functions ───

// routeDirect routes to the target node's personal queue.
func routeDirect(task *Task) string {
	if task.Routing.TargetNodeID == "" {
		// Fallback: if no target specified, go to broadcast
		return routeBroadcast()
	}
	return directQueueID(task.Routing.TargetNodeID)
}

// routeCapability routes to the first required capability's queue.
// If multiple capabilities are required, the task goes to the first one.
// The consumer must have ALL required capabilities to monitor that queue.
//
// Design decision: Use a composite queue ID when multiple capabilities are needed,
// to avoid the task being consumed by a node with only partial capabilities.
func routeCapability(task *Task) string {
	caps := task.Routing.RequiredCapabilities
	if len(caps) == 0 {
		return routeBroadcast()
	}
	if len(caps) == 1 {
		return capabilityQueueID(caps[0])
	}
	// Multiple capabilities: create a composite queue ID
	// e.g., "cap:purge:execute+storage:delete"
	names := make([]string, len(caps))
	for i, c := range caps {
		names[i] = string(c)
	}
	return fmt.Sprintf("cap:%s", strings.Join(names, "+"))
}

// routeBroadcast routes to the global queue.
func routeBroadcast() string {
	return globalQueueID()
}

// ─── Queue ID parsing utilities ───

// ParseQueueType extracts the routing type from a queue ID.
func ParseQueueType(queueID string) RoutingStrategy {
	switch {
	case strings.HasPrefix(queueID, "node:"):
		return RoutingDirect
	case strings.HasPrefix(queueID, "cap:"):
		return RoutingCapability
	case queueID == "global":
		return RoutingBroadcast
	default:
		return RoutingBroadcast
	}
}
