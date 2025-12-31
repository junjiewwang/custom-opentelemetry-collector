// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"errors"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// AgentRegistry defines the interface for agent registration and status management.
type AgentRegistry interface {
	// Register registers a new agent.
	Register(ctx context.Context, agent *AgentInfo) error

	// Heartbeat updates the agent's heartbeat and status.
	Heartbeat(ctx context.Context, agentID string, status *AgentStatus) error

	// RegisterOrHeartbeat registers a new agent if not exists, or updates heartbeat if exists.
	// This provides upsert semantics for automatic registration via status reports.
	RegisterOrHeartbeat(ctx context.Context, agent *AgentInfo) error

	// Unregister removes an agent from the registry.
	Unregister(ctx context.Context, agentID string) error

	// GetAgent retrieves information about a specific agent.
	GetAgent(ctx context.Context, agentID string) (*AgentInfo, error)

	// GetAllAgents returns all agents (including offline ones within InstanceTTL).
	// This is useful for statistics that need to count offline agents.
	GetAllAgents(ctx context.Context) ([]*AgentInfo, error)

	// GetOnlineAgents returns all online agents.
	GetOnlineAgents(ctx context.Context) ([]*AgentInfo, error)

	// GetAgentsByLabel returns agents matching the specified label.
	GetAgentsByLabel(ctx context.Context, labelKey, labelValue string) ([]*AgentInfo, error)

	// GetAgentsByToken returns all agents under a specific token/app.
	GetAgentsByToken(ctx context.Context, token string) ([]*AgentInfo, error)

	// GetApps returns all app IDs.
	GetApps(ctx context.Context) ([]string, error)

	// GetServicesByApp returns all service names under an app.
	GetServicesByApp(ctx context.Context, appID string) ([]string, error)

	// GetInstancesByService returns all instances under a service.
	GetInstancesByService(ctx context.Context, appID, serviceName string) ([]*AgentInfo, error)

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
	AgentID     string            `json:"agent_id"`
	Token       string            `json:"token"`        // Token for authentication
	AppID       string            `json:"app_id"`       // Application ID (App = AppGroup, 1:1 relationship)
	ServiceName string            `json:"service_name"` // Service name within the app
	Hostname    string            `json:"hostname"`
	IP          string            `json:"ip"`
	Version     string            `json:"version"`
	StartTime   int64             `json:"start_time"`
	Labels      map[string]string `json:"labels"`
	Status      *AgentStatus      `json:"status"`
	RegisteredAt  int64           `json:"registered_at"`
	LastHeartbeat int64           `json:"last_heartbeat"`

	// Deprecated: Use AppID instead. Kept for backward compatibility with existing data.
	AppGroupID string `json:"app_group_id,omitempty"`
}

// Validate validates the agent info and sets default values for optional fields.
// Returns an error if required fields are missing.
func (a *AgentInfo) Validate() error {
	if a == nil {
		return errors.New("agent cannot be nil")
	}
	if a.AgentID == "" {
		return errors.New("agent_id is required")
	}
	// AppID must be set from token validation. If empty, it means token validation
	// failed or token was not provided, so we reject the registration.
	if a.AppID == "" {
		return errors.New("app_id is required (must be obtained from token validation)")
	}
	// ServiceName is optional during registration as it may be populated later
	// or derived from other sources. Use default if empty to avoid invalid storage keys.
	if a.ServiceName == "" {
		a.ServiceName = "_unknown"
	}
	return nil
}

// MergeFrom merges non-empty fields from another AgentInfo into this one.
// This is used for updating existing agent info with new values.
func (a *AgentInfo) MergeFrom(other *AgentInfo) {
	if other == nil {
		return
	}
	if other.Hostname != "" {
		a.Hostname = other.Hostname
	}
	if other.IP != "" {
		a.IP = other.IP
	}
	if other.Version != "" {
		a.Version = other.Version
	}
	if other.Token != "" {
		a.Token = other.Token
	}
	if other.AppID != "" {
		a.AppID = other.AppID
	}
	if other.ServiceName != "" {
		a.ServiceName = other.ServiceName
	}
	if other.StartTime != 0 {
		a.StartTime = other.StartTime
	}
	if len(other.Labels) > 0 {
		a.Labels = other.Labels
	}
}

// AgentStatus represents the current status of an agent.
type AgentStatus struct {
	State          AgentState                   `json:"state"`
	StateChangedAt int64                        `json:"state_changed_at,omitempty"` // Timestamp when state last changed
	Health         *controlplanev1.HealthStatus `json:"health,omitempty"`
	CurrentTask    string                       `json:"current_task,omitempty"`
	ConfigVersion  string                       `json:"config_version,omitempty"`
	Metrics        *AgentMetrics                `json:"metrics,omitempty"`
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

	// InstanceTTL is the TTL for agent instance info (for historical queries).
	// This determines how long agent info is retained after going offline.
	// Default: 24h
	InstanceTTL time.Duration `mapstructure:"instance_ttl"`

	// HeartbeatTTL is the TTL for heartbeat keys (for online status detection).
	// When a heartbeat key expires, the agent is considered offline.
	// Should be 2-3x the expected heartbeat interval from agents.
	// Default: 30s (assuming 10-15s heartbeat interval)
	HeartbeatTTL time.Duration `mapstructure:"heartbeat_ttl"`

	// OfflineCheckInterval is the interval for checking offline agents
	OfflineCheckInterval time.Duration `mapstructure:"offline_check_interval"`

	// EnableEvents enables publishing agent events via Pub/Sub
	EnableEvents bool `mapstructure:"enable_events"`

	// EnableHierarchyIndex enables App -> Service -> Instance hierarchy indexes.
	// When disabled, only the essential keys are created:
	//   - Instance info key (with InstanceTTL)
	//   - Heartbeat key (with HeartbeatTTL)
	//   - Agent ID index in _ids hash
	//   - Online sorted set
	// When enabled, additional index keys are created:
	//   - _apps set
	//   - {app}:_services set
	//   - {app}/{service}:_instances set
	// Disable this if you don't need GetApps/GetServicesByApp/GetInstancesByService queries.
	// Default: true
	EnableHierarchyIndex bool `mapstructure:"enable_hierarchy_index"`

	// InstanceKeyMode controls how instance keys are generated.
	// - "host": Use hostname only: app/{appID}/svc/{svc}/host/{host}
	// - "host_ip": Use hostname and IP: app/{appID}/svc/{svc}/host/{host}/ip/{ip}
	// Use "host" when each hostname is unique per service.
	// Use "host_ip" when multiple instances may run on the same host (e.g., containers).
	// Default: "host"
	InstanceKeyMode string `mapstructure:"instance_key_mode"`
}

// Instance key mode constants
const (
	// InstanceKeyModeHost uses hostname only for instance identification.
	// Format: app/{appID}/svc/{svc}/host/{host}
	InstanceKeyModeHost = "host"

	// InstanceKeyModeHostIP uses hostname and IP for instance identification.
	// Format: app/{appID}/svc/{svc}/host/{host}/ip/{ip}
	InstanceKeyModeHostIP = "host_ip"
)

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:                 "memory",
		RedisName:            "default",
		KeyPrefix:            "otel:agents",
		InstanceTTL:          24 * time.Hour,   // Keep instance info for 24 hours
		HeartbeatTTL:         30 * time.Second, // Heartbeat key TTL (2-3x heartbeat interval)
		OfflineCheckInterval: 10 * time.Second,
		EnableEvents:         true,
		EnableHierarchyIndex: true,               // Enable by default for backward compatibility
		InstanceKeyMode:      InstanceKeyModeHost, // Use simpler format by default
	}
}
