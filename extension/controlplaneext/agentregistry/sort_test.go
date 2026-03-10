// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ParseSortOptions Tests
// ============================================================================

func TestParseSortOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		field     string
		order     string
		wantField SortField
		wantOrder SortOrder
		wantErr   bool
	}{
		{
			name:      "empty values use defaults",
			field:     "",
			order:     "",
			wantField: SortFieldStatusHeartbeat,
			wantOrder: SortOrderDesc,
		},
		{
			name:      "valid field status_heartbeat",
			field:     "status_heartbeat",
			order:     "desc",
			wantField: SortFieldStatusHeartbeat,
			wantOrder: SortOrderDesc,
		},
		{
			name:      "valid field last_heartbeat asc",
			field:     "last_heartbeat",
			order:     "asc",
			wantField: SortFieldLastHeartbeat,
			wantOrder: SortOrderAsc,
		},
		{
			name:      "valid field registered_at",
			field:     "registered_at",
			order:     "desc",
			wantField: SortFieldRegisteredAt,
			wantOrder: SortOrderDesc,
		},
		{
			name:      "valid field start_time",
			field:     "start_time",
			order:     "asc",
			wantField: SortFieldStartTime,
			wantOrder: SortOrderAsc,
		},
		{
			name:      "order is case-insensitive",
			field:     "last_heartbeat",
			order:     "ASC",
			wantField: SortFieldLastHeartbeat,
			wantOrder: SortOrderAsc,
		},
		{
			name:    "invalid field",
			field:   "unknown_field",
			order:   "asc",
			wantErr: true,
		},
		{
			name:    "invalid order",
			field:   "last_heartbeat",
			order:   "random",
			wantErr: true,
		},
		{
			name:      "only field specified, order defaults to desc",
			field:     "registered_at",
			order:     "",
			wantField: SortFieldRegisteredAt,
			wantOrder: SortOrderDesc,
		},
		{
			name:      "only order specified, field defaults to status_heartbeat",
			field:     "",
			order:     "asc",
			wantField: SortFieldStatusHeartbeat,
			wantOrder: SortOrderAsc,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts, err := ParseSortOptions(tt.field, tt.order)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantField, opts.Field)
			assert.Equal(t, tt.wantOrder, opts.Order)
		})
	}
}

// ============================================================================
// SortAgents Tests
// ============================================================================

// newTestAgent creates a test AgentInfo with the given parameters.
func newTestAgent(id string, state AgentState, lastHeartbeat, registeredAt, startTime int64) *AgentInfo {
	return &AgentInfo{
		AgentID:       id,
		LastHeartbeat: lastHeartbeat,
		RegisteredAt:  registeredAt,
		StartTime:     startTime,
		Status: &AgentStatus{
			State: state,
		},
	}
}

func TestSortAgents_StatusHeartbeat_Desc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("offline-old", AgentStateOffline, 1000, 100, 100),
		newTestAgent("online-recent", AgentStateOnline, 3000, 300, 300),
		newTestAgent("unhealthy", AgentStateUnhealthy, 2000, 200, 200),
		newTestAgent("online-old", AgentStateOnline, 1000, 100, 100),
		newTestAgent("offline-recent", AgentStateOffline, 3000, 300, 300),
	}

	SortAgents(agents, SortOptions{Field: SortFieldStatusHeartbeat, Order: SortOrderDesc})

	wantIDs := []string{"online-recent", "online-old", "unhealthy", "offline-recent", "offline-old"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "desc: online first (by heartbeat), then unhealthy, then offline")
}

func TestSortAgents_StatusHeartbeat_Asc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("online-recent", AgentStateOnline, 3000, 300, 300),
		newTestAgent("offline-old", AgentStateOffline, 1000, 100, 100),
		newTestAgent("unhealthy", AgentStateUnhealthy, 2000, 200, 200),
		newTestAgent("online-old", AgentStateOnline, 1000, 100, 100),
		newTestAgent("offline-recent", AgentStateOffline, 3000, 300, 300),
	}

	SortAgents(agents, SortOptions{Field: SortFieldStatusHeartbeat, Order: SortOrderAsc})

	wantIDs := []string{"offline-old", "offline-recent", "unhealthy", "online-old", "online-recent"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "asc: offline first (by heartbeat), then unhealthy, then online")
}

func TestSortAgents_LastHeartbeat_Desc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("agent-a", AgentStateOnline, 1000, 100, 100),
		newTestAgent("agent-b", AgentStateOffline, 3000, 300, 300),
		newTestAgent("agent-c", AgentStateOnline, 2000, 200, 200),
	}

	SortAgents(agents, SortOptions{Field: SortFieldLastHeartbeat, Order: SortOrderDesc})

	wantIDs := []string{"agent-b", "agent-c", "agent-a"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "desc by last_heartbeat regardless of state")
}

func TestSortAgents_LastHeartbeat_Asc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("agent-b", AgentStateOffline, 3000, 300, 300),
		newTestAgent("agent-a", AgentStateOnline, 1000, 100, 100),
		newTestAgent("agent-c", AgentStateOnline, 2000, 200, 200),
	}

	SortAgents(agents, SortOptions{Field: SortFieldLastHeartbeat, Order: SortOrderAsc})

	wantIDs := []string{"agent-a", "agent-c", "agent-b"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "asc by last_heartbeat")
}

