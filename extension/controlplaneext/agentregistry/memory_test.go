// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func newTestMemoryRegistry(t *testing.T) *MemoryAgentRegistry {
	logger := zap.NewNop()
	config := Config{
		HeartbeatTTL:         5 * time.Second,
		OfflineCheckInterval: 1 * time.Second,
	}
	return NewMemoryAgentRegistry(logger, config)
}

func TestMemoryAgentRegistry_Register(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	agent := &AgentInfo{
		AgentID:  "agent-1",
		AppID:    "app-test",
		Hostname: "host-1",
		IP:       "192.168.1.1",
		Version:  "1.0.0",
		Labels:   map[string]string{"env": "test"},
	}

	err = registry.Register(ctx, agent)
	require.NoError(t, err)

	// Verify agent was registered
	retrieved, err := registry.GetAgent(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, "host-1", retrieved.Hostname)
	assert.Equal(t, AgentStateOnline, retrieved.Status.State)
	assert.NotZero(t, retrieved.RegisteredAt)
	assert.NotZero(t, retrieved.LastHeartbeat)
}

func TestMemoryAgentRegistry_Register_Validation(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Nil agent
	err = registry.Register(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")

	// Empty agent ID
	err = registry.Register(ctx, &AgentInfo{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id is required")

	// Empty app ID
	err = registry.Register(ctx, &AgentInfo{AgentID: "agent-1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "app_id is required")
}

func TestMemoryAgentRegistry_Heartbeat(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agent
	agent := &AgentInfo{
		AgentID:  "agent-1",
		AppID:    "app-test",
		Hostname: "host-1",
	}
	err = registry.Register(ctx, agent)
	require.NoError(t, err)

	// Get initial heartbeat time
	retrieved, _ := registry.GetAgent(ctx, "agent-1")
	initialHeartbeat := retrieved.LastHeartbeat

	time.Sleep(10 * time.Millisecond)

	// Send heartbeat
	status := &AgentStatus{
		ConfigVersion: "v1.0",
		Metrics: &AgentMetrics{
			UptimeSeconds: 100,
		},
	}
	err = registry.Heartbeat(ctx, "agent-1", status)
	require.NoError(t, err)

	// Verify heartbeat was updated
	retrieved, _ = registry.GetAgent(ctx, "agent-1")
	assert.Greater(t, retrieved.LastHeartbeat, initialHeartbeat)
	assert.Equal(t, AgentStateOnline, retrieved.Status.State)
	assert.Equal(t, "v1.0", retrieved.Status.ConfigVersion)
}

func TestMemoryAgentRegistry_Heartbeat_NotFound(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	err = registry.Heartbeat(ctx, "nonexistent", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMemoryAgentRegistry_RegisterOrHeartbeat_NewAgent(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// RegisterOrHeartbeat with new agent should auto-register
	agent := &AgentInfo{
		AgentID:  "agent-new",
		AppID:    "app-test",
		Hostname: "host-new",
		IP:       "192.168.1.100",
		Version:  "2.0.0",
		Labels:   map[string]string{"env": "prod"},
	}

	err = registry.RegisterOrHeartbeat(ctx, agent)
	require.NoError(t, err)

	// Verify agent was registered
	retrieved, err := registry.GetAgent(ctx, "agent-new")
	require.NoError(t, err)
	assert.Equal(t, "agent-new", retrieved.AgentID)
	assert.Equal(t, "host-new", retrieved.Hostname)
	assert.Equal(t, "192.168.1.100", retrieved.IP)
	assert.Equal(t, "2.0.0", retrieved.Version)
	assert.Equal(t, AgentStateOnline, retrieved.Status.State)
	assert.NotZero(t, retrieved.RegisteredAt)
}

func TestMemoryAgentRegistry_RegisterOrHeartbeat_ExistingAgent(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// First, register an agent
	agent := &AgentInfo{
		AgentID:  "agent-existing",
		AppID:    "app-test",
		Hostname: "host-old",
		IP:       "192.168.1.1",
		Version:  "1.0.0",
		Labels:   map[string]string{"env": "test"},
	}
	err = registry.Register(ctx, agent)
	require.NoError(t, err)

	retrieved, _ := registry.GetAgent(ctx, "agent-existing")
	initialHeartbeat := retrieved.LastHeartbeat
	initialRegisteredAt := retrieved.RegisteredAt

	// Wait a bit to ensure timestamp changes
	time.Sleep(10 * time.Millisecond)

	// RegisterOrHeartbeat with updated info
	updatedAgent := &AgentInfo{
		AgentID:  "agent-existing",
		AppID:    "app-test",
		Hostname: "host-new",
		IP:       "192.168.1.2",
		Version:  "2.0.0",
		Labels:   map[string]string{"env": "prod", "region": "us-west"},
		Status: &AgentStatus{
			ConfigVersion: "v2.0",
		},
	}

	err = registry.RegisterOrHeartbeat(ctx, updatedAgent)
	require.NoError(t, err)

	// Verify agent was updated, not re-registered
	retrieved, err = registry.GetAgent(ctx, "agent-existing")
	require.NoError(t, err)
	assert.Equal(t, "agent-existing", retrieved.AgentID)
	assert.Equal(t, "host-new", retrieved.Hostname)
	assert.Equal(t, "192.168.1.2", retrieved.IP)
	assert.Equal(t, "2.0.0", retrieved.Version)
	assert.Equal(t, "v2.0", retrieved.Status.ConfigVersion)
	assert.Equal(t, AgentStateOnline, retrieved.Status.State)

	// RegisteredAt should not change
	assert.Equal(t, initialRegisteredAt, retrieved.RegisteredAt)

	// LastHeartbeat should be updated
	assert.Greater(t, retrieved.LastHeartbeat, initialHeartbeat)

	// Labels should be updated
	assert.Equal(t, "prod", retrieved.Labels["env"])
	assert.Equal(t, "us-west", retrieved.Labels["region"])
}

func TestMemoryAgentRegistry_RegisterOrHeartbeat_Validation(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Nil agent
	err = registry.RegisterOrHeartbeat(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")

	// Empty agent_id
	err = registry.RegisterOrHeartbeat(ctx, &AgentInfo{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id")

	// Empty app_id
	err = registry.RegisterOrHeartbeat(ctx, &AgentInfo{AgentID: "agent-1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "app_id")
}

func TestMemoryAgentRegistry_Unregister(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agent
	agent := &AgentInfo{
		AgentID:  "agent-1",
		AppID:    "app-test",
		Hostname: "host-1",
		Labels:   map[string]string{"env": "test"},
	}
	err = registry.Register(ctx, agent)
	require.NoError(t, err)

	// Unregister
	err = registry.Unregister(ctx, "agent-1")
	require.NoError(t, err)

	// Verify agent is gone
	_, err = registry.GetAgent(ctx, "agent-1")
	assert.Error(t, err)

	// Unregister again should be idempotent
	err = registry.Unregister(ctx, "agent-1")
	require.NoError(t, err)
}

func TestMemoryAgentRegistry_GetOnlineAgents(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register multiple agents
	for i := 1; i <= 3; i++ {
		agent := &AgentInfo{
			AgentID:  "agent-" + string(rune('0'+i)),
			AppID:    "app-test",
			Hostname: "host-" + string(rune('0'+i)),
		}
		err = registry.Register(ctx, agent)
		require.NoError(t, err)
	}

	// Get online agents
	agents, err := registry.GetOnlineAgents(ctx)
	require.NoError(t, err)
	assert.Len(t, agents, 3)
}

func TestMemoryAgentRegistry_GetAllAgents(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register multiple agents
	for i := 1; i <= 3; i++ {
		agent := &AgentInfo{
			AgentID:  "agent-" + string(rune('0'+i)),
			AppID:    "app-test",
			Hostname: "host-" + string(rune('0'+i)),
		}
		err = registry.Register(ctx, agent)
		require.NoError(t, err)
	}

	// Mark one agent as offline
	agent1, _ := registry.GetAgent(ctx, "agent-1")
	agent1.Status.State = AgentStateOffline

	// GetOnlineAgents should return only online agents
	onlineAgents, err := registry.GetOnlineAgents(ctx)
	require.NoError(t, err)
	assert.Len(t, onlineAgents, 2)

	// GetAllAgents should return all agents including offline
	allAgents, err := registry.GetAllAgents(ctx)
	require.NoError(t, err)
	assert.Len(t, allAgents, 3)

	// Verify stats include offline count
	stats, err := registry.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.TotalAgents)
	assert.Equal(t, 2, stats.OnlineAgents)
	assert.Equal(t, 1, stats.OfflineAgents)
}

func TestMemoryAgentRegistry_GetAgentsByLabel(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agents with different labels
	agent1 := &AgentInfo{
		AgentID: "agent-1",
		AppID:   "app-test",
		Labels:  map[string]string{"env": "prod"},
	}
	agent2 := &AgentInfo{
		AgentID: "agent-2",
		AppID:   "app-test",
		Labels:  map[string]string{"env": "prod"},
	}
	agent3 := &AgentInfo{
		AgentID: "agent-3",
		AppID:   "app-test",
		Labels:  map[string]string{"env": "test"},
	}

	_ = registry.Register(ctx, agent1)
	_ = registry.Register(ctx, agent2)
	_ = registry.Register(ctx, agent3)

	// Get by label
	prodAgents, err := registry.GetAgentsByLabel(ctx, "env", "prod")
	require.NoError(t, err)
	assert.Len(t, prodAgents, 2)

	testAgents, err := registry.GetAgentsByLabel(ctx, "env", "test")
	require.NoError(t, err)
	assert.Len(t, testAgents, 1)

	// Non-existent label
	noAgents, err := registry.GetAgentsByLabel(ctx, "env", "staging")
	require.NoError(t, err)
	assert.Empty(t, noAgents)
}

func TestMemoryAgentRegistry_GetAgentStats(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agents
	agent1 := &AgentInfo{
		AgentID: "agent-1",
		AppID:   "app-test",
		Labels:  map[string]string{"env": "prod"},
		Status:  &AgentStatus{State: AgentStateOnline},
	}
	agent2 := &AgentInfo{
		AgentID: "agent-2",
		AppID:   "app-test",
		Labels:  map[string]string{"env": "prod"},
		Status:  &AgentStatus{State: AgentStateOnline},
	}

	_ = registry.Register(ctx, agent1)
	_ = registry.Register(ctx, agent2)

	// Get stats
	stats, err := registry.GetAgentStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalAgents)
	assert.Equal(t, 2, stats.OnlineAgents)
	assert.Equal(t, 0, stats.OfflineAgents)
}

func TestMemoryAgentRegistry_IsOnline(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agent
	agent := &AgentInfo{
		AgentID: "agent-1",
		AppID:   "app-test",
	}
	_ = registry.Register(ctx, agent)

	// Check online status
	online, err := registry.IsOnline(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, online)

	// Non-existent agent
	online, err = registry.IsOnline(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, online)
}

func TestMemoryAgentRegistry_UpdateHealth(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agent
	agent := &AgentInfo{
		AgentID: "agent-1",
		AppID:   "app-test",
	}
	_ = registry.Register(ctx, agent)

	// Update health
	health := &controlplanev1.HealthStatus{
		State: controlplanev1.HealthStateHealthy,
	}
	err = registry.UpdateHealth(ctx, "agent-1", health)
	require.NoError(t, err)

	// Verify
	retrieved, _ := registry.GetAgent(ctx, "agent-1")
	assert.Equal(t, controlplanev1.HealthStateHealthy, retrieved.Status.Health.State)
	assert.Equal(t, AgentStateOnline, retrieved.Status.State)

	// Update to unhealthy
	health.State = controlplanev1.HealthStateUnhealthy
	err = registry.UpdateHealth(ctx, "agent-1", health)
	require.NoError(t, err)

	retrieved, _ = registry.GetAgent(ctx, "agent-1")
	assert.Equal(t, AgentStateUnhealthy, retrieved.Status.State)
}

func TestMemoryAgentRegistry_SetCurrentTask(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	// Register agent
	agent := &AgentInfo{
		AgentID: "agent-1",
		AppID:   "app-test",
	}
	_ = registry.Register(ctx, agent)

	// Set current task
	err = registry.SetCurrentTask(ctx, "agent-1", "task-123")
	require.NoError(t, err)

	// Verify
	retrieved, _ := registry.GetAgent(ctx, "agent-1")
	assert.Equal(t, "task-123", retrieved.Status.CurrentTask)

	// Clear task
	err = registry.ClearCurrentTask(ctx, "agent-1")
	require.NoError(t, err)

	retrieved, _ = registry.GetAgent(ctx, "agent-1")
	assert.Empty(t, retrieved.Status.CurrentTask)
}

func TestMemoryAgentRegistry_StartClose(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	// Start
	err := registry.Start(ctx)
	require.NoError(t, err)

	// Double start should be idempotent
	err = registry.Start(ctx)
	require.NoError(t, err)

	// Close
	err = registry.Close()
	require.NoError(t, err)

	// Double close should be idempotent
	err = registry.Close()
	require.NoError(t, err)
}

func TestMemoryAgentRegistry_ConcurrentAccess(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	err := registry.Start(ctx)
	require.NoError(t, err)
	defer registry.Close()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			agent := &AgentInfo{
				AgentID:  "agent-" + string(rune('0'+id)),
				AppID:    "app-test",
				Hostname: "host",
			}
			_ = registry.Register(ctx, agent)
			_ = registry.Heartbeat(ctx, agent.AgentID, nil)
			_, _ = registry.GetAgent(ctx, agent.AgentID)
			_, _ = registry.GetOnlineAgents(ctx)
			_, _ = registry.GetAgentStats(ctx)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMemoryAgentRegistry_GetApps(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	// Register agents with different apps
	agents := []*AgentInfo{
		{AgentID: "agent-1", AppID: "app-a", ServiceName: "svc-1", Hostname: "host1", IP: "10.0.0.1"},
		{AgentID: "agent-2", AppID: "app-a", ServiceName: "svc-2", Hostname: "host2", IP: "10.0.0.2"},
		{AgentID: "agent-3", AppID: "app-b", ServiceName: "svc-1", Hostname: "host3", IP: "10.0.0.3"},
	}

	for _, agent := range agents {
		err := registry.Register(ctx, agent)
		require.NoError(t, err)
	}

	apps, err := registry.GetApps(ctx)
	require.NoError(t, err)
	assert.Len(t, apps, 2)
	assert.Contains(t, apps, "app-a")
	assert.Contains(t, apps, "app-b")
}

func TestMemoryAgentRegistry_GetServicesByApp(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	// Register agents
	agents := []*AgentInfo{
		{AgentID: "agent-1", AppID: "app-a", ServiceName: "svc-1", Hostname: "host1", IP: "10.0.0.1"},
		{AgentID: "agent-2", AppID: "app-a", ServiceName: "svc-2", Hostname: "host2", IP: "10.0.0.2"},
		{AgentID: "agent-3", AppID: "app-a", ServiceName: "svc-1", Hostname: "host3", IP: "10.0.0.3"},
		{AgentID: "agent-4", AppID: "app-b", ServiceName: "svc-3", Hostname: "host4", IP: "10.0.0.4"},
	}

	for _, agent := range agents {
		err := registry.Register(ctx, agent)
		require.NoError(t, err)
	}

	services, err := registry.GetServicesByApp(ctx, "app-a")
	require.NoError(t, err)
	assert.Len(t, services, 2)
	assert.Contains(t, services, "svc-1")
	assert.Contains(t, services, "svc-2")

	services, err = registry.GetServicesByApp(ctx, "app-b")
	require.NoError(t, err)
	assert.Len(t, services, 1)
	assert.Contains(t, services, "svc-3")

	services, err = registry.GetServicesByApp(ctx, "non-existent")
	require.NoError(t, err)
	assert.Len(t, services, 0)
}

func TestMemoryAgentRegistry_GetInstancesByService(t *testing.T) {
	registry := newTestMemoryRegistry(t)
	ctx := context.Background()

	// Register agents
	agents := []*AgentInfo{
		{AgentID: "agent-1", AppID: "app-a", ServiceName: "svc-1", Hostname: "host1", IP: "10.0.0.1"},
		{AgentID: "agent-2", AppID: "app-a", ServiceName: "svc-1", Hostname: "host2", IP: "10.0.0.2"},
		{AgentID: "agent-3", AppID: "app-a", ServiceName: "svc-2", Hostname: "host3", IP: "10.0.0.3"},
	}

	for _, agent := range agents {
		err := registry.Register(ctx, agent)
		require.NoError(t, err)
	}

	instances, err := registry.GetInstancesByService(ctx, "app-a", "svc-1")
	require.NoError(t, err)
	assert.Len(t, instances, 2)

	instances, err = registry.GetInstancesByService(ctx, "app-a", "svc-2")
	require.NoError(t, err)
	assert.Len(t, instances, 1)

	instances, err = registry.GetInstancesByService(ctx, "app-a", "non-existent")
	require.NoError(t, err)
	assert.Len(t, instances, 0)
}
