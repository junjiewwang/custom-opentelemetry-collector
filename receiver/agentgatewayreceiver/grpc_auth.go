// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// OTLP gRPC service full method names.
const (
	otlpTracesExportMethod  = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
	otlpMetricsExportMethod = "/opentelemetry.proto.collector.metrics.v1.MetricsService/Export"
	otlpLogsExportMethod    = "/opentelemetry.proto.collector.logs.v1.LogsService/Export"
)

// isOTLPMethod checks if the gRPC method is an OTLP Export method.
func isOTLPMethod(fullMethod string) bool {
	return fullMethod == otlpTracesExportMethod ||
		fullMethod == otlpMetricsExportMethod ||
		fullMethod == otlpLogsExportMethod
}

// grpcAuthInterceptor returns a gRPC unary server interceptor that performs
// token authentication. It mirrors the behavior of HTTP tokenAuthMiddleware:
//   - OTLP requests without Authorization header are allowed through
//     (can be authenticated later by tokenauth processor in the pipeline).
//   - OTLP requests with Authorization header are validated and appID is injected.
//   - Control plane requests must have valid Authorization.
func (r *agentGatewayReceiver) grpcAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// If token auth is not enabled or control plane is not available, pass through.
		if !r.config.TokenAuth.Enabled || r.controlPlane == nil {
			return handler(ctx, req)
		}

		// Extract authorization from gRPC metadata.
		token := r.extractGRPCToken(ctx)

		if token == "" {
			// OTLP requests without token are allowed through (same as HTTP behavior).
			if isOTLPMethod(info.FullMethod) {
				r.logger.Debug("gRPC OTLP request without auth, allowing through for processor auth",
					zap.String("method", info.FullMethod),
				)
				return handler(ctx, req)
			}

			// Non-OTLP requests (e.g., control plane) must have authorization.
			return nil, status.Error(codes.Unauthenticated, "missing authorization")
		}

		// Validate the token.
		result, err := r.controlPlane.ValidateToken(ctx, token)
		if err != nil {
			r.logger.Debug("gRPC token validation error",
				zap.String("method", info.FullMethod),
				zap.Error(err),
			)
			return nil, status.Error(codes.Unauthenticated, "token validation failed")
		}
		if result == nil || !result.Valid {
			reason := "invalid token"
			if result != nil && result.Reason != "" {
				reason = result.Reason
			}
			return nil, status.Error(codes.Unauthenticated, reason)
		}

		// Inject validated info into context.
		ctx = context.WithValue(ctx, ContextKeyAppID, result.AppID)
		ctx = context.WithValue(ctx, ContextKeyToken, token)

		r.logger.Debug("gRPC token validated",
			zap.String("method", info.FullMethod),
			zap.String("app_id", result.AppID),
		)

		return handler(ctx, req)
	}
}

// extractGRPCToken extracts the bearer token from gRPC metadata.
// gRPC metadata keys are normalized to lowercase in Go, so "authorization" suffices.
func (r *agentGatewayReceiver) extractGRPCToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	// gRPC metadata keys are case-insensitive and normalized to lowercase.
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}

	headerPrefix := r.config.GetTokenAuthHeaderPrefix()
	token := strings.TrimSpace(vals[0])
	if headerPrefix != "" && strings.HasPrefix(token, headerPrefix) {
		token = strings.TrimSpace(strings.TrimPrefix(token, headerPrefix))
	} else if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		// Fallback: handle "Bearer " prefix case-insensitively.
		token = strings.TrimSpace(token[len("bearer "):])
	}

	return token
}