func TestSortAgents_RegisteredAt_Desc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("agent-a", AgentStateOnline, 1000, 100, 100),
		newTestAgent("agent-c", AgentStateOnline, 1000, 300, 300),
		newTestAgent("agent-b", AgentStateOffline, 1000, 200, 200),
	}

	SortAgents(agents, SortOptions{Field: SortFieldRegisteredAt, Order: SortOrderDesc})

	wantIDs := []string{"agent-c", "agent-b", "agent-a"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "desc by registered_at")
}

func TestSortAgents_StartTime_Asc(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("agent-c", AgentStateOnline, 1000, 100, 300),
		newTestAgent("agent-a", AgentStateOnline, 1000, 100, 100),
		newTestAgent("agent-b", AgentStateOffline, 1000, 100, 200),
	}

	SortAgents(agents, SortOptions{Field: SortFieldStartTime, Order: SortOrderAsc})

	wantIDs := []string{"agent-a", "agent-b", "agent-c"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "asc by start_time")
}

func TestSortAgents_TieBreaker_AgentID(t *testing.T) {
	t.Parallel()

	// All same state and heartbeat — should be sorted by AgentID ascending
	agents := []*AgentInfo{
		newTestAgent("charlie", AgentStateOnline, 1000, 100, 100),
		newTestAgent("alice", AgentStateOnline, 1000, 100, 100),
		newTestAgent("bob", AgentStateOnline, 1000, 100, 100),
	}

	SortAgents(agents, SortOptions{Field: SortFieldStatusHeartbeat, Order: SortOrderDesc})

	wantIDs := []string{"alice", "bob", "charlie"}
	gotIDs := extractIDs(agents)
	assert.Equal(t, wantIDs, gotIDs, "tie-breaker: AgentID ascending")
}

func TestSortAgents_NilStatus(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("online-agent", AgentStateOnline, 2000, 200, 200),
		{AgentID: "nil-status-agent", LastHeartbeat: 1000, RegisteredAt: 100, StartTime: 100},
		newTestAgent("offline-agent", AgentStateOffline, 1500, 150, 150),
	}

	SortAgents(agents, SortOptions{Field: SortFieldStatusHeartbeat, Order: SortOrderDesc})

	wantIDs := []string{"online-agent", "nil-status-agent", "offline-agent"}
	gotIDs := extractIDs(agents)
	// nil status has weight 0, same as offline. Nil agent has heartbeat 1000 < offline's 1500,
	// but both have weight 0, so sort by heartbeat desc within same weight: offline(1500) > nil(1000).
	// Wait — let me recalculate: offline-agent heartbeat=1500, nil-status-agent heartbeat=1000.
	// Both weight 0, desc: offline(1500) first, then nil(1000).
	wantIDs = []string{"online-agent", "offline-agent", "nil-status-agent"}
	assert.Equal(t, wantIDs, gotIDs, "nil status treated as weight 0 (same as offline)")
}

func TestSortAgents_EmptySlice(t *testing.T) {
	t.Parallel()

	var agents []*AgentInfo
	SortAgents(agents, DefaultSortOptions())
	assert.Empty(t, agents)
}

func TestSortAgents_SingleElement(t *testing.T) {
	t.Parallel()

	agents := []*AgentInfo{
		newTestAgent("only-one", AgentStateOnline, 1000, 100, 100),
	}

	SortAgents(agents, DefaultSortOptions())
	assert.Equal(t, "only-one", agents[0].AgentID)
}

// ============================================================================
// stateWeight Tests
// ============================================================================

func TestStateWeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		agent  *AgentInfo
		weight int
	}{
		{
			name:   "online = 2",
			agent:  &AgentInfo{Status: &AgentStatus{State: AgentStateOnline}},
			weight: 2,
		},
		{
			name:   "unhealthy = 1",
			agent:  &AgentInfo{Status: &AgentStatus{State: AgentStateUnhealthy}},
			weight: 1,
		},
		{
			name:   "offline = 0",
			agent:  &AgentInfo{Status: &AgentStatus{State: AgentStateOffline}},
			weight: 0,
		},
		{
			name:   "nil status = 0",
			agent:  &AgentInfo{},
			weight: 0,
		},
		{
			name:   "unknown state = 0",
			agent:  &AgentInfo{Status: &AgentStatus{State: "unknown"}},
			weight: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.weight, stateWeight(tt.agent))
		})
	}
}

// ============================================================================
// DefaultSortOptions Tests
// ============================================================================

func TestDefaultSortOptions(t *testing.T) {
	t.Parallel()

	opts := DefaultSortOptions()
	assert.Equal(t, SortFieldStatusHeartbeat, opts.Field)
	assert.Equal(t, SortOrderDesc, opts.Order)
}

// ============================================================================
// Helpers
// ============================================================================

func extractIDs(agents []*AgentInfo) []string {
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.AgentID
	}
	return ids
}
