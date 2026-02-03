// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/conv/probeconv"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// controlPlaneService implements the probe control plane contract on the server side.
//
// It is transport-agnostic: HTTP and gRPC adapters call into this service.
// Token/app_id is injected into context by HTTP middleware or gRPC auth wrapper.

type controlPlaneService struct {
	logger          *zap.Logger
	controlPlane    controlplaneext.ControlPlaneV2
	longPollManager *longpoll.Manager
}

func newControlPlaneService(logger *zap.Logger, cp controlplaneext.ControlPlaneV2, lpm *longpoll.Manager) *controlPlaneService {
	return &controlPlaneService{logger: logger, controlPlane: cp, longPollManager: lpm}
}

func (s *controlPlaneService) UnifiedPoll(ctx context.Context, req *controlplanev1.UnifiedPollRequest) *controlplanev1.UnifiedPollResponse {
	if s.longPollManager == nil {
		return unifiedPollError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "long poll not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromUnifiedPoll(req)
	serviceName := serviceNameFromUnifiedPoll(req)

	// Validate required fields for config polling
	if req.GetConfigRequest() != nil && serviceName == "" {
		return unifiedPollError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "service_name is required for config polling")
	}

	lpReq := &longpoll.PollRequest{
		AgentID:              agentID,
		Token:                appID,
		ServiceName:          serviceName,
		CurrentConfigVersion: configVersionFromUnifiedPoll(req),
		CurrentConfigEtag:    configEtagFromUnifiedPoll(req),
		TimeoutMillis:        timeoutFromUnifiedPoll(req),
	}

	combined, err := s.longPollManager.Poll(ctx, lpReq)
	if err != nil {
		s.logger.Warn("UnifiedPoll failed", zap.Error(err))
		return unifiedPollError(controlplanev1.ResponseStatus_CODE_ERROR, err.Error())
	}

	resp := &controlplanev1.UnifiedPollResponse{
		Status:        &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		HasAnyChanges: combined.HasAnyChanges,
	}

	// Build config_response and task_response from poll results
	for pollType, r := range combined.Results {
		if r == nil {
			continue
		}

		switch pollType {
		case longpoll.LongPollTypeConfig:
			cfgResp := &controlplanev1.ConfigResponse{
				Status:                      &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
				Success:                     true,
				HasChanges:                  r.HasChanges,
				SuggestedPollIntervalMillis: 0,
				ConfigVersion:               r.ConfigVersion,
				Etag:                        r.ConfigEtag,
			}
			if r.Config != nil {
				cfgResp.Config = probeconv.AgentConfigToProto(r.Config)
			}
			resp.ConfigResponse = cfgResp

		case longpoll.LongPollTypeTask:
			resp.TaskResponse = &controlplanev1.TaskResponse{
				Status:                      &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
				Tasks:                       probeconv.TasksToProto(r.Tasks),
				SuggestedPollIntervalMillis: 0,
			}
		}
	}

	return resp
}

func (s *controlPlaneService) GetConfig(ctx context.Context, req *controlplanev1.ConfigRequest) *controlplanev1.ConfigResponse {
	if s.longPollManager == nil {
		return configError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "long poll not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromConfigRequest(req)
	serviceName := req.GetServiceName()

	// Validate required fields
	if serviceName == "" {
		return configError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "service_name is required")
	}

	lpReq := &longpoll.PollRequest{
		AgentID:              agentID,
		Token:                appID,
		ServiceName:          serviceName,
		CurrentConfigVersion: pickFirstNonEmpty(req.GetCurrentVersion().GetVersion(), req.GetCurrentConfigVersion()),
		CurrentConfigEtag:    pickFirstNonEmpty(req.GetCurrentVersion().GetEtag(), req.GetCurrentEtag()),
		TimeoutMillis:        req.GetLongPollTimeoutMillis(),
	}

	pollResp, err := s.longPollManager.PollSingle(ctx, lpReq, longpoll.LongPollTypeConfig)
	if err != nil {
		s.logger.Warn("GetConfig failed", zap.Error(err))
		return configError(controlplanev1.ResponseStatus_CODE_ERROR, err.Error())
	}

	cfgResp := &controlplanev1.ConfigResponse{
		Status:                      &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Success:                     true,
		HasChanges:                  pollResp.HasChanges,
		SuggestedPollIntervalMillis: 0,
		ConfigVersion:               pollResp.ConfigVersion,
		Etag:                        pollResp.ConfigEtag,
	}

	if pollResp.Config != nil {
		cfgResp.Config = probeconv.AgentConfigToProto(pollResp.Config)
	}

	return cfgResp
}

