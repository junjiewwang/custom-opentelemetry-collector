// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
)

// agentGatewayReceiver implements a unified receiver that routes requests
// to different handlers based on URL path.
type agentGatewayReceiver struct {
	config   *Config
	settings receiver.Settings
	logger   *zap.Logger

	// gRPC server for OTLP gRPC protocol
	serverGRPC *grpc.Server

	// HTTP server for all HTTP-based protocols
	serverHTTP *http.Server

	// Extension references
	controlPlane    controlplaneext.ControlPlane
	controlPlaneExt *controlplaneext.Extension // For accessing internal components
	arthasTunnel    arthastunnelext.ArthasTunnel

	// Long poll manager
	longPollManager *longpoll.Manager

	// Consumers for OTLP data
	tracesConsumer  consumer.Traces
	metricsConsumer consumer.Metrics
	logsConsumer    consumer.Logs

	// Observability reports
	obsrepGRPC *receiverhelper.ObsReport
	obsrepHTTP *receiverhelper.ObsReport

	// Lifecycle management
	shutdownWG sync.WaitGroup
	startOnce  sync.Once
	stopOnce   sync.Once
	startErr   error
}

// sharedReceivers ensures we only create one receiver instance per configuration.
var (
	sharedReceivers   = make(map[*Config]*agentGatewayReceiver)
	sharedReceiversMu sync.Mutex
)

// getOrCreateReceiver returns an existing receiver or creates a new one.
func getOrCreateReceiver(set receiver.Settings, cfg *Config) (*agentGatewayReceiver, error) {
	sharedReceiversMu.Lock()
	defer sharedReceiversMu.Unlock()

	if r, exists := sharedReceivers[cfg]; exists {
		return r, nil
	}

	r := &agentGatewayReceiver{
		config:   cfg,
		settings: set,
		logger:   set.Logger,
	}

	// Create observability reports
	var err error
	r.obsrepGRPC, err = receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              "grpc",
		ReceiverCreateSettings: set,
	})
	if err != nil {
		return nil, err
	}

	r.obsrepHTTP, err = receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              "http",
		ReceiverCreateSettings: set,
	})
	if err != nil {
		return nil, err
	}

	sharedReceivers[cfg] = r
	return r, nil
}

// registerTracesConsumer registers a traces consumer.
func (r *agentGatewayReceiver) registerTracesConsumer(tc consumer.Traces) {
	r.tracesConsumer = tc
}

// registerMetricsConsumer registers a metrics consumer.
func (r *agentGatewayReceiver) registerMetricsConsumer(mc consumer.Metrics) {
	r.metricsConsumer = mc
}

// registerLogsConsumer registers a logs consumer.
func (r *agentGatewayReceiver) registerLogsConsumer(lc consumer.Logs) {
	r.logsConsumer = lc
}

// Start implements component.Component.
func (r *agentGatewayReceiver) Start(ctx context.Context, host component.Host) error {
	r.startOnce.Do(func() {
		r.startErr = r.start(ctx, host)
	})
	return r.startErr
}

func (r *agentGatewayReceiver) start(ctx context.Context, host component.Host) error {
	// Find extensions
	r.findExtensions(host)

	// Initialize long poll manager if control plane is available
	if err := r.initLongPollManager(ctx); err != nil {
		r.logger.Warn("Failed to initialize long poll manager", zap.Error(err))
	}

	// Start gRPC server if configured
	if err := r.startGRPCServer(ctx, host); err != nil {
		return err
	}

	// Start HTTP server
	if err := r.startHTTPServer(ctx, host); err != nil {
		return errors.Join(err, r.Shutdown(ctx))
	}

	return nil
}

// findExtensions finds required extensions from host.
func (r *agentGatewayReceiver) findExtensions(host component.Host) {
	extensions := host.GetExtensions()

	r.logger.Info("Looking for extensions",
		zap.Int("total_extensions", len(extensions)),
		zap.String("looking_for_controlplane", controlplaneext.Type.String()),
		zap.String("looking_for_arthas", arthastunnelext.Type.String()),
	)

	for id, ext := range extensions {
		r.logger.Debug("Found extension",
			zap.String("id", id.String()),
			zap.String("type", id.Type().String()),
		)

		// Find ControlPlane extension
		if id.Type() == controlplaneext.Type {
			if cp, ok := ext.(controlplaneext.ControlPlane); ok {
				r.controlPlane = cp
				r.logger.Info("Found control plane extension", zap.String("id", id.String()))

				// Also get the concrete extension for internal access
				if cpExt, ok := ext.(*controlplaneext.Extension); ok {
					r.controlPlaneExt = cpExt
				}
			} else {
				r.logger.Warn("Extension type matches but interface assertion failed",
					zap.String("id", id.String()),
					zap.String("ext_type", id.Type().String()),
				)
			}
		}

		// Find ArthasTunnel extension
		if id.Type() == arthastunnelext.Type {
			if at, ok := ext.(arthastunnelext.ArthasTunnel); ok {
				r.arthasTunnel = at
				r.logger.Info("Found arthas tunnel extension", zap.String("id", id.String()))
			}
		}
	}

	// Log warnings if extensions not found but enabled
	if r.config.ControlPlane.Enabled && r.controlPlane == nil {
		r.logger.Warn("Control plane enabled but extension not found, control plane features will be disabled")
	}
	if r.config.ArthasTunnel.Enabled && r.arthasTunnel == nil {
		r.logger.Warn("Arthas tunnel enabled but extension not found, arthas tunnel will be disabled")
	}
}

