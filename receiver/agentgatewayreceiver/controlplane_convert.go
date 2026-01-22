// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func agentIDFromUnifiedPoll(req *controlplanev1.UnifiedPollRequest) string {
	if req == nil {
		return ""
	}
	return req.GetAgentId()
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
			// Preserve as labels to keep registry simple.
			ai.Labels[k] = v
		}
	}

	return ai
}
