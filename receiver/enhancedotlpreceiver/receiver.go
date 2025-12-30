// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

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

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
)

// enhancedOTLPReceiver implements a receiver that combines OTLP protocol
// with control plane APIs on the same HTTP endpoint.
type enhancedOTLPReceiver struct {
	config   *Config
	settings receiver.Settings
	logger   *zap.Logger

	// gRPC server for OTLP gRPC protocol
	serverGRPC *grpc.Server

	// HTTP server for both OTLP HTTP and Control Plane APIs
	serverHTTP *http.Server

	// Control plane extension reference
	controlPlane controlplaneext.ControlPlane

	// Consumers
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
	sharedReceivers   = make(map[*Config]*enhancedOTLPReceiver)
	sharedReceiversMu sync.Mutex
)

// newEnhancedOTLPReceiver creates a new enhanced OTLP receiver.
func newEnhancedOTLPReceiver(
	set receiver.Settings,
	cfg *Config,
) (*enhancedOTLPReceiver, error) {
	sharedReceiversMu.Lock()
	defer sharedReceiversMu.Unlock()

	// Check for existing receiver with same config
	if r, exists := sharedReceivers[cfg]; exists {
		return r, nil
	}

	r := &enhancedOTLPReceiver{
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
func (r *enhancedOTLPReceiver) registerTracesConsumer(tc consumer.Traces) {
	r.tracesConsumer = tc
}

// registerMetricsConsumer registers a metrics consumer.
func (r *enhancedOTLPReceiver) registerMetricsConsumer(mc consumer.Metrics) {
	r.metricsConsumer = mc
}

// registerLogsConsumer registers a logs consumer.
func (r *enhancedOTLPReceiver) registerLogsConsumer(lc consumer.Logs) {
	r.logsConsumer = lc
}

// Start implements component.Component.
// Uses sync.Once to ensure the receiver is only started once even when
// used in multiple pipelines.
func (r *enhancedOTLPReceiver) Start(ctx context.Context, host component.Host) error {
	r.startOnce.Do(func() {
		r.startErr = r.start(ctx, host)
	})
	return r.startErr
}

func (r *enhancedOTLPReceiver) start(ctx context.Context, host component.Host) error {
	// Find control plane extension
	if r.config.ControlPlane.Enabled {
		if err := r.findControlPlaneExtension(host); err != nil {
			r.logger.Warn("Control plane extension not found, control plane features will be limited",
				zap.Error(err))
		}
	}

	// Start gRPC server
	if err := r.startGRPCServer(ctx, host); err != nil {
		return err
	}

	// Start HTTP server (includes both OTLP and Control Plane)
	if err := r.startHTTPServer(ctx, host); err != nil {
		// Cleanup gRPC server if HTTP fails
		return errors.Join(err, r.Shutdown(ctx))
	}

	return nil
}

// findControlPlaneExtension finds the control plane extension from host.
func (r *enhancedOTLPReceiver) findControlPlaneExtension(host component.Host) error {
	extensions := host.GetExtensions()
	for id, ext := range extensions {
		if id.Type() == controlplaneext.Type {
			if cp, ok := ext.(controlplaneext.ControlPlane); ok {
				r.controlPlane = cp
				r.logger.Info("Found control plane extension", zap.String("id", id.String()))
				return nil
			}
		}
	}
	return errors.New("control plane extension not found")
}

// startGRPCServer starts the gRPC server for OTLP gRPC protocol.
func (r *enhancedOTLPReceiver) startGRPCServer(ctx context.Context, host component.Host) error {
	if r.config.Protocols.GRPC == nil {
		return nil
	}

	var err error
	r.serverGRPC, err = r.config.Protocols.GRPC.ToServer(ctx, host, r.settings.TelemetrySettings)
	if err != nil {
		return err
	}

	// Register OTLP services
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

	r.logger.Info("Starting GRPC server", zap.String("endpoint", r.config.Protocols.GRPC.NetAddr.Endpoint))

	gln, err := r.config.Protocols.GRPC.NetAddr.Listen(ctx)
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

// startHTTPServer starts the HTTP server for both OTLP HTTP and Control Plane APIs.
func (r *enhancedOTLPReceiver) startHTTPServer(ctx context.Context, host component.Host) error {
	if r.config.Protocols.HTTP == nil {
		return nil
	}

	httpMux := http.NewServeMux()

	// Register OTLP HTTP endpoints
	r.registerOTLPHTTPEndpoints(httpMux)

	// Register Control Plane endpoints if enabled
	if r.config.ControlPlane.Enabled {
		r.registerControlPlaneEndpoints(httpMux)
	}

	var err error
	r.serverHTTP, err = r.config.Protocols.HTTP.ServerConfig.ToServer(
		ctx, host, r.settings.TelemetrySettings, httpMux,
		confighttp.WithErrorHandler(otlpErrorHandler),
	)
	if err != nil {
		return err
	}

	r.logger.Info("Starting HTTP server",
		zap.String("endpoint", r.config.Protocols.HTTP.ServerConfig.Endpoint),
		zap.Bool("control_plane_enabled", r.config.ControlPlane.Enabled))

	var hln net.Listener
	hln, err = r.config.Protocols.HTTP.ServerConfig.ToListener(ctx)
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

// registerOTLPHTTPEndpoints registers OTLP HTTP protocol endpoints.
func (r *enhancedOTLPReceiver) registerOTLPHTTPEndpoints(mux *http.ServeMux) {
	if r.tracesConsumer != nil {
		tracesPath := r.config.GetTracesURLPath()
		mux.HandleFunc(tracesPath, r.handleTraces)
		r.logger.Debug("Registered traces endpoint", zap.String("path", tracesPath))
	}

	if r.metricsConsumer != nil {
		metricsPath := r.config.GetMetricsURLPath()
		mux.HandleFunc(metricsPath, r.handleMetrics)
		r.logger.Debug("Registered metrics endpoint", zap.String("path", metricsPath))
	}

	if r.logsConsumer != nil {
		logsPath := r.config.GetLogsURLPath()
		mux.HandleFunc(logsPath, r.handleLogs)
		r.logger.Debug("Registered logs endpoint", zap.String("path", logsPath))
	}
}

// registerControlPlaneEndpoints registers control plane API endpoints.
func (r *enhancedOTLPReceiver) registerControlPlaneEndpoints(mux *http.ServeMux) {
	prefix := r.config.GetControlURLPathPrefix()
	handler := newControlHandler(r.logger, r.controlPlane)

	// Configuration management
	mux.HandleFunc(prefix+"/config", handler.handleConfig)

	// Task management
	mux.HandleFunc(prefix+"/tasks", handler.handleTasks)
	mux.HandleFunc(prefix+"/tasks/cancel", handler.handleTaskCancel)

	// Status and heartbeat
	mux.HandleFunc(prefix+"/status", handler.handleStatus)

	// Agent registration and management
	mux.HandleFunc(prefix+"/register", handler.handleRegister)
	mux.HandleFunc(prefix+"/unregister", handler.handleUnregister)
	mux.HandleFunc(prefix+"/agents", handler.handleAgents)
	mux.HandleFunc(prefix+"/agents/stats", handler.handleAgents)

	// Chunk upload
	mux.HandleFunc(prefix+"/upload-chunk", handler.handleUploadChunk)

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	r.logger.Info("Registered control plane endpoints",
		zap.String("prefix", prefix),
		zap.Strings("paths", []string{
			prefix + "/config",
			prefix + "/tasks",
			prefix + "/tasks/cancel",
			prefix + "/status",
			prefix + "/register",
			prefix + "/unregister",
			prefix + "/agents",
			prefix + "/agents/stats",
			prefix + "/upload-chunk",
			"/health",
		}))
}

// Shutdown implements component.Component.
// Uses sync.Once to ensure the receiver is only shutdown once.
func (r *enhancedOTLPReceiver) Shutdown(ctx context.Context) error {
	var err error
	r.stopOnce.Do(func() {
		err = r.shutdown(ctx)
	})
	return err
}

func (r *enhancedOTLPReceiver) shutdown(ctx context.Context) error {
	var err error

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

// otlpErrorHandler handles errors for OTLP HTTP endpoints.
func otlpErrorHandler(w http.ResponseWriter, _ *http.Request, _ string, statusCode int) {
	http.Error(w, http.StatusText(statusCode), statusCode)
}
