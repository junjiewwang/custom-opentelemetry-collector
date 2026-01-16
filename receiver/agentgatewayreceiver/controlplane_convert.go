// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"encoding/json"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
	legacyv1 "go.opentelemetry.io/collector/custom/proto/controlplane_legacy/v1"
)

func agentIDFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if req.GetAgentIdentity() != nil {
		return req.GetAgentIdentity().GetAgentId()
	}
	return ""
}

func configVersionFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil || req.GetCurrentConfigVersion() == nil {
		return ""
	}
	return req.GetCurrentConfigVersion().GetVersion()
}

func configEtagFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil || req.GetCurrentConfigVersion() == nil {
		return ""
	}
	return req.GetCurrentConfigVersion().GetEtag()
}

func agentIDFromConfigRequest(req *controlplanev1.ConfigRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if req.GetAgentIdentity() != nil {
		return req.GetAgentIdentity().GetAgentId()
	}
	return ""
}

func agentIDFromTaskRequest(req *controlplanev1.TaskRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if req.GetAgentIdentity() != nil {
		return req.GetAgentIdentity().GetAgentId()
	}
	return ""
}

func agentIDFromTaskResultRequest(req *controlplanev1.TaskResultRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if req.GetAgentIdentity() != nil {
		return req.GetAgentIdentity().GetAgentId()
	}
	return ""
}

func agentIDFromStatusRequest(req *controlplanev1.StatusRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if req.GetAgentIdentity() != nil {
		return req.GetAgentIdentity().GetAgentId()
	}
	return ""
}

func legacyConfigToProto(cfg *legacyv1.AgentConfig, etag string) *controlplanev1.AgentConfig {
	if cfg == nil {
		return nil
	}

	out := &controlplanev1.AgentConfig{
		Version: &controlplanev1.ConfigVersion{
			Version: cfg.ConfigVersion,
			Etag:    etag,
		},
		DynamicResourceAttributes: cfg.DynamicResourceAttributes,
		ExtensionConfigJson:       cfg.ExtensionConfigJSON,
	}

	if cfg.Sampler != nil {
		out.Sampler = &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerConfig_SamplerType(cfg.Sampler.Type),
			Ratio: cfg.Sampler.Ratio,
			// NOTE: legacy rule format is JSON string; probe expects structured rules.
			// We intentionally do not attempt to parse here.
			Rules: nil,
		}
	}

	if cfg.Batch != nil {
		out.Batch = &controlplanev1.BatchConfig{
			MaxExportBatchSize: cfg.Batch.MaxExportBatchSize,
			MaxQueueSize:       cfg.Batch.MaxQueueSize,
			ScheduleDelayMillis: cfg.Batch.ScheduleDelayMillis,
			ExportTimeoutMillis: cfg.Batch.ExportTimeoutMillis,
		}
	}

	return out
}

func legacyTasksToProto(tasks []*legacyv1.Task) []*controlplanev1.Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]*controlplanev1.Task, 0, len(tasks))
	for _, t := range tasks {
		if t == nil {
			continue
		}
		out = append(out, legacyTaskToProto(t))
	}
	return out
}

func legacyTaskToProto(t *legacyv1.Task) *controlplanev1.Task {
	if t == nil {
		return nil
	}

	priorityEnum := controlplanev1.Task_PRIORITY_UNSPECIFIED
	switch {
	case t.Priority <= 1:
		priorityEnum = controlplanev1.Task_PRIORITY_LOW
	case t.Priority >= 3:
		priorityEnum = controlplanev1.Task_PRIORITY_HIGH
	default:
		priorityEnum = controlplanev1.Task_PRIORITY_NORMAL
	}

	paramsJSON := ""
	if t.Parameters != nil {
		if b, err := json.Marshal(t.Parameters); err == nil {
			paramsJSON = string(b)
		}
	}

	return &controlplanev1.Task{
		TaskId:                  t.TaskID,
		Type:                    controlplanev1.Task_TASK_TYPE_CUSTOM,
		TaskTypeName:            t.TaskType,
		ParametersJson:          paramsJSON,
		Priority:                priorityEnum,
		PriorityNum:             t.Priority,
		TimeoutMillis:           t.TimeoutMillis,
		CreatedAtMillis:         t.CreatedAtMillis,
		ExpiresAtMillis:         t.ExpiresAtMillis,
		MaxAcceptableDelayMillis: t.MaxAcceptableDelayMillis,
	}
}

