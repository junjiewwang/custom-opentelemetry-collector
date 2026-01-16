// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	// Register gzip compressor for gRPC.
	_ "google.golang.org/grpc/encoding/gzip"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

type controlPlaneGRPCServer struct {
	controlplanev1.UnimplementedControlPlaneServiceServer
	logger *zap.Logger
	recv   *agentGatewayReceiver
	svc    *controlPlaneService
}

func newControlPlaneGRPCServer(recv *agentGatewayReceiver, svc *controlPlaneService) *controlPlaneGRPCServer {
	return &controlPlaneGRPCServer{
		logger: recv.logger,
		recv:   recv,
		svc:    svc,
	}
}

func (s *controlPlaneGRPCServer) UnifiedPoll(ctx context.Context, req *controlplanev1.UnifiedPollRequest) (*controlplanev1.UnifiedPollResponse, error) {
	ctx, err := s.withBearerAuth(ctx, agentIDFromUnifiedPoll(req))
	if err != nil {
		return nil, err
	}
	return s.svc.UnifiedPoll(ctx, req), nil
}

func (s *controlPlaneGRPCServer) GetConfig(ctx context.Context, req *controlplanev1.ConfigRequest) (*controlplanev1.ConfigResponse, error) {
	ctx, err := s.withBearerAuth(ctx, agentIDFromConfigRequest(req))
	if err != nil {
		return nil, err
	}
	return s.svc.GetConfig(ctx, req), nil
}

func (s *controlPlaneGRPCServer) GetTasks(ctx context.Context, req *controlplanev1.TaskRequest) (*controlplanev1.TaskResponse, error) {
	ctx, err := s.withBearerAuth(ctx, agentIDFromTaskRequest(req))
	if err != nil {
		return nil, err
	}
	return s.svc.GetTasks(ctx, req), nil
}

func (s *controlPlaneGRPCServer) ReportStatus(ctx context.Context, req *controlplanev1.StatusRequest) (*controlplanev1.StatusResponse, error) {
	ctx, err := s.withBearerAuth(ctx, agentIDFromStatusRequest(req))
	if err != nil {
		return nil, err
	}
	return s.svc.ReportStatus(ctx, req), nil
}

func (s *controlPlaneGRPCServer) ReportTaskResult(ctx context.Context, req *controlplanev1.TaskResultRequest) (*controlplanev1.TaskResultResponse, error) {
	ctx, err := s.withBearerAuth(ctx, agentIDFromTaskResultRequest(req))
	if err != nil {
		return nil, err
	}
	return s.svc.ReportTaskResult(ctx, req), nil
}

func (s *controlPlaneGRPCServer) UploadChunkedResult(ctx context.Context, req *controlplanev1.ChunkedTaskResult) (*controlplanev1.ChunkedUploadResponse, error) {
	ctx, err := s.withBearerAuth(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.svc.UploadChunkedResult(ctx, req), nil
}

func (s *controlPlaneGRPCServer) withBearerAuth(ctx context.Context, agentID string) (context.Context, error) {
	if s.recv == nil || s.recv.controlPlane == nil {
		return ctx, status.Error(codes.Unavailable, "control plane not available")
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing metadata")
	}

	// gRPC metadata keys are case-insensitive and normalized to lowercase in Go.
	vals := md.Get("authorization")
	if len(vals) == 0 {
		vals = md.Get("Authorization")
	}
	if len(vals) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "missing authorization")
	}

	authHeader := vals[0]
	token := strings.TrimSpace(authHeader)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	if token == "" {
		return ctx, status.Error(codes.Unauthenticated, "invalid bearer token")
	}

	result, err := s.recv.controlPlane.ValidateToken(ctx, token)
	if err != nil {
		s.logger.Debug("Token validation error", zap.Error(err))
		return ctx, status.Error(codes.Unauthenticated, "token validation failed")
	}
	if result == nil || !result.Valid {
		reason := "invalid token"
		if result != nil && result.Reason != "" {
			reason = result.Reason
		}
		return ctx, status.Error(codes.Unauthenticated, reason)
	}

	ctx = context.WithValue(ctx, ContextKeyAppID, result.AppID)
	ctx = context.WithValue(ctx, ContextKeyToken, token)
	if agentID != "" {
		ctx = context.WithValue(ctx, ContextKeyAgentID, agentID)
	}

	return ctx, nil
}
