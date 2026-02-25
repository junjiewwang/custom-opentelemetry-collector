// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"errors"
	"net"
	"net/http"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/config/configgrpc"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// startGRPCServer starts the gRPC server for OTLP gRPC protocol.
func (r *agentGatewayReceiver) startGRPCServer(ctx context.Context, host component.Host) error {
	if r.config.GRPC == nil {
		return nil
	}

	// Build gRPC server options with auth interceptor.
	var serverOpts []configgrpc.ToServerOption
	if r.config.TokenAuth.Enabled && r.controlPlane != nil {
		serverOpts = append(serverOpts,
			configgrpc.WithGrpcServerOption(grpc.ChainUnaryInterceptor(r.grpcAuthInterceptor())),
		)
	}

	var err error
	r.serverGRPC, err = r.config.GRPC.ToServer(ctx, host, r.settings.TelemetrySettings, serverOpts...)
	if err != nil {
		return err
	}

	// Register OTLP gRPC services
	r.registerOTLPGRPC()

	// Register ControlPlane gRPC service (shares the same gRPC port with OTLP).
	if r.config.ControlPlane.Enabled && r.controlPlane != nil {
		svc := newControlPlaneService(r.logger, r.controlPlane, r.longPollManager)
		controlplanev1.RegisterControlPlaneServiceServer(r.serverGRPC, newControlPlaneGRPCServer(r, svc))
		r.logger.Info("Registered control plane gRPC service")
	}

	r.logger.Info("Starting gRPC server", zap.String("endpoint", r.config.GRPC.NetAddr.Endpoint))

	gln, err := r.config.GRPC.NetAddr.Listen(ctx)
	if err != nil {
		return err
	}

	r.shutdownWG.Add(1)
	go func() {
		defer r.shutdownWG.Done()
		if errGrpc := r.serverGRPC.Serve(gln); errGrpc != nil && !errors.Is(errGrpc, grpc.ErrServerStopped) {
			componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(errGrpc))
		}
	}()

	return nil
}

// registerOTLPGRPC registers OTLP gRPC services to the gRPC server.
func (r *agentGatewayReceiver) registerOTLPGRPC() {
	if r.tracesConsumer != nil {
		ptraceotlp.RegisterGRPCServer(r.serverGRPC, &traceReceiver{
			consumer: r.tracesConsumer,
			obsrep:   r.obsrepGRPC,
			recv:     r,
		})
	}

	if r.metricsConsumer != nil {
		pmetricotlp.RegisterGRPCServer(r.serverGRPC, &metricsReceiver{
			consumer: r.metricsConsumer,
			obsrep:   r.obsrepGRPC,
			recv:     r,
		})
	}

	if r.logsConsumer != nil {
		plogotlp.RegisterGRPCServer(r.serverGRPC, &logsReceiver{
			consumer: r.logsConsumer,
			obsrep:   r.obsrepGRPC,
			recv:     r,
		})
	}
}

// startHTTPServer starts the HTTP server.
func (r *agentGatewayReceiver) startHTTPServer(ctx context.Context, host component.Host) error {
	if r.config.HTTP == nil {
		return nil
	}

	// Create router with all routes
	httpRouter := r.newHTTPRouter()

	var err error
	r.serverHTTP, err = r.config.HTTP.ToServer(
		ctx, host, r.settings.TelemetrySettings, httpRouter,
		confighttp.WithErrorHandler(defaultErrorHandler),
	)
	if err != nil {
		return err
	}

	r.logger.Info("Starting HTTP server",
		zap.String("endpoint", r.config.HTTP.Endpoint),
		zap.Bool("otlp_enabled", r.config.OTLP.Enabled),
		zap.Bool("control_plane_enabled", r.config.ControlPlane.Enabled && r.controlPlane != nil),
		zap.Bool("arthas_tunnel_enabled", r.config.ArthasTunnel.Enabled && r.arthasTunnel != nil),
	)

	var hln net.Listener
	hln, err = r.config.HTTP.ToListener(ctx)
	if err != nil {
		return err
	}

	r.shutdownWG.Add(1)
	go func() {
		defer r.shutdownWG.Done()
		if errHTTP := r.serverHTTP.Serve(hln); errHTTP != nil && !errors.Is(errHTTP, http.ErrServerClosed) {
			componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(errHTTP))
		}
	}()

	return nil
}

// defaultErrorHandler handles errors for HTTP endpoints.
func defaultErrorHandler(w http.ResponseWriter, _ *http.Request, _ string, statusCode int) {
	http.Error(w, http.StatusText(statusCode), statusCode)
}
