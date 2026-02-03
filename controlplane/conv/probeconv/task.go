// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package probeconv

import (
	"encoding/json"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func TaskFromProto(t *controlplanev1.Task) *model.Task {
	if t == nil {
		return nil
	}

	out := &model.Task{
		ID:                       t.GetTaskId(),
		TypeName:                 t.GetTaskTypeName(),
		PriorityNum:              priorityNumFromProto(t),
		TimeoutMillis:            t.GetTimeoutMillis(),
		CreatedAtMillis:          t.GetCreatedAtMillis(),
		ExpiresAtMillis:          t.GetExpiresAtMillis(),
		MaxAcceptableDelayMillis: t.GetMaxAcceptableDelayMillis(),
	}

	if t.GetParametersJson() != "" {
		out.ParametersJSON = json.RawMessage(t.GetParametersJson())
	}

	return out
}

func TasksFromProto(tasks []*controlplanev1.Task) []*model.Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]*model.Task, 0, len(tasks))
	for _, t := range tasks {
		if t == nil {
			continue
		}
		out = append(out, TaskFromProto(t))
	}
	return out
}

func TaskToProto(t *model.Task) *controlplanev1.Task {
	if t == nil {
		return nil
	}

	out := &controlplanev1.Task{
		TaskId:                   t.ID,
		Type:                     controlplanev1.Task_TASK_TYPE_CUSTOM,
		TaskTypeName:             t.TypeName,
		ParametersJson:           string(t.ParametersJSON),
		Priority:                 priorityEnumFromNum(t.PriorityNum),
		PriorityNum:              t.PriorityNum,
		TimeoutMillis:            t.TimeoutMillis,
		CreatedAtMillis:          t.CreatedAtMillis,
		ExpiresAtMillis:          t.ExpiresAtMillis,
		MaxAcceptableDelayMillis: t.MaxAcceptableDelayMillis,
	}
	return out
}

func TasksToProto(tasks []*model.Task) []*controlplanev1.Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]*controlplanev1.Task, 0, len(tasks))
	for _, t := range tasks {
		if t == nil {
			continue
		}
		out = append(out, TaskToProto(t))
	}
	return out
}

func priorityNumFromProto(t *controlplanev1.Task) int32 {
	if t == nil {
		return 0
	}
	if t.GetPriorityNum() != 0 {
		return t.GetPriorityNum()
	}
	// Fallback from enum -> approximate numeric (legacy convention)
	switch t.GetPriority() {
	case controlplanev1.Task_PRIORITY_LOW:
		return 1
	case controlplanev1.Task_PRIORITY_HIGH:
		return 3
	case controlplanev1.Task_PRIORITY_NORMAL:
		return 2
	default:
		return 0
	}
}

func priorityEnumFromNum(priority int32) controlplanev1.Task_Priority {
	switch {
	case priority <= 0:
		return controlplanev1.Task_PRIORITY_UNSPECIFIED
	case priority <= 1:
		return controlplanev1.Task_PRIORITY_LOW
	case priority >= 3:
		return controlplanev1.Task_PRIORITY_HIGH
	default:
		return controlplanev1.Task_PRIORITY_NORMAL
	}
}

// TaskResultFromTaskProto converts TaskResult (task.proto) to model.TaskResult.
// Uses the global TaskStatus enum.
func TaskResultFromTaskProto(tr *controlplanev1.TaskResult, agentID string) *model.TaskResult {
	if tr == nil {
		return nil
	}
	out := &model.TaskResult{
		TaskID:              tr.GetTaskId(),
		AgentID:             agentID,
		Status:              TaskStatusFromProto(tr.GetStatus()),
		ErrorCode:           tr.GetErrorCode(),
		ErrorMessage:        tr.GetErrorMessage(),
		ResultData:          tr.GetResultData(),
		StartedAtMillis:     tr.GetStartedAtMillis(),
		CompletedAtMillis:   tr.GetCompletedAtMillis(),
		ExecutionTimeMillis: tr.GetExecutionTimeMillis(),
		ResultDataType:      tr.GetResultDataType(),
		RetryCount:          tr.GetRetryCount(),
		Compression:         CompressionFromProto(tr.GetCompression()),
		OriginalSize:        tr.GetOriginalSize(),
		CompressedSize:      tr.GetCompressedSize(),
	}
	if tr.GetResultJson() != "" {
		out.ResultJSON = json.RawMessage(tr.GetResultJson())
	}
	return out
}

