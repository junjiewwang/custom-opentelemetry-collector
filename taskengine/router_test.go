// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"testing"

	"go.opentelemetry.io/collector/custom/taskengine/node"
)

func TestCompositeRouter_Route_Direct(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t1",
		Routing: TaskRouting{
			Strategy:     RoutingDirect,
			TargetNodeID: "agent-42",
		},
	}

	queue := router.Route(task)
	expected := "node:agent-42"
	if queue != expected {
		t.Errorf("expected queue %q, got %q", expected, queue)
	}
}

func TestCompositeRouter_Route_Direct_Empty(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t2",
		Routing: TaskRouting{
			Strategy:     RoutingDirect,
			TargetNodeID: "", // empty target falls back to broadcast
		},
	}

	queue := router.Route(task)
	if queue != "global" {
		t.Errorf("empty target should fallback to global, got %q", queue)
	}
}

func TestCompositeRouter_Route_Capability_Single(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t3",
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
		},
	}

	queue := router.Route(task)
	expected := "cap:purge:execute"
	if queue != expected {
		t.Errorf("expected queue %q, got %q", expected, queue)
	}
}

func TestCompositeRouter_Route_Capability_Multiple(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t4",
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: []node.Capability{node.CapPurgeExecute, node.CapStorageDelete},
		},
	}

	queue := router.Route(task)
	expected := "cap:purge:execute+storage:delete"
	if queue != expected {
		t.Errorf("expected queue %q, got %q", expected, queue)
	}
}

func TestCompositeRouter_Route_Capability_Empty(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t5",
		Routing: TaskRouting{
			Strategy:             RoutingCapability,
			RequiredCapabilities: nil,
		},
	}

	queue := router.Route(task)
	if queue != "global" {
		t.Errorf("empty caps should fallback to global, got %q", queue)
	}
}

func TestCompositeRouter_Route_Broadcast(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t6",
		Routing: TaskRouting{
			Strategy: RoutingBroadcast,
		},
	}

	queue := router.Route(task)
	if queue != "global" {
		t.Errorf("expected global queue, got %q", queue)
	}
}

func TestCompositeRouter_Route_Unknown(t *testing.T) {
	router := NewCompositeRouter()
	task := &Task{
		ID: "t7",
		Routing: TaskRouting{
			Strategy: RoutingStrategy("unknown"),
		},
	}

	queue := router.Route(task)
	if queue != "global" {
		t.Errorf("unknown strategy should fallback to global, got %q", queue)
	}
}

func TestCompositeRouter_MatchQueues(t *testing.T) {
	router := NewCompositeRouter()
	consumer := &ConsumerDescriptor{
		ID:           "node-1",
		Roles:        []node.Role{node.RolePurger},
		Capabilities: node.NewCapabilitySet(node.CapPurgeExecute, node.CapStorageRead, node.CapStorageDelete, node.CapPurgePlan),
	}

	queues := router.MatchQueues(consumer)

	// Should include: direct queue + capability queues + global
	// direct: "node:node-1"
	// caps (sorted): purge:execute, purge:plan, storage:delete, storage:read
	// global: "global"
	expectedMin := 6 // 1 direct + 4 caps + 1 global
	if len(queues) < expectedMin {
		t.Errorf("expected at least %d queues, got %d: %v", expectedMin, len(queues), queues)
	}

	// First should be direct queue
	if queues[0] != "node:node-1" {
		t.Errorf("first queue should be direct, got %q", queues[0])
	}
	// Last should be global
	if queues[len(queues)-1] != "global" {
		t.Errorf("last queue should be global, got %q", queues[len(queues)-1])
	}
}

func TestParseQueueType(t *testing.T) {
	tests := []struct {
		queueID  string
		expected RoutingStrategy
	}{
		{"node:agent-1", RoutingDirect},
		{"cap:purge:execute", RoutingCapability},
		{"global", RoutingBroadcast},
		{"unknown", RoutingBroadcast},
	}

	for _, tt := range tests {
		if got := ParseQueueType(tt.queueID); got != tt.expected {
			t.Errorf("ParseQueueType(%q) = %s, want %s", tt.queueID, got, tt.expected)
		}
	}
}
