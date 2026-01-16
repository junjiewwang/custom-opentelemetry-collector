// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
	legacyv1 "go.opentelemetry.io/collector/custom/proto/controlplane_legacy/v1"
)

// controlPlaneService implements the probe control plane contract on the server side.
//
// It is transport-agnostic: HTTP and gRPC adapters call into this service.
// Token/app_id is injected into context by HTTP middleware or gRPC auth wrapper.

type controlPlaneService struct {
	logger          *zap.Logger
	controlPlane    controlplaneext.ControlPlane
	longPollManager *longpoll.Manager
}

func newControlPlaneService(logger *zap.Logger, cp controlplaneext.ControlPlane, lpm *longpoll.Manager) *controlPlaneService {
	return &controlPlaneService{logger: logger, controlPlane: cp, longPollManager: lpm}
}

func (s *controlPlaneService) UnifiedPoll(ctx context.Context, req *controlplanev1.UnifiedPollRequest) *controlplanev1.UnifiedPollResponse {
	if s.longPollManager == nil {
		return unifiedPollError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "long poll not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromUnifiedPoll(req)

	lpReq := &longpoll.PollRequest{
		AgentID:              agentID,
		Token:                appID,
		CurrentConfigVersion: pickFirstNonEmpty(configVersionFromUnifiedPoll(req), req.GetCurrentConfigVersionStr()),
		CurrentConfigEtag:    pickFirstNonEmpty(configEtagFromUnifiedPoll(req), req.GetCurrentConfigEtag()),
		TimeoutMillis:        req.GetTimeoutMillis(),
	}

	combined, err := s.longPollManager.Poll(ctx, lpReq)
	if err != nil {
		s.logger.Warn("UnifiedPoll failed", zap.Error(err))
		return unifiedPollError(controlplanev1.ResponseStatus_CODE_ERROR, err.Error())
	}

	resp := &controlplanev1.UnifiedPollResponse{
		Status: &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Success:       true,
		HasAnyChanges: combined.HasAnyChanges,
		Results:       make(map[string]*controlplanev1.PollResult, len(combined.Results)),
	}

	for pollType, r := range combined.Results {
		if r == nil {
			continue
		}
		key := string(pollType)
		pr := &controlplanev1.PollResult{
			Type:       key,
			HasChanges: r.HasChanges,
			Message:    r.Message,
		}

		switch pollType {
		case longpoll.LongPollTypeConfig:
			// Config is stored internally as legacy JSON model; convert to probe proto.
			if r.Config != nil {
				cfgProto := legacyConfigToProto(r.Config, r.ConfigEtag)
				cfgBytes, _ := proto.Marshal(cfgProto)
				pr.ConfigData = cfgBytes
			}
			pr.ConfigVersion = r.ConfigVersion
			pr.ConfigEtag = r.ConfigEtag
		case longpoll.LongPollTypeTask:
			pr.Tasks = legacyTasksToProto(r.Tasks)
		}

		resp.Results[key] = pr
	}

	return resp
}

func (s *controlPlaneService) GetConfig(ctx context.Context, req *controlplanev1.ConfigRequest) *controlplanev1.ConfigResponse {
	if s.longPollManager == nil {
		return configError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "long poll not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromConfigRequest(req)

	lpReq := &longpoll.PollRequest{
		AgentID:              agentID,
		Token:                appID,
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
		Status: &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Success:                     true,
		HasChanges:                  pollResp.HasChanges,
		SuggestedPollIntervalMillis: 0,
		ConfigVersion:               pollResp.ConfigVersion,
		Etag:                        pollResp.ConfigEtag,
	}

	if pollResp.Config != nil {
		cfgResp.Config = legacyConfigToProto(pollResp.Config, pollResp.ConfigEtag)
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
		Status:                     &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Success:                    true,
		Tasks:                      legacyTasksToProto(pollResp.Tasks),
		SuggestedPollIntervalMillis: 0,
	}
}

func (s *controlPlaneService) ReportTaskResult(ctx context.Context, req *controlplanev1.TaskResultRequest) *controlplanev1.TaskResultResponse {
	if req.GetTaskId() == "" {
		return taskResultError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "task_id is required")
	}
	if s.controlPlane == nil {
		return taskResultError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "control plane not available")
	}

	agentID := agentIDFromTaskResultRequest(req)
	legacy := taskResultRequestToLegacy(req, agentID)

	if legacy.CompletedAtMillis == 0 {
		legacy.CompletedAtMillis = time.Now().UnixMilli()
	}

	err := s.controlPlane.ReportTaskResult(ctx, legacy)
	if err != nil {
		code := classifyBusinessError(err)
		return &controlplanev1.TaskResultResponse{
			Status:        &controlplanev1.ResponseStatus{Code: code, Message: err.Error()},
			Acknowledged:  false,
			Success:       false,
			ErrorMessage:  err.Error(),
			Message:       "failed",
		}
	}

	return &controlplanev1.TaskResultResponse{
		Status:       &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		Acknowledged: true,
		Success:      true,
		Message:      "acknowledged",
	}
}

