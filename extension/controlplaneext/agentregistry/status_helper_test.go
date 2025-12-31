// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func TestStatusHelper_InitializeStatus(t *testing.T) {
	h := NewStatusHelper()
	now := time.Now().UnixNano()

	t.Run("nil status", func(t *testing.T) {
		agent := &AgentInfo{AgentID: "test"}
		h.InitializeStatus(agent, now)

		assert.NotNil(t, agent.Status)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})

	t.Run("existing status", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOffline,
				StateChangedAt: now - 1000,
			},
		}
		h.InitializeStatus(agent, now)

		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})
}

func TestStatusHelper_UpdateHeartbeatStatus(t *testing.T) {
	h := NewStatusHelper()
	now := time.Now().UnixNano()

	t.Run("recover from offline with new status", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOffline,
				StateChangedAt: now - 1000,
			},
		}
		newStatus := &AgentStatus{ConfigVersion: "v1"}

		wasOffline := h.UpdateHeartbeatStatus(agent, newStatus, now)

		assert.True(t, wasOffline)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
		assert.Equal(t, "v1", agent.Status.ConfigVersion)
	})

	t.Run("normal heartbeat preserves StateChangedAt", func(t *testing.T) {
		originalTime := now - 5000
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOnline,
				StateChangedAt: originalTime,
			},
		}
		newStatus := &AgentStatus{ConfigVersion: "v2"}

		wasOffline := h.UpdateHeartbeatStatus(agent, newStatus, now)

		assert.False(t, wasOffline)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, originalTime, agent.Status.StateChangedAt) // Preserved
		assert.Equal(t, "v2", agent.Status.ConfigVersion)
	})

	t.Run("nil new status updates existing", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOffline,
				StateChangedAt: now - 1000,
			},
		}

		wasOffline := h.UpdateHeartbeatStatus(agent, nil, now)

		assert.True(t, wasOffline)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})

	t.Run("nil existing status creates new", func(t *testing.T) {
		agent := &AgentInfo{AgentID: "test"}

		wasOffline := h.UpdateHeartbeatStatus(agent, nil, now)

		assert.False(t, wasOffline)
		assert.NotNil(t, agent.Status)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})
}

func TestStatusHelper_MarkOffline(t *testing.T) {
	h := NewStatusHelper()
	now := time.Now().UnixNano()

	t.Run("mark online agent offline", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOnline,
				StateChangedAt: now - 1000,
			},
		}

		changed := h.MarkOffline(agent, now)

		assert.True(t, changed)
		assert.Equal(t, AgentStateOffline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})

	t.Run("already offline agent", func(t *testing.T) {
		originalTime := now - 1000
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOffline,
				StateChangedAt: originalTime,
			},
		}

		changed := h.MarkOffline(agent, now)

		assert.False(t, changed)
		assert.Equal(t, AgentStateOffline, agent.Status.State)
		assert.Equal(t, originalTime, agent.Status.StateChangedAt) // Unchanged
	})

	t.Run("nil status", func(t *testing.T) {
		agent := &AgentInfo{AgentID: "test"}

		changed := h.MarkOffline(agent, now)

		assert.True(t, changed)
		assert.NotNil(t, agent.Status)
		assert.Equal(t, AgentStateOffline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})
}

func TestStatusHelper_UpdateHealthStatus(t *testing.T) {
	h := NewStatusHelper()
	now := time.Now().UnixNano()

	t.Run("healthy to unhealthy", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOnline,
				StateChangedAt: now - 1000,
			},
		}
		health := &controlplanev1.HealthStatus{State: controlplanev1.HealthStateUnhealthy}

		newState, changed := h.UpdateHealthStatus(agent, health, now)

		assert.True(t, changed)
		assert.Equal(t, AgentStateUnhealthy, newState)
		assert.Equal(t, AgentStateUnhealthy, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
		assert.Equal(t, health, agent.Status.Health)
	})

	t.Run("unhealthy to healthy", func(t *testing.T) {
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateUnhealthy,
				StateChangedAt: now - 1000,
			},
		}
		health := &controlplanev1.HealthStatus{State: controlplanev1.HealthStateHealthy}

		newState, changed := h.UpdateHealthStatus(agent, health, now)

		assert.True(t, changed)
		assert.Equal(t, AgentStateOnline, newState)
		assert.Equal(t, AgentStateOnline, agent.Status.State)
		assert.Equal(t, now, agent.Status.StateChangedAt)
	})

	t.Run("same state no change", func(t *testing.T) {
		originalTime := now - 1000
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOnline,
				StateChangedAt: originalTime,
			},
		}
		health := &controlplanev1.HealthStatus{State: controlplanev1.HealthStateHealthy}

		newState, changed := h.UpdateHealthStatus(agent, health, now)

		assert.False(t, changed)
		assert.Equal(t, AgentStateOnline, newState)
		assert.Equal(t, originalTime, agent.Status.StateChangedAt) // Unchanged
	})

	t.Run("nil health preserves state", func(t *testing.T) {
		originalTime := now - 1000
		agent := &AgentInfo{
			AgentID: "test",
			Status: &AgentStatus{
				State:          AgentStateOnline,
				StateChangedAt: originalTime,
			},
		}

		newState, changed := h.UpdateHealthStatus(agent, nil, now)

		assert.False(t, changed)
		assert.Equal(t, AgentStateOnline, newState)
		assert.Nil(t, agent.Status.Health)
	})
}

func TestStatusHelper_IsHeartbeatExpired(t *testing.T) {
	h := NewStatusHelper()

	t.Run("expired", func(t *testing.T) {
		lastHeartbeat := time.Now().Add(-1 * time.Minute).UnixNano()
		expired := h.IsHeartbeatExpired(lastHeartbeat, 30*time.Second)
		assert.True(t, expired)
	})

	t.Run("not expired", func(t *testing.T) {
		lastHeartbeat := time.Now().Add(-10 * time.Second).UnixNano()
		expired := h.IsHeartbeatExpired(lastHeartbeat, 30*time.Second)
		assert.False(t, expired)
	})
}

func TestStatusHelper_IsOnline(t *testing.T) {
	h := NewStatusHelper()

	t.Run("online", func(t *testing.T) {
		agent := &AgentInfo{
			Status: &AgentStatus{State: AgentStateOnline},
		}
		assert.True(t, h.IsOnline(agent))
	})

	t.Run("offline", func(t *testing.T) {
		agent := &AgentInfo{
			Status: &AgentStatus{State: AgentStateOffline},
		}
		assert.False(t, h.IsOnline(agent))
	})

	t.Run("nil status", func(t *testing.T) {
		agent := &AgentInfo{}
		assert.False(t, h.IsOnline(agent))
	})

	t.Run("nil agent", func(t *testing.T) {
		assert.False(t, h.IsOnline(nil))
	})
}
