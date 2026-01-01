// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// StatusHelper provides common status management logic for AgentRegistry implementations.
// This centralizes the state transition logic to avoid duplication between Redis and Memory implementations.
type StatusHelper struct{}

// NewStatusHelper creates a new StatusHelper.
func NewStatusHelper() *StatusHelper {
	return &StatusHelper{}
}

// Now returns the current timestamp in milliseconds.
// All time-related operations should use this method to ensure consistent time format.
func (h *StatusHelper) Now() int64 {
	return time.Now().UnixMilli()
}

// InitializeStatus initializes the status for a newly registered agent.
// Sets state to Online and records the state change time.
func (h *StatusHelper) InitializeStatus(agent *AgentInfo, now int64) {
	if agent.Status == nil {
		agent.Status = &AgentStatus{
			State:          AgentStateOnline,
			StateChangedAt: now,
		}
	} else {
		agent.Status.State = AgentStateOnline
		agent.Status.StateChangedAt = now
	}
}

// UpdateHeartbeatStatus updates the agent status during a heartbeat.
// Handles state transitions from offline to online and preserves StateChangedAt when appropriate.
// Parameters:
//   - agent: the existing agent info
//   - newStatus: optional new status from the heartbeat (can be nil)
//   - now: current timestamp in milliseconds
//
// Returns true if the agent was previously offline (recovered).
func (h *StatusHelper) UpdateHeartbeatStatus(agent *AgentInfo, newStatus *AgentStatus, now int64) (wasOffline bool) {
	wasOffline = agent.Status != nil && agent.Status.State == AgentStateOffline

	if newStatus != nil {
		// New status provided - use it but ensure state is online
		newStatus.State = AgentStateOnline
		if wasOffline {
			newStatus.StateChangedAt = now
		} else if agent.Status != nil {
			// Preserve existing StateChangedAt if not recovering from offline
			newStatus.StateChangedAt = agent.Status.StateChangedAt
		}
		agent.Status = newStatus
	} else if agent.Status != nil {
		// No new status - just update existing
		if wasOffline {
			agent.Status.StateChangedAt = now
		}
		agent.Status.State = AgentStateOnline
	} else {
		// No existing status - create new
		agent.Status = &AgentStatus{
			State:          AgentStateOnline,
			StateChangedAt: now,
		}
	}

	return wasOffline
}

// MarkOffline marks an agent as offline and updates StateChangedAt.
// Returns true if the state actually changed (was previously online).
func (h *StatusHelper) MarkOffline(agent *AgentInfo, now int64) (stateChanged bool) {
	if agent.Status == nil {
		agent.Status = &AgentStatus{
			State:          AgentStateOffline,
			StateChangedAt: now,
		}
		return true
	}

	if agent.Status.State == AgentStateOnline {
		agent.Status.State = AgentStateOffline
		agent.Status.StateChangedAt = now
		return true
	}

	return false
}

// UpdateHealthStatus updates the agent's health status and derives the appropriate state.
// Returns the new state and whether the state changed.
func (h *StatusHelper) UpdateHealthStatus(agent *AgentInfo, health *controlplanev1.HealthStatus, now int64) (newState AgentState, stateChanged bool) {
	if agent.Status == nil {
		agent.Status = &AgentStatus{}
	}

	oldState := agent.Status.State
	agent.Status.Health = health

	// Derive state from health
	if health != nil {
		switch health.State {
		case controlplanev1.HealthStateHealthy:
			newState = AgentStateOnline
		case controlplanev1.HealthStateUnhealthy:
			newState = AgentStateUnhealthy
		default:
			newState = oldState
		}
	} else {
		newState = oldState
	}

	// Update state and StateChangedAt if changed
	stateChanged = newState != oldState
	if stateChanged {
		agent.Status.StateChangedAt = now
	}
	agent.Status.State = newState

	return newState, stateChanged
}

// IsHeartbeatExpired checks if an agent's heartbeat has expired based on the given TTL.
func (h *StatusHelper) IsHeartbeatExpired(lastHeartbeat int64, heartbeatTTL time.Duration) bool {
	threshold := time.Now().Add(-heartbeatTTL).UnixMilli()
	return lastHeartbeat < threshold
}

// IsOnline checks if an agent is currently online based on its status.
func (h *StatusHelper) IsOnline(agent *AgentInfo) bool {
	return agent != nil && agent.Status != nil && agent.Status.State == AgentStateOnline
}