func taskResultRequestToLegacy(req *controlplanev1.TaskResultRequest, agentID string) *legacyv1.TaskResult {
	out := &legacyv1.TaskResult{
		TaskID:              req.GetTaskId(),
		AgentID:             agentID,
		Status:              legacyTaskStatusFromProbe(req.GetStatus()),
		ErrorCode:           req.GetErrorCode(),
		ErrorMessage:        req.GetErrorMessage(),
		ResultData:          req.GetResultData(),
		StartedAtMillis:     req.GetStartedAtMillis(),
		CompletedAtMillis:   req.GetCompletedAtMillis(),
		ExecutionTimeMillis: req.GetExecutionTimeMillis(),
	}

	if req.GetResultJson() != "" {
		out.Result = json.RawMessage(req.GetResultJson())
	}

	return out
}

func taskResultToLegacy(tr *controlplanev1.TaskResult, agentID string) *legacyv1.TaskResult {
	if tr == nil {
		return nil
	}

	out := &legacyv1.TaskResult{
		TaskID:              tr.GetTaskId(),
		AgentID:             agentID,
		Status:              legacyTaskResultStatusFromProbe(tr.GetStatus()),
		ErrorCode:           tr.GetErrorCode(),
		ErrorMessage:        tr.GetErrorMessage(),
		ResultData:          tr.GetResultData(),
		StartedAtMillis:     tr.GetStartedAtMillis(),
		CompletedAtMillis:   tr.GetCompletedAtMillis(),
		ExecutionTimeMillis: tr.GetExecutionTimeMillis(),
	}
	if tr.GetResultJson() != "" {
		out.Result = json.RawMessage(tr.GetResultJson())
	}
	return out
}

func legacyTaskStatusFromProbe(st controlplanev1.TaskResultStatus) legacyv1.TaskStatus {
	switch st {
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_PENDING:
		return legacyv1.TaskStatusPending
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_RUNNING:
		return legacyv1.TaskStatusRunning
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_SUCCESS:
		return legacyv1.TaskStatusSuccess
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_TIMEOUT:
		return legacyv1.TaskStatusTimeout
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_CANCELLED:
		return legacyv1.TaskStatusCancelled
	case controlplanev1.TaskResultStatus_TASK_RESULT_STATUS_FAILED:
		return legacyv1.TaskStatusFailed
	default:
		return legacyv1.TaskStatusUnspecified
	}
}

func legacyTaskResultStatusFromProbe(st controlplanev1.TaskResult_Status) legacyv1.TaskStatus {
	switch st {
	case controlplanev1.TaskResult_STATUS_SUCCESS:
		return legacyv1.TaskStatusSuccess
	case controlplanev1.TaskResult_STATUS_FAILED:
		return legacyv1.TaskStatusFailed
	case controlplanev1.TaskResult_STATUS_TIMEOUT:
		return legacyv1.TaskStatusTimeout
	case controlplanev1.TaskResult_STATUS_CANCELLED:
		return legacyv1.TaskStatusCancelled
	default:
		return legacyv1.TaskStatusUnspecified
	}
}

func statusRequestToAgentInfo(req *controlplanev1.StatusRequest, appID string) *agentregistry.AgentInfo {
	if req == nil {
		return nil
	}

	id := req.GetAgentIdentity()
	if id == nil {
		return nil
	}

	agentID := agentIDFromStatusRequest(req)
	if agentID == "" {
		return nil
	}

	labels := map[string]string{}
	if id.GetServiceName() != "" {
		labels["service.name"] = id.GetServiceName()
	}
	if id.GetServiceNamespace() != "" {
		labels["service.namespace"] = id.GetServiceNamespace()
	}
	if id.GetProcessId() != "" {
		labels["process.pid"] = id.GetProcessId()
	}

	ai := &agentregistry.AgentInfo{
		AgentID:     agentID,
		AppID:       appID,
		Hostname:    id.GetHostName(),
		Version:     id.GetSdkVersion(),
		ServiceName: id.GetServiceName(),
		StartTime:   id.GetStartTimeMillis(),
		Labels:      labels,
	}

	if attrs := id.GetAttributes(); len(attrs) > 0 {
		if ai.Labels == nil {
			ai.Labels = map[string]string{}
		}
		for k, v := range attrs {
			// Preserve as labels to keep registry simple.
			ai.Labels[k] = v
		}
	}

	return ai
}
