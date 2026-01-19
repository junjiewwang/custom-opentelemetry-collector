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

func TaskResultFromPollProto(req *controlplanev1.TaskResultRequest, agentID string) *model.TaskResult {
	if req == nil {
		return nil
	}
	out := &model.TaskResult{
		TaskID:              req.GetTaskId(),
		AgentID:             agentID,
		Status:              taskStatusFromPoll(req.GetStatus()),
		ErrorCode:           req.GetErrorCode(),
		ErrorMessage:        req.GetErrorMessage(),
		ResultData:          req.GetResultData(),
		StartedAtMillis:     req.GetStartedAtMillis(),
		CompletedAtMillis:   req.GetCompletedAtMillis(),
		ExecutionTimeMillis: req.GetExecutionTimeMillis(),
	}
	if req.GetResultJson() != "" {
		out.ResultJSON = json.RawMessage(req.GetResultJson())
	}
	return out
}

func TaskResultFromTaskProto(tr *controlplanev1.TaskResult, agentID string) *model.TaskResult {
	if tr == nil {
		return nil
	}
	out := &model.TaskResult{
		TaskID:              tr.GetTaskId(),
		AgentID:             agentID,
		Status:              taskStatusFromTask(tr.GetStatus()),
		ErrorCode:           tr.GetErrorCode(),
		ErrorMessage:        tr.GetErrorMessage(),
		ResultData:          tr.GetResultData(),
		StartedAtMillis:     tr.GetStartedAtMillis(),
		CompletedAtMillis:   tr.GetCompletedAtMillis(),
		ExecutionTimeMillis: tr.GetExecutionTimeMillis(),
		ResultDataType:      tr.GetResultDataType(),
		RetryCount:          tr.GetRetryCount(),
		Compression:         compressionFromProto(tr.GetCompression()),
		OriginalSize:        tr.GetOriginalSize(),
		CompressedSize:      tr.GetCompressedSize(),
	}
	if tr.GetResultJson() != "" {
		out.ResultJSON = json.RawMessage(tr.GetResultJson())
	}
	return out
}

func TaskResultToPollProto(tr *model.TaskResult) *controlplanev1.TaskResultRequest {
	if tr == nil {
		return nil
	}

	errCode := tr.ErrorCode
	status := taskStatusToPoll(tr.Status)
	if tr.Status == model.TaskStatusResultTooLarge {
		// poll.proto doesn't have RESULT_TOO_LARGE; normalize to FAILED with error_code.
		status = controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_FAILED
		if errCode == "" {
			errCode = "RESULT_TOO_LARGE"
		}
	}

	return &controlplanev1.TaskResultRequest{
		TaskId:              tr.TaskID,
		AgentId:             tr.AgentID,
		Status:              status,
		ErrorCode:           errCode,
		ErrorMessage:        tr.ErrorMessage,
		ResultData:          tr.ResultData,
		ResultJson:          string(tr.ResultJSON),
		StartedAtMillis:     tr.StartedAtMillis,
		CompletedAtMillis:   tr.CompletedAtMillis,
		ExecutionTimeMillis: tr.ExecutionTimeMillis,
	}
}

func TaskResultToTaskProto(tr *model.TaskResult) *controlplanev1.TaskResult {
	if tr == nil {
		return nil
	}

	return &controlplanev1.TaskResult{
		TaskId:              tr.TaskID,
		Status:              taskStatusToTask(tr.Status),
		StartedAtMillis:     tr.StartedAtMillis,
		CompletedAtMillis:   tr.CompletedAtMillis,
		ExecutionTimeMillis: tr.ExecutionTimeMillis,
		ResultData:          tr.ResultData,
		ResultJson:          string(tr.ResultJSON),
		ResultDataType:      tr.ResultDataType,
		ErrorCode:           tr.ErrorCode,
		ErrorMessage:        tr.ErrorMessage,
		RetryCount:          tr.RetryCount,
		Compression:         compressionToProto(tr.Compression),
		OriginalSize:        tr.OriginalSize,
		CompressedSize:      tr.CompressedSize,
	}
}

func taskStatusFromPoll(st controlplanev1.TaskResultStatus) model.TaskStatus {
	switch st {
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_PENDING:
		return model.TaskStatusPending
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_RUNNING:
		return model.TaskStatusRunning
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_SUCCESS:
		return model.TaskStatusSuccess
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_FAILED:
		return model.TaskStatusFailed
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_TIMEOUT:
		return model.TaskStatusTimeout
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_CANCELLED:
		return model.TaskStatusCancelled
	default:
		return model.TaskStatusUnspecified
	}
}

func taskStatusToPoll(st model.TaskStatus) controlplanev1.TaskResultStatus {
	switch st {
	case model.TaskStatusPending:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_PENDING
	case model.TaskStatusRunning:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_RUNNING
	case model.TaskStatusSuccess:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_SUCCESS
	case model.TaskStatusFailed:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_FAILED
	case model.TaskStatusTimeout:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_TIMEOUT
	case model.TaskStatusCancelled:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_CANCELLED
	default:
		return controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_UNSPECIFIED
	}
}

func taskStatusFromTask(st controlplanev1.TaskResult_Status) model.TaskStatus {
	switch st {
	case controlplanev1.TaskResult_STATUS_SUCCESS:
		return model.TaskStatusSuccess
	case controlplanev1.TaskResult_STATUS_FAILED:
		return model.TaskStatusFailed
	case controlplanev1.TaskResult_STATUS_TIMEOUT:
		return model.TaskStatusTimeout
	case controlplanev1.TaskResult_STATUS_CANCELLED:
		return model.TaskStatusCancelled
	case controlplanev1.TaskResult_STATUS_RESULT_TOO_LARGE:
		return model.TaskStatusResultTooLarge
	default:
		return model.TaskStatusUnspecified
	}
}

func taskStatusToTask(st model.TaskStatus) controlplanev1.TaskResult_Status {
	switch st {
	case model.TaskStatusSuccess:
		return controlplanev1.TaskResult_STATUS_SUCCESS
	case model.TaskStatusFailed:
		return controlplanev1.TaskResult_STATUS_FAILED
	case model.TaskStatusTimeout:
		return controlplanev1.TaskResult_STATUS_TIMEOUT
	case model.TaskStatusCancelled:
		return controlplanev1.TaskResult_STATUS_CANCELLED
	case model.TaskStatusResultTooLarge:
		return controlplanev1.TaskResult_STATUS_RESULT_TOO_LARGE
	default:
		// task.proto doesn't model RUNNING/PENDING; keep unspecified.
		return controlplanev1.TaskResult_STATUS_UNSPECIFIED
	}
}

func compressionFromProto(c controlplanev1.TaskResult_CompressionType) model.CompressionType {
	switch c {
	case controlplanev1.TaskResult_COMPRESSION_TYPE_GZIP:
		return model.CompressionTypeGzip
	default:
		return model.CompressionTypeNone
	}
}

func compressionToProto(c model.CompressionType) controlplanev1.TaskResult_CompressionType {
	switch c {
	case model.CompressionTypeGzip:
		return controlplanev1.TaskResult_COMPRESSION_TYPE_GZIP
	default:
		return controlplanev1.TaskResult_COMPRESSION_TYPE_NONE
	}
}
