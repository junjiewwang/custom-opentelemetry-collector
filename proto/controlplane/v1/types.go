// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package controlplanev1 contains the protocol buffer types for the control plane API.
// These types are manually defined to match the proto definitions.
// In production, these would be generated using protoc.
package controlplanev1

// SamplerType enumerates available sampler types.
type SamplerType int32

const (
	SamplerTypeUnspecified  SamplerType = 0
	SamplerTypeAlwaysOn     SamplerType = 1
	SamplerTypeAlwaysOff    SamplerType = 2
	SamplerTypeTraceIDRatio SamplerType = 3
	SamplerTypeParentBased  SamplerType = 4
	SamplerTypeRuleBased    SamplerType = 5
)

func (s SamplerType) String() string {
	switch s {
	case SamplerTypeAlwaysOn:
		return "ALWAYS_ON"
	case SamplerTypeAlwaysOff:
		return "ALWAYS_OFF"
	case SamplerTypeTraceIDRatio:
		return "TRACE_ID_RATIO"
	case SamplerTypeParentBased:
		return "PARENT_BASED"
	case SamplerTypeRuleBased:
		return "RULE_BASED"
	default:
		return "UNSPECIFIED"
	}
}

// TaskStatus enumerates possible task outcomes.
type TaskStatus int32

const (
	TaskStatusUnspecified TaskStatus = 0
	TaskStatusSuccess     TaskStatus = 1
	TaskStatusFailed      TaskStatus = 2
	TaskStatusTimeout     TaskStatus = 3
	TaskStatusCancelled   TaskStatus = 4
	TaskStatusPending     TaskStatus = 5
	TaskStatusRunning     TaskStatus = 6
)

func (s TaskStatus) String() string {
	switch s {
	case TaskStatusSuccess:
		return "SUCCESS"
	case TaskStatusFailed:
		return "FAILED"
	case TaskStatusTimeout:
		return "TIMEOUT"
	case TaskStatusCancelled:
		return "CANCELLED"
	case TaskStatusPending:
		return "PENDING"
	case TaskStatusRunning:
		return "RUNNING"
	default:
		return "UNSPECIFIED"
	}
}

// HealthState enumerates possible health states.
type HealthState int32

const (
	HealthStateUnspecified HealthState = 0
	HealthStateHealthy     HealthState = 1
	HealthStateDegraded    HealthState = 2
	HealthStateUnhealthy   HealthState = 3
)

func (s HealthState) String() string {
	switch s {
	case HealthStateHealthy:
		return "HEALTHY"
	case HealthStateDegraded:
		return "DEGRADED"
	case HealthStateUnhealthy:
		return "UNHEALTHY"
	default:
		return "UNSPECIFIED"
	}
}

// SamplerConfig defines sampling behavior.
type SamplerConfig struct {
	Type      SamplerType `json:"type"`
	Ratio     float64     `json:"ratio"`
	RulesJSON string      `json:"rules_json,omitempty"`
}

// BatchConfig defines batch processor settings.
type BatchConfig struct {
	MaxExportBatchSize  int32 `json:"max_export_batch_size"`
	MaxQueueSize        int32 `json:"max_queue_size"`
	ScheduleDelayMillis int64 `json:"schedule_delay_millis"`
	ExportTimeoutMillis int64 `json:"export_timeout_millis"`
}

// AgentConfig represents the configuration to be pushed to agents.
type AgentConfig struct {
	ConfigVersion              string            `json:"config_version"`
	Sampler                    *SamplerConfig    `json:"sampler,omitempty"`
	Batch                      *BatchConfig      `json:"batch,omitempty"`
	DynamicResourceAttributes  map[string]string `json:"dynamic_resource_attributes,omitempty"`
	ExtensionConfigJSON        string            `json:"extension_config_json,omitempty"`
}

// Task represents a command to be executed by the agent.
type Task struct {
	TaskID            string `json:"task_id"`
	TaskType          string `json:"task_type"`
	ParametersJSON    string `json:"parameters_json,omitempty"`
	Priority          int32  `json:"priority"`
	TimeoutMillis     int64  `json:"timeout_millis"`
	CreatedAtUnixNano int64  `json:"created_at_unix_nano"`
	ExpiresAtUnixNano int64  `json:"expires_at_unix_nano,omitempty"`
}

// TaskResult contains the outcome of a task execution.
type TaskResult struct {
	TaskID              string     `json:"task_id"`
	Status              TaskStatus `json:"status"`
	ResultData          []byte     `json:"result_data,omitempty"`
	ErrorMessage        string     `json:"error_message,omitempty"`
	CompletedAtUnixNano int64      `json:"completed_at_unix_nano"`
}

// HealthStatus describes the agent's health.
type HealthStatus struct {
	State                HealthState `json:"state"`
	SuccessRate          float64     `json:"success_rate"`
	SuccessCount         int64       `json:"success_count"`
	FailureCount         int64       `json:"failure_count"`
	CurrentConfigVersion string      `json:"current_config_version"`
}

// AgentStatus represents the current state of an agent.
type AgentStatus struct {
	AgentID           string            `json:"agent_id"`
	TimestampUnixNano int64             `json:"timestamp_unix_nano"`
	Health            *HealthStatus     `json:"health,omitempty"`
	CompletedTasks    []*TaskResult     `json:"completed_tasks,omitempty"`
	Metrics           map[string]string `json:"metrics,omitempty"`

	// Agent identification fields for auto-registration
	Hostname  string            `json:"hostname,omitempty"`
	IP        string            `json:"ip,omitempty"`
	Version   string            `json:"version,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	StartTime int64             `json:"start_time,omitempty"`
}

// ConfigRequest is sent by the control plane to update agent configuration.
type ConfigRequest struct {
	Config *AgentConfig `json:"config"`
}

// ConfigResponse is returned after processing a configuration update or fetch.
type ConfigResponse struct {
	Success        bool         `json:"success"`
	Message        string       `json:"message,omitempty"`
	AppliedVersion string       `json:"applied_version,omitempty"`
	Config         *AgentConfig `json:"config,omitempty"` // Current config (for fetch requests)
}

// TaskRequest is sent to submit a new task.
type TaskRequest struct {
	Task *Task `json:"task"`
}

// TaskResponse is returned after task submission.
type TaskResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
}

// TaskResultRequest is sent to query task results.
type TaskResultRequest struct {
	TaskID string `json:"task_id"`
}

// TaskResultResponse returns task execution results.
type TaskResultResponse struct {
	Found  bool        `json:"found"`
	Result *TaskResult `json:"result,omitempty"`
}

// StatusRequest is sent to report agent status.
type StatusRequest struct {
	Status *AgentStatus `json:"status"`
}

// StatusResponse is returned after status report.
type StatusResponse struct {
	Received     bool    `json:"received"`
	Message      string  `json:"message,omitempty"`
	PendingTasks []*Task `json:"pending_tasks,omitempty"`
}

// UploadChunkRequest is sent to upload large data in chunks.
type UploadChunkRequest struct {
	TaskID      string `json:"task_id"`
	ChunkIndex  int32  `json:"chunk_index"`
	TotalChunks int32  `json:"total_chunks"`
	Data        []byte `json:"data"`
	Checksum    string `json:"checksum,omitempty"`
}

// UploadChunkResponse is returned after chunk upload.
type UploadChunkResponse struct {
	Success        bool   `json:"success"`
	Message        string `json:"message,omitempty"`
	ChunksReceived int32  `json:"chunks_received"`
	Complete       bool   `json:"complete"`
}
