// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// AgentRegistry defines the interface for agent registration and status management.
type AgentRegistry interface {
	// Register registers a new agent.
	Register(ctx context.Context, agent *AgentInfo) error

	// Heartbeat updates the agent's heartbeat and status.
	Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error

	// Unregister removes an agent from the registry.
	Unregister(ctx context.Context, agentID string) error

	// GetAgent retrieves information about a specific agent.
	GetAgent(ctx context.Context, agentID string) (*AgentInfo, error)

	// GetOnlineAgents returns all online agents.
	GetOnlineAgents(ctx context.Context) ([]*AgentInfo, error)

	// GetAgentsByLabel returns agents matching the specified label.
	GetAgentsByLabel(ctx context.Context, labelKey, labelValue string) ([]*AgentInfo, error)

	// GetAgentsByToken returns all agents under a specific token/app.
	GetAgentsByToken(ctx context.Context, token string) ([]*AgentInfo, error)

	// GetAgentStats returns statistics about registered agents.
	GetAgentStats(ctx context.Context) (*AgentStats, error)

	// IsOnline checks if an agent is currently online.
	IsOnline(ctx context.Context, agentID string) (bool, error)

	// UpdateHealth updates an agent's health status.
	UpdateHealth(ctx context.Context, agentID string, health *controlplanev1.HealthStatus) error

	// SetCurrentTask sets the current task for an agent.
	SetCurrentTask(ctx context.Context, agentID string, taskID string) error

	// ClearCurrentTask clears the current task for an agent.
	ClearCurrentTask(ctx context.Context, agentID string) error

	// Start initializes the agent registry.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// AgentInfo contains complete information about an agent.
type AgentInfo struct {
	AgentID       string            `json:"agent_id"`
	Token         string            `json:"token"`           // Token/AppID for authentication
	AppID         string            `json:"app_id"`          // Associated app ID
	Hostname      string            `json:"hostname"`
	IP            string            `json:"ip"`
	Version       string            `json:"version"`
	StartTime     int64             `json:"start_time"`
	Labels        map[string]string `json:"labels"`
	Status        *AgentStatus      `json:"status"`
	RegisteredAt  int64             `json:"registered_at"`
	LastHeartbeat int64             `json:"last_heartbeat"`
}

// AgentStatus represents the current status of an agent.
type AgentStatus struct {
	State         AgentState                   `json:"state"`
	Health        *controlplanev1.HealthStatus `json:"health,omitempty"`
	CurrentTask   string                       `json:"current_task,omitempty"`
	ConfigVersion string                       `json:"config_version,omitempty"`
	Metrics       *AgentMetrics                `json:"metrics,omitempty"`
}

// AgentState represents the state of an agent.
type AgentState string

const (
	AgentStateOnline    AgentState = "online"
	AgentStateOffline   AgentState = "offline"
	AgentStateUnhealthy AgentState = "unhealthy"
)

// AgentMetrics contains runtime metrics for an agent.
type AgentMetrics struct {
	UptimeSeconds   int64   `json:"uptime_seconds"`
	TracesReceived  int64   `json:"traces_received"`
	MetricsReceived int64   `json:"metrics_received"`
	LogsReceived    int64   `json:"logs_received"`
	TasksCompleted  int64   `json:"tasks_completed"`
	TasksFailed     int64   `json:"tasks_failed"`
	MemoryUsageMB   float64 `json:"memory_usage_mb"`
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
}

// AgentStats contains statistics about registered agents.
type AgentStats struct {
	TotalAgents     int            `json:"total_agents"`
	OnlineAgents    int            `json:"online_agents"`
	OfflineAgents   int            `json:"offline_agents"`
	UnhealthyAgents int            `json:"unhealthy_agents"`
	ByLabel         map[string]int `json:"by_label,omitempty"`
}

// Config holds the configuration for AgentRegistry.
type Config struct {
	// Type specifies the backend type: "memory" or "redis"
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from storage extension
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for all Redis keys
	KeyPrefix string `mapstructure:"key_prefix"`

	// HeartbeatTTL is the TTL for agent info (should be ~3x heartbeat interval)
	HeartbeatTTL time.Duration `mapstructure:"heartbeat_ttl"`

	// OfflineCheckInterval is the interval for checking offline agents
	OfflineCheckInterval time.Duration `mapstructure:"offline_check_interval"`

	// EnableEvents enables publishing agent events via Pub/Sub
	EnableEvents bool `mapstructure:"enable_events"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:                 "memory",
		RedisName:            "default",
		KeyPrefix:            "otel:agents",
		HeartbeatTTL:         60 * time.Second,
		OfflineCheckInterval: 10 * time.Second,
		EnableEvents:         true,
	}
}
