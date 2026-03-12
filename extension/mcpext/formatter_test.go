// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
)

func TestFormatAgentList(t *testing.T) {
	agents := []*agentregistry.AgentInfo{
		{
			AgentID:     "agent-1",
			AppID:       "app-001",
			ServiceName: "order-service",
			IP:          "10.0.0.1",
			Hostname:    "pod-abc",
			Version:     "1.0.0",
			Status: &agentregistry.AgentStatus{
				State: agentregistry.AgentStateOnline,
			},
		},
		{
			AgentID:     "agent-2",
			AppID:       "app-001",
			ServiceName: "user-service",
			IP:          "10.0.0.2",
		},
	}

	result := formatAgentList(agents)

	assert.Contains(t, result, "共 2 个")
	assert.Contains(t, result, "agent-1")
	assert.Contains(t, result, "agent-2")
	assert.Contains(t, result, "order-service")
	assert.Contains(t, result, "user-service")
	assert.Contains(t, result, "10.0.0.1")
	assert.Contains(t, result, "online")
}

func TestFormatAgentList_Empty(t *testing.T) {
	result := formatAgentList([]*agentregistry.AgentInfo{})
	assert.Contains(t, result, "共 0 个")
}

func TestFormatAgentInfo(t *testing.T) {
	agent := &agentregistry.AgentInfo{
		AgentID:       "agent-1",
		AppID:         "app-001",
		ServiceName:   "order-service",
		IP:            "10.0.0.1",
		Hostname:      "pod-abc",
		Version:       "1.0.0",
		StartTime:     1710000000000,
		RegisteredAt:  1710000000000,
		LastHeartbeat: 1710000060000,
		Labels:        map[string]string{"env": "production", "region": "us-east"},
		Status: &agentregistry.AgentStatus{
			State:         agentregistry.AgentStateOnline,
			ConfigVersion: "v2",
			Health: &agentregistry.HealthStatus{
				State:       agentregistry.HealthStateHealthy,
				SuccessRate: 0.995,
			},
			Metrics: &agentregistry.AgentMetrics{
				UptimeSeconds:   86400,
				CPUUsagePercent: 12.5,
				MemoryUsageMB:   256.0,
				TracesReceived:  10000,
				TasksCompleted:  5,
			},
		},
	}

	result := formatAgentInfo(agent)

	assert.Contains(t, result, "agent-1")
	assert.Contains(t, result, "app-001")
	assert.Contains(t, result, "order-service")
	assert.Contains(t, result, "10.0.0.1")
	assert.Contains(t, result, "pod-abc")
	assert.Contains(t, result, "1.0.0")
	assert.Contains(t, result, "online")
	assert.Contains(t, result, "healthy")
	assert.Contains(t, result, "12.5%")
	assert.Contains(t, result, "256.0 MB")
	assert.Contains(t, result, "production")
	assert.Contains(t, result, "us-east")
}

func TestFormatAgentInfo_Minimal(t *testing.T) {
	agent := &agentregistry.AgentInfo{
		AgentID: "agent-minimal",
		AppID:   "app-001",
	}

	result := formatAgentInfo(agent)

	assert.Contains(t, result, "agent-minimal")
	assert.Contains(t, result, "app-001")
	// Should not panic with nil Status
	assert.NotContains(t, result, "运行状态")
}

func TestFormatArthasStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		message string
	}{
		{
			name:    "tunnel_registered",
			status:  "tunnel_registered",
			message: "Arthas 已连接到 Tunnel",
		},
		{
			name:    "not_attached",
			status:  "not_attached",
			message: "Arthas 未连接到 Tunnel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatArthasStatus("test-agent", tt.status, tt.message)
			assert.Contains(t, result, "test-agent")
			assert.Contains(t, result, tt.status)
			assert.Contains(t, result, tt.message)
		})
	}
}

func TestFormatDurationSeconds(t *testing.T) {
	tests := []struct {
		seconds  int64
		expected string
	}{
		{60, "1分钟"},
		{3600, "1小时0分钟"},
		{3661, "1小时1分钟"},
		{86400, "1天0小时0分钟"},
		{90061, "1天1小时1分钟"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDurationSeconds(tt.seconds)
			// Remove spaces for comparison
			assert.Equal(t, tt.expected, strings.TrimSpace(result))
		})
	}
}