// startGRPCServer starts the gRPC server for OTLP gRPC protocol.
func (r *agentGatewayReceiver) startGRPCServer(ctx context.Context, host component.Host) error {
	if r.config.GRPC == nil {
		return nil
	}

	var err error
	r.serverGRPC, err = r.config.GRPC.ToServer(ctx, host, r.settings.TelemetrySettings)
	if err != nil {
		return err
	}

	// Register OTLP gRPC services
	if r.tracesConsumer != nil {
		ptraceotlp.RegisterGRPCServer(r.serverGRPC, &traceReceiver{
			consumer: r.tracesConsumer,
			obsrep:   r.obsrepGRPC,
		})
	}

	if r.metricsConsumer != nil {
		pmetricotlp.RegisterGRPCServer(r.serverGRPC, &metricsReceiver{
			consumer: r.metricsConsumer,
			obsrep:   r.obsrepGRPC,
		})
	}

	if r.logsConsumer != nil {
		plogotlp.RegisterGRPCServer(r.serverGRPC, &logsReceiver{
			consumer: r.logsConsumer,
			obsrep:   r.obsrepGRPC,
		})
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

// Shutdown implements component.Component.
func (r *agentGatewayReceiver) Shutdown(ctx context.Context) error {
	var err error
	r.stopOnce.Do(func() {
		err = r.shutdown(ctx)
	})
	return err
}

func (r *agentGatewayReceiver) shutdown(ctx context.Context) error {
	var err error

	// Stop long poll manager
	if r.longPollManager != nil {
		if stopErr := r.longPollManager.Stop(); stopErr != nil {
			r.logger.Warn("Error stopping long poll manager", zap.Error(stopErr))
		}
	}

	// Shutdown HTTP server
	if r.serverHTTP != nil {
		err = errors.Join(err, r.serverHTTP.Shutdown(ctx))
	}

	// Shutdown gRPC server
	if r.serverGRPC != nil {
		r.serverGRPC.GracefulStop()
	}

	r.shutdownWG.Wait()

	// Remove from shared receivers
	sharedReceiversMu.Lock()
	delete(sharedReceivers, r.config)
	sharedReceiversMu.Unlock()

	return err
}

// defaultErrorHandler handles errors for HTTP endpoints.
func defaultErrorHandler(w http.ResponseWriter, _ *http.Request, _ string, statusCode int) {
	http.Error(w, http.StatusText(statusCode), statusCode)
}

// initLongPollManager initializes the long poll manager with handlers.
func (r *agentGatewayReceiver) initLongPollManager(ctx context.Context) error {
	if r.controlPlaneExt == nil {
		r.logger.Debug("Control plane extension not available, skipping long poll manager")
		return nil
	}

	storage := r.controlPlaneExt.GetStorage()
	if storage == nil {
		r.logger.Debug("Storage not available, skipping long poll manager")
		return nil
	}

	// Create long poll manager
	r.longPollManager = longpoll.NewManager(r.logger, longpoll.DefaultManagerConfig())

	// Create and register config handler if Nacos is available
	if storage.HasNacos("default") {
		nacosClient, err := storage.GetDefaultNacosConfigClient()
		if err != nil {
			r.logger.Warn("Failed to get Nacos client for config handler", zap.Error(err))
		} else {
			configHandler := longpoll.NewConfigPollHandler(r.logger, nacosClient)
			if err := r.longPollManager.RegisterHandler(configHandler); err != nil {
				r.logger.Warn("Failed to register config poll handler", zap.Error(err))
			}
		}
	}

	// Create and register task handler if Redis is available
	if storage.HasRedis("default") {
		redisClient, err := storage.GetDefaultRedis()
		if err != nil {
			r.logger.Warn("Failed to get Redis client for task handler", zap.Error(err))
		} else {
			taskHandler := longpoll.NewTaskPollHandler(r.logger, redisClient, "otel:tasks")
			if err := r.longPollManager.RegisterHandler(taskHandler); err != nil {
				r.logger.Warn("Failed to register task poll handler", zap.Error(err))
			}
		}
	}

	// Start the manager
	if err := r.longPollManager.Start(ctx); err != nil {
		return err
	}

	r.logger.Info("Long poll manager initialized",
		zap.Int("handlers", len(r.longPollManager.GetRegisteredTypes())))

	return nil
}
