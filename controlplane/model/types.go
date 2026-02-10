// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package model defines the internal domain model for the custom controlplane.
//
// Design goal: keep business logic independent from any specific wire format
// (probe proto / legacy JSON types). Those conversions live in conv packages.
package model

import "encoding/json"

// SamplerType keeps numeric values aligned with both probe proto and legacy types.
//
// Values:
// - 0: unspecified
// - 1: always on
// - 2: always off
// - 3: trace id ratio
// - 4: parent based
// - 5: rule based
type SamplerType int32

const (
	SamplerTypeUnspecified  SamplerType = 0
	SamplerTypeAlwaysOn     SamplerType = 1
	SamplerTypeAlwaysOff    SamplerType = 2
	SamplerTypeTraceIDRatio SamplerType = 3
	SamplerTypeParentBased  SamplerType = 4
	SamplerTypeRuleBased    SamplerType = 5
)

// TaskStatus is a unified status that can represent both:
// - poll.proto stable set (PENDING/RUNNING/SUCCESS/FAILED/TIMEOUT/CANCELLED)
// - task.proto result set (SUCCESS/FAILED/TIMEOUT/CANCELLED/RESULT_TOO_LARGE)
//
// RESULT_TOO_LARGE is treated as a distinct status to avoid losing semantics.
// Legacy formats do not have this value; conversions may normalize it.
type TaskStatus int32

const (
	TaskStatusUnspecified    TaskStatus = 0
	TaskStatusPending        TaskStatus = 1
	TaskStatusRunning        TaskStatus = 2
	TaskStatusSuccess        TaskStatus = 3
	TaskStatusFailed         TaskStatus = 4
	TaskStatusTimeout        TaskStatus = 5
	TaskStatusCancelled      TaskStatus = 6
	TaskStatusResultTooLarge TaskStatus = 7
)

// CompressionType represents payload compression used in task results.
type CompressionType int32

const (
	CompressionTypeNone CompressionType = 0
	CompressionTypeGzip CompressionType = 1
)

// ChunkUploadStatus represents chunk upload state.
type ChunkUploadStatus int32

const (
	ChunkUploadStatusUnspecified      ChunkUploadStatus = 0
	ChunkUploadStatusChunkReceived    ChunkUploadStatus = 1
	ChunkUploadStatusUploadComplete   ChunkUploadStatus = 2
	ChunkUploadStatusChecksumMismatch ChunkUploadStatus = 3
	ChunkUploadStatusUploadFailed     ChunkUploadStatus = 4
)

// ConfigVersion is the unified configuration version info.
// TimestampMillis is optional and may be 0 if unavailable.
type ConfigVersion struct {
	Version         string `json:"version"`
	TimestampMillis int64  `json:"timestamp_millis,omitempty"`
	Etag            string `json:"etag,omitempty"`
}

// AgentConfig is the internal representation of agent configuration.
//
// Metadata fields (Version, UpdatedAt, Etag) are managed by the server.
type AgentConfig struct {
	Version                   string            `json:"version"`
	UpdatedAt                 int64             `json:"updated_at,omitempty"`
	Etag                      string            `json:"etag,omitempty"`
	ServerMetadata            map[string]string `json:"server_metadata,omitempty"`
	Sampler                   *SamplerConfig    `json:"sampler,omitempty"`
	Batch                     *BatchConfig      `json:"batch,omitempty"`
	DynamicResourceAttributes map[string]string `json:"dynamic_resource_attributes,omitempty"`
	ExtensionConfigJSON       string            `json:"extension_config_json,omitempty"`
}

type SamplerConfig struct {
	Type      SamplerType   `json:"type"`
	Ratio     float64       `json:"ratio"`
	Rules     []SamplerRule `json:"rules,omitempty"`
	RulesJSON string        `json:"rules_json,omitempty"`
}

type SamplerRule struct {
	Name            string            `json:"name"`
	SpanNamePattern string            `json:"span_name_pattern,omitempty"`
	AttributeMatch  map[string]string `json:"attribute_match,omitempty"`
	Ratio           float64           `json:"ratio"`
	Priority        int32             `json:"priority,omitempty"`
}

type BatchConfig struct {
	MaxExportBatchSize  int32 `json:"max_export_batch_size"`
	MaxQueueSize        int32 `json:"max_queue_size"`
	ScheduleDelayMillis int64 `json:"schedule_delay_millis"`
	ExportTimeoutMillis int64 `json:"export_timeout_millis"`
}

// Task is the internal task model.
// ParametersJSON is raw JSON bytes (not escaped), allowing lossless storage.
type Task struct {
	ID                       string          `json:"task_id"`
	TypeName                 string          `json:"task_type_name"`
	ParametersJSON           json.RawMessage `json:"parameters_json,omitempty"`
	PriorityNum              int32           `json:"priority_num,omitempty"`
	TimeoutMillis            int64           `json:"timeout_millis,omitempty"`
	CreatedAtMillis          int64           `json:"created_at_millis,omitempty"`
	ExpiresAtMillis          int64           `json:"expires_at_millis,omitempty"`
	MaxAcceptableDelayMillis int64           `json:"max_acceptable_delay_millis,omitempty"`
	TargetAgentID            string          `json:"target_agent_id,omitempty"`
}

// TaskResult is the internal representation of a task execution.
//
// ResultJSON is raw JSON bytes. When sourced from probe/legacy DTO strings, we
// store the bytes as-is (no validation) to preserve round-trips.
type TaskResult struct {
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id,omitempty"`

	Status TaskStatus `json:"status"`

	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	ResultJSON json.RawMessage `json:"result_json,omitempty"`
	ResultData []byte          `json:"result_data,omitempty"`

	StartedAtMillis     int64 `json:"started_at_millis,omitempty"`
	CompletedAtMillis   int64 `json:"completed_at_millis,omitempty"`
	ExecutionTimeMillis int64 `json:"execution_time_millis,omitempty"`

	// Extended fields (probe task.proto)
	ResultDataType string          `json:"result_data_type,omitempty"`
	RetryCount     int32           `json:"retry_count,omitempty"`
	Compression    CompressionType `json:"compression,omitempty"`
	OriginalSize   int64           `json:"original_size,omitempty"`
	CompressedSize int64           `json:"compressed_size,omitempty"`

	// Artifact fields — set when chunked upload is persisted to BlobStore.
	// ArtifactRef is the blob key (e.g., "artifacts/<taskID>").
	ArtifactRef  string `json:"artifact_ref,omitempty"`
	ArtifactSize int64  `json:"artifact_size,omitempty"`
}

// ChunkUpload is the internal chunk upload request model.
type ChunkUpload struct {
	TaskID        string `json:"task_id"`
	UploadID      string `json:"upload_id,omitempty"`
	ChunkIndex    int32  `json:"chunk_index"`
	TotalChunks   int32  `json:"total_chunks"`
	ChunkData     []byte `json:"chunk_data"`
	ChunkChecksum string `json:"chunk_checksum,omitempty"`
	IsLastChunk   bool   `json:"is_last_chunk,omitempty"`
}

// ChunkUploadResponse is the internal response model.
type ChunkUploadResponse struct {
	UploadID           string            `json:"upload_id,omitempty"`
	ReceivedChunkIndex int32             `json:"received_chunk_index,omitempty"`
	Status             ChunkUploadStatus `json:"status"`
	ErrorMessage       string            `json:"error_message,omitempty"`
}
