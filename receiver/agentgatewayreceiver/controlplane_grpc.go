// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"

	"go.uber.org/zap"

	// Register gzip compressor for gRPC.
	_ "google.golang.org/grpc/encoding/gzip"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

type controlPlaneGRPCServer struct {
	controlplanev1.UnimplementedControlPlaneServiceServer
	logger *zap.Logger
	svc    *controlPlaneService
}

func newControlPlaneGRPCServer(recv *agentGatewayReceiver, svc *controlPlaneService) *controlPlaneGRPCServer {
	return &controlPlaneGRPCServer{
		logger: recv.logger,
		svc:    svc,
	}
}

// withAgentID extracts the agent ID from the request body and injects it into context.
// Token authentication is already handled by the gRPC auth interceptor.
func withAgentID(ctx context.Context, agentID string) context.Context {
	if agentID != "" {
		ctx = context.WithValue(ctx, ContextKeyAgentID, agentID)
	}
	return ctx
}

func (s *controlPlaneGRPCServer) UnifiedPoll(ctx context.Context, req *controlplanev1.UnifiedPollRequest) (*controlplanev1.UnifiedPollResponse, error) {
	ctx = withAgentID(ctx, agentIDFromUnifiedPoll(req))
	return s.svc.UnifiedPoll(ctx, req), nil
}

func (s *controlPlaneGRPCServer) GetConfig(ctx context.Context, req *controlplanev1.ConfigRequest) (*controlplanev1.ConfigResponse, error) {
	ctx = withAgentID(ctx, agentIDFromConfigRequest(req))
	return s.svc.GetConfig(ctx, req), nil
}

func (s *controlPlaneGRPCServer) GetTasks(ctx context.Context, req *controlplanev1.TaskRequest) (*controlplanev1.TaskResponse, error) {
	ctx = withAgentID(ctx, agentIDFromTaskRequest(req))
	return s.svc.GetTasks(ctx, req), nil
}

func (s *controlPlaneGRPCServer) ReportStatus(ctx context.Context, req *controlplanev1.StatusRequest) (*controlplanev1.StatusResponse, error) {
	ctx = withAgentID(ctx, agentIDFromStatusRequest(req))
	return s.svc.ReportStatus(ctx, req), nil
}

func (s *controlPlaneGRPCServer) ReportTaskResult(ctx context.Context, req *controlplanev1.TaskResultRequest) (*controlplanev1.TaskResultResponse, error) {
	ctx = withAgentID(ctx, agentIDFromTaskResultRequest(req))
	return s.svc.ReportTaskResult(ctx, req), nil
}

func (s *controlPlaneGRPCServer) UploadChunkedResult(ctx context.Context, req *controlplanev1.ChunkedTaskResult) (*controlplanev1.ChunkedUploadResponse, error) {
	return s.svc.UploadChunkedResult(ctx, req), nil
}