// TaskResultToTaskProto converts model.TaskResult to TaskResult (task.proto).
// Uses the global TaskStatus enum.
func TaskResultToTaskProto(tr *model.TaskResult) *controlplanev1.TaskResult {
	if tr == nil {
		return nil
	}

	return &controlplanev1.TaskResult{
		TaskId:              tr.TaskID,
		Status:              TaskStatusToProto(tr.Status),
		StartedAtMillis:     tr.StartedAtMillis,
		CompletedAtMillis:   tr.CompletedAtMillis,
		ExecutionTimeMillis: tr.ExecutionTimeMillis,
		ResultData:          tr.ResultData,
		ResultJson:          string(tr.ResultJSON),
		ResultDataType:      tr.ResultDataType,
		ErrorCode:           tr.ErrorCode,
		ErrorMessage:        tr.ErrorMessage,
		RetryCount:          tr.RetryCount,
		Compression:         CompressionToProto(tr.Compression),
		OriginalSize:        tr.OriginalSize,
		CompressedSize:      tr.CompressedSize,
	}
}

// TaskResultRequestToProto converts model.TaskResult to TaskResultRequest (poll.proto).
// Uses the new simplified structure that embeds TaskResult.
func TaskResultRequestToProto(tr *model.TaskResult, agentID string) *controlplanev1.TaskResultRequest {
	if tr == nil {
		return nil
	}
	return &controlplanev1.TaskResultRequest{
		AgentId: agentID,
		Result:  TaskResultToTaskProto(tr),
	}
}

// TaskStatusFromProto converts global TaskStatus enum to model.TaskStatus.
func TaskStatusFromProto(st controlplanev1.TaskStatus) model.TaskStatus {
	switch st {
	case controlplanev1.TaskStatus_TASK_STATUS_PENDING:
		return model.TaskStatusPending
	case controlplanev1.TaskStatus_TASK_STATUS_RUNNING:
		return model.TaskStatusRunning
	case controlplanev1.TaskStatus_TASK_STATUS_SUCCESS:
		return model.TaskStatusSuccess
	case controlplanev1.TaskStatus_TASK_STATUS_FAILED:
		return model.TaskStatusFailed
	case controlplanev1.TaskStatus_TASK_STATUS_TIMEOUT:
		return model.TaskStatusTimeout
	case controlplanev1.TaskStatus_TASK_STATUS_CANCELLED:
		return model.TaskStatusCancelled
	case controlplanev1.TaskStatus_TASK_STATUS_RESULT_TOO_LARGE:
		return model.TaskStatusResultTooLarge
	default:
		return model.TaskStatusUnspecified
	}
}

// TaskStatusToProto converts model.TaskStatus to global TaskStatus enum.
func TaskStatusToProto(st model.TaskStatus) controlplanev1.TaskStatus {
	switch st {
	case model.TaskStatusPending:
		return controlplanev1.TaskStatus_TASK_STATUS_PENDING
	case model.TaskStatusRunning:
		return controlplanev1.TaskStatus_TASK_STATUS_RUNNING
	case model.TaskStatusSuccess:
		return controlplanev1.TaskStatus_TASK_STATUS_SUCCESS
	case model.TaskStatusFailed:
		return controlplanev1.TaskStatus_TASK_STATUS_FAILED
	case model.TaskStatusTimeout:
		return controlplanev1.TaskStatus_TASK_STATUS_TIMEOUT
	case model.TaskStatusCancelled:
		return controlplanev1.TaskStatus_TASK_STATUS_CANCELLED
	case model.TaskStatusResultTooLarge:
		return controlplanev1.TaskStatus_TASK_STATUS_RESULT_TOO_LARGE
	default:
		return controlplanev1.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}

// CompressionFromProto converts global CompressionType enum to model.CompressionType.
func CompressionFromProto(c controlplanev1.CompressionType) model.CompressionType {
	switch c {
	case controlplanev1.CompressionType_COMPRESSION_TYPE_GZIP:
		return model.CompressionTypeGzip
	default:
		return model.CompressionTypeNone
	}
}

// CompressionToProto converts model.CompressionType to global CompressionType enum.
func CompressionToProto(c model.CompressionType) controlplanev1.CompressionType {
	switch c {
	case model.CompressionTypeGzip:
		return controlplanev1.CompressionType_COMPRESSION_TYPE_GZIP
	default:
		return controlplanev1.CompressionType_COMPRESSION_TYPE_NONE
	}
}