func (s *controlPlaneService) GetTasks(ctx context.Context, req *controlplanev1.TaskRequest) *controlplanev1.TaskResponse {
	if s.longPollManager == nil {
		return taskError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "long poll not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromTaskRequest(req)

	lpReq := &longpoll.PollRequest{
		AgentID:       agentID,
		Token:         appID,
		TimeoutMillis: req.GetLongPollTimeoutMillis(),
	}

	pollResp, err := s.longPollManager.PollSingle(ctx, lpReq, longpoll.LongPollTypeTask)
	if err != nil {
		s.logger.Warn("GetTasks failed", zap.Error(err))
		return taskError(controlplanev1.ResponseStatus_CODE_ERROR, err.Error())
	}

	return &controlplanev1.TaskResponse{
		Status:                      &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Tasks:                       probeconv.TasksToProto(pollResp.Tasks),
		SuggestedPollIntervalMillis: 0,
	}
}

func (s *controlPlaneService) ReportTaskResult(ctx context.Context, req *controlplanev1.TaskResultRequest) *controlplanev1.TaskResultResponse {
	if s.controlPlane == nil {
		return taskResultError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "control plane not available")
	}

	result := req.GetResult()
	if result == nil || result.GetTaskId() == "" {
		return taskResultError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "task_id is required")
	}

	agentID := agentIDFromTaskResultRequest(req)
	tr := probeconv.TaskResultFromTaskProto(result, agentID)
	if tr == nil {
		return taskResultError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "invalid task result")
	}
	if tr.CompletedAtMillis == 0 {
		tr.CompletedAtMillis = time.Now().UnixMilli()
	}

	err := s.controlPlane.ReportTaskResult(ctx, tr)
	if err != nil {
		code := classifyBusinessError(err)
		return &controlplanev1.TaskResultResponse{
			Status:       &controlplanev1.ResponseStatus{Code: code, Message: err.Error()},
			Acknowledged: false,
			Message:      "failed",
		}
	}

	return &controlplanev1.TaskResultResponse{
		Status:       &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Acknowledged: true,
		Message:      "acknowledged",
	}
}

func (s *controlPlaneService) ReportStatus(ctx context.Context, req *controlplanev1.StatusRequest) *controlplanev1.StatusResponse {
	if s.controlPlane == nil {
		return statusError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "control plane not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromStatusRequest(req)

	// Update agent registry based on AgentIdentity (heartbeat).
	if agentID != "" {
		if ai := statusRequestToAgentInfo(req, appID); ai != nil {
			if err := s.controlPlane.RegisterOrHeartbeatAgent(ctx, ai); err != nil {
				s.logger.Debug("RegisterOrHeartbeatAgent failed", zap.String("agent_id", agentID), zap.Error(err))
			}
		}
	}

	return &controlplanev1.StatusResponse{
		Status:           &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		ServerTimeMillis: time.Now().UnixMilli(),
	}
}

func (s *controlPlaneService) UploadChunkedResult(ctx context.Context, req *controlplanev1.ChunkedTaskResult) *controlplanev1.ChunkedUploadResponse {
	if s.controlPlane == nil {
		return chunkError(controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, "control plane not available")
	}

	modelReq := probeconv.ChunkUploadFromProto(req)
	if modelReq == nil {
		return chunkError(controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, "invalid chunk request")
	}

	modelResp, err := s.controlPlane.UploadChunk(ctx, modelReq)
	if err != nil {
		return chunkError(controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, err.Error())
	}
	if modelResp == nil {
		return chunkError(controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, "empty response")
	}

	// Keep response aligned with probe contract.
	if modelResp.UploadID == "" {
		modelResp.UploadID = pickFirstNonEmpty(req.GetUploadId(), req.GetTaskId())
	}
	if modelResp.ReceivedChunkIndex == 0 {
		// Preserve old semantics: receiver always echoes the current chunk index.
		modelResp.ReceivedChunkIndex = req.GetChunkIndex()
	}

	return probeconv.ChunkUploadResponseToProto(modelResp)
}

func pickFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func classifyBusinessError(err error) controlplanev1.ResponseStatus_Code {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return controlplanev1.ResponseStatus_CODE_NOT_FOUND
	case strings.Contains(msg, "invalid") || strings.Contains(msg, "required"):
		return controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT
	case strings.Contains(msg, "unavailable"):
		return controlplanev1.ResponseStatus_CODE_UNAVAILABLE
	default:
		return controlplanev1.ResponseStatus_CODE_ERROR
	}
}

func unifiedPollError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.UnifiedPollResponse {
	return &controlplanev1.UnifiedPollResponse{
		Status: &controlplanev1.ResponseStatus{Code: code, Message: message},
	}
}

func configError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.ConfigResponse {
	return &controlplanev1.ConfigResponse{
		Status: &controlplanev1.ResponseStatus{Code: code, Message: message},
	}
}

func taskError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.TaskResponse {
	return &controlplanev1.TaskResponse{
		Status: &controlplanev1.ResponseStatus{Code: code, Message: message},
	}
}

func statusError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.StatusResponse {
	return &controlplanev1.StatusResponse{
		Status:           &controlplanev1.ResponseStatus{Code: code, Message: message},
		ServerTimeMillis: time.Now().UnixMilli(),
	}
}

func taskResultError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.TaskResultResponse {
	return &controlplanev1.TaskResultResponse{
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Acknowledged: false,
		Message:      message,
	}
}

func chunkError(status controlplanev1.ChunkUploadStatus, message string) *controlplanev1.ChunkedUploadResponse {
	return &controlplanev1.ChunkedUploadResponse{
		Status:       status,
		ErrorMessage: message,
	}
}
