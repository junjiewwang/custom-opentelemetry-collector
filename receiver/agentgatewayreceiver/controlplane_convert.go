// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// agentIDFromUnifiedPoll extracts agent ID from UnifiedPollRequest.
// Priority: top-level agent_id > config_request.agent_id > task_request.agent_id
func agentIDFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	if req.GetAgentId() != "" {
		return req.GetAgentId()
	}
	if cr := req.GetConfigRequest(); cr != nil && cr.GetAgentId() != "" {
		return cr.GetAgentId()
	}
	if tr := req.GetTaskRequest(); tr != nil && tr.GetAgentId() != "" {
		return tr.GetAgentId()
	}
	return ""
}

// timeoutFromUnifiedPoll extracts timeout from UnifiedPollRequest.
// Priority: top-level timeout_millis > config_request > task_request
func timeoutFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) int64 {
	if req == nil {
		return 0
	}
	if req.GetTimeoutMillis() > 0 {
		return req.GetTimeoutMillis()
	}
	if cr := req.GetConfigRequest(); cr != nil && cr.GetLongPollTimeoutMillis() > 0 {
		return cr.GetLongPollTimeoutMillis()
	}
	if tr := req.GetTaskRequest(); tr != nil && tr.GetLongPollTimeoutMillis() > 0 {
		return tr.GetLongPollTimeoutMillis()
	}
	return 0
}

// configVersionFromUnifiedPoll extracts config version from UnifiedPollRequest.
func configVersionFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	cr := req.GetConfigRequest()
	if cr == nil {
		return ""
	}
	if v := cr.GetCurrentVersion(); v != nil && v.GetVersion() != "" {
		return v.GetVersion()
	}
	return cr.GetCurrentConfigVersion()
}

// configEtagFromUnifiedPoll extracts config etag from UnifiedPollRequest.
func configEtagFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	cr := req.GetConfigRequest()
	if cr == nil {
		return ""
	}
	if v := cr.GetCurrentVersion(); v != nil && v.GetEtag() != "" {
		return v.GetEtag()
	}
	return cr.GetCurrentEtag()
}

// serviceNameFromUnifiedPoll extracts service name from UnifiedPollRequest.
// Service name is used as the routing key for config lookup (e.g., Nacos DataId).
func serviceNameFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	if cr := req.GetConfigRequest(); cr != nil {
		return cr.GetServiceName()
	}
	return ""
}

func agentIDFromConfigRequest(req *controlplanev1.ConfigRequest) string {
	if req == nil {
		return ""
	}
	return req.GetAgentId()
}

func agentIDFromTaskRequest(req *controlplanev1.TaskRequest) string {
	if req == nil {
		return ""
	}
	return req.GetAgentId()
}

func agentIDFromTaskResultRequest(req *controlplanev1.TaskResultRequest) string {
	if req == nil {
		return ""
	}
	return req.GetAgentId()
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
		IP:          id.GetIp(),
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
			ai.Labels[k] = v
		}
	}

	return ai
}
