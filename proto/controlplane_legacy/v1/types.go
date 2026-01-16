// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package controlplanev1 contains the protocol buffer types for the control plane API.
// These types are manually defined to match the proto definitions.
// In production, these would be generated using protoc.
package controlplanev1

import (
	"encoding/json"
	"strings"
)

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

// IsDispatchable returns true if the task with this status should be dispatched to agents.
// Only PENDING and RUNNING tasks need to be dispatched:
// - PENDING: task waiting to be picked up
// - RUNNING: task may need to be re-dispatched (e.g., agent reconnected)
// Terminal states (SUCCESS/FAILED/TIMEOUT/CANCELLED) should NOT be dispatched.
func (s TaskStatus) IsDispatchable() bool {
	return s == TaskStatusPending || s == TaskStatusRunning
}

// IsTerminal returns true if the task has reached a final state.
// Terminal tasks should be removed from pending queues.
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case TaskStatusSuccess, TaskStatusFailed, TaskStatusTimeout, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// UnmarshalJSON supports both number and string formats for TaskStatus.
//
// We keep the wire format flexible to accommodate agents that send status as strings
// (e.g. "FAILED", "STALE", "EXPIRED"). The server will normalize these into
// stable TaskStatus values.
func (s *TaskStatus) UnmarshalJSON(data []byte) error {
	// Try number first
	var num int32
	if err := json.Unmarshal(data, &num); err == nil {
		*s = TaskStatus(num)
		return nil
	}

	// Then try string
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}

	switch strings.ToUpper(str) {
	case "SUCCESS":
		*s = TaskStatusSuccess
	case "FAILED", "FAILURE":
		*s = TaskStatusFailed
	case "TIMEOUT":
		*s = TaskStatusTimeout
	case "CANCELLED", "CANCELED":
		*s = TaskStatusCancelled
	case "PENDING":
		*s = TaskStatusPending
	case "RUNNING":
		*s = TaskStatusRunning
	case "EXPIRED", "STALE", "REJECTED":
		// These are failure reasons; normalize to FAILED.
		*s = TaskStatusFailed
	default:
		*s = TaskStatusUnspecified
	}
	return nil
}

// MarshalJSON encodes TaskStatus as a number.
func (s TaskStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(int32(s))
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
	TaskID                   string         `json:"task_id"`
	TaskType                 string         `json:"task_type"`
	Parameters               map[string]any `json:"parameters,omitempty"`
	Priority                 int32          `json:"priority"`
	TimeoutMillis            int64          `json:"timeout_millis"`
	CreatedAtMillis          int64          `json:"created_at_millis"`
	ExpiresAtMillis          int64          `json:"expires_at_millis,omitempty"`
	MaxAcceptableDelayMillis int64          `json:"max_acceptable_delay_millis,omitempty"`
	TargetAgentID            string         `json:"target_agent_id,omitempty"`
}

// TaskResult contains the outcome of a task execution.
//
// NOTE: This struct is used for JSON APIs. Keep fields backward-compatible.
// Agents may send additional fields; the server will ignore unknown fields.
type TaskResult struct {
	TaskID string `json:"task_id"`

	// AgentID is optional but highly recommended for observability.
	AgentID string `json:"agent_id,omitempty"`

	// Status is the normalized execution status.
	// We recommend agents use stable values: SUCCESS/FAILED/TIMEOUT/CANCELLED.
	Status TaskStatus `json:"status"`

	// ErrorCode provides machine-readable failure reason, e.g. TASK_STALE/TASK_EXPIRED.
	ErrorCode string `json:"error_code,omitempty"`

	// ErrorMessage provides human-readable details.
	ErrorMessage string `json:"error_message,omitempty"`

	// Result is optional structured JSON produced by the agent (already JSON, not escaped).
	Result json.RawMessage `json:"result,omitempty"`

	// ResultData is optional binary payload (base64 in JSON).
	ResultData []byte `json:"result_data,omitempty"`

	StartedAtMillis      int64 `json:"started_at_millis,omitempty"`
	CompletedAtMillis    int64 `json:"completed_at_millis"`
	ExecutionTimeMillis  int64 `json:"execution_time_millis,omitempty"`
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