func (s *controlPlaneService) ReportStatus(ctx context.Context, req *controlplanev1.StatusRequest) *controlplanev1.StatusResponse {
	if s.controlPlane == nil {
		return statusError(controlplanev1.ResponseStatus_CODE_UNAVAILABLE, "control plane not available")
	}

	appID := GetAppIDFromContext(ctx)
	agentID := agentIDFromStatusRequest(req)

	// Best-effort: update agent registry based on AgentIdentity.
	if agentID != "" {
		if ai := statusRequestToAgentInfo(req, appID); ai != nil {
			if err := s.controlPlane.RegisterOrHeartbeatAgent(ctx, ai); err != nil {
				s.logger.Debug("RegisterOrHeartbeatAgent failed", zap.String("agent_id", agentID), zap.Error(err))
			}
		}
	}

	// Best-effort: apply embedded task results (if present).
	for _, tr := range req.GetTaskResults() {
		if tr == nil || tr.GetTaskId() == "" {
			continue
		}
		legacyTr := taskResultToLegacy(tr, agentID)
		_ = s.controlPlane.ReportTaskResult(ctx, legacyTr)
	}

	return &controlplanev1.StatusResponse{
		Status:                    &controlplanev1.ResponseStatus{Code: controlplanev1.ResponseStatus_CODE_OK, Message: "ok"},
		ServerTimeMillis:          time.Now().UnixMilli(),
		SuggestedReportIntervalMillis: 0,
		AcknowledgedTaskIds:       nil,
		Success:                   true,
	}
}

func (s *controlPlaneService) UploadChunkedResult(ctx context.Context, req *controlplanev1.ChunkedTaskResult) *controlplanev1.ChunkedUploadResponse {
	if s.controlPlane == nil {
		return chunkError(controlplanev1.ChunkedUploadResponse_STATUS_UPLOAD_FAILED, "control plane not available")
	}

	// Current controlplane extension uses a legacy chunk upload API.
	// We bridge probe's ChunkedTaskResult into the legacy UploadChunkRequest.
	legacyReq := &legacyv1.UploadChunkRequest{
		TaskID:      req.GetTaskId(),
		ChunkIndex:  req.GetChunkIndex(),
		TotalChunks: req.GetTotalChunks(),
		Data:        req.GetChunkData(),
		Checksum:    req.GetChunkChecksum(),
	}

	legacyResp, err := s.controlPlane.UploadChunk(ctx, legacyReq)
	if err != nil {
		return chunkError(controlplanev1.ChunkedUploadResponse_STATUS_UPLOAD_FAILED, err.Error())
	}

	status := controlplanev1.ChunkedUploadResponse_STATUS_CHUNK_RECEIVED
	if legacyResp.Complete {
		status = controlplanev1.ChunkedUploadResponse_STATUS_UPLOAD_COMPLETE
	}
	if strings.Contains(strings.ToLower(legacyResp.Message), "checksum") {
		status = controlplanev1.ChunkedUploadResponse_STATUS_CHECKSUM_MISMATCH
	}

	uploadID := req.GetUploadId()
	if uploadID == "" {
		uploadID = req.GetTaskId()
	}

	return &controlplanev1.ChunkedUploadResponse{
		UploadId:          uploadID,
		ReceivedChunkIndex: req.GetChunkIndex(),
		Status:            status,
		ErrorMessage:      pickFirstNonEmpty(legacyResp.Message, ""),
	}
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
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Success:      false,
		ErrorMessage: message,
	}
}

func configError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.ConfigResponse {
	return &controlplanev1.ConfigResponse{
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Success:      false,
		ErrorMessage: message,
	}
}

func taskError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.TaskResponse {
	return &controlplanev1.TaskResponse{
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Success:      false,
		ErrorMessage: message,
	}
}

func statusError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.StatusResponse {
	return &controlplanev1.StatusResponse{
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Success:      false,
		ErrorMessage: message,
		ServerTimeMillis: time.Now().UnixMilli(),
	}
}

func taskResultError(code controlplanev1.ResponseStatus_Code, message string) *controlplanev1.TaskResultResponse {
	return &controlplanev1.TaskResultResponse{
		Status:       &controlplanev1.ResponseStatus{Code: code, Message: message},
		Acknowledged: false,
		Success:      false,
		ErrorMessage: message,
		Message:      message,
	}
}

func chunkError(status controlplanev1.ChunkedUploadResponse_Status, message string) *controlplanev1.ChunkedUploadResponse {
	return &controlplanev1.ChunkedUploadResponse{
		Status:       status,
		ErrorMessage: message,
	}
}
