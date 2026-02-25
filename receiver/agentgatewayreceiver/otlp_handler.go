// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"io"
	"net/http"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
)

const (
	pbContentType   = "application/x-protobuf"
	jsonContentType = "application/json"
)

// ===== gRPC Receivers =====

// traceReceiver implements ptraceotlp.GRPCServer for gRPC traces.
type traceReceiver struct {
	ptraceotlp.UnimplementedGRPCServer
	consumer consumer.Traces
	obsrep   *receiverhelper.ObsReport
	recv     *agentGatewayReceiver
}

func (r *traceReceiver) Export(ctx context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	td := req.Traces()
	numSpans := td.SpanCount()
	if numSpans == 0 {
		return ptraceotlp.NewExportResponse(), nil
	}

	r.recv.injectAuthToTraces(td, ctx)

	ctx = r.obsrep.StartTracesOp(ctx)
	err := r.consumer.ConsumeTraces(ctx, td)
	r.obsrep.EndTracesOp(ctx, "protobuf", numSpans, err)

	return ptraceotlp.NewExportResponse(), err
}

// metricsReceiver implements pmetricotlp.GRPCServer for gRPC metrics.
type metricsReceiver struct {
	pmetricotlp.UnimplementedGRPCServer
	consumer consumer.Metrics
	obsrep   *receiverhelper.ObsReport
	recv     *agentGatewayReceiver
}

func (r *metricsReceiver) Export(ctx context.Context, req pmetricotlp.ExportRequest) (pmetricotlp.ExportResponse, error) {
	md := req.Metrics()
	numDataPoints := md.DataPointCount()
	if numDataPoints == 0 {
		return pmetricotlp.NewExportResponse(), nil
	}

	r.recv.injectAuthToMetrics(md, ctx)

	ctx = r.obsrep.StartMetricsOp(ctx)
	err := r.consumer.ConsumeMetrics(ctx, md)
	r.obsrep.EndMetricsOp(ctx, "protobuf", numDataPoints, err)

	return pmetricotlp.NewExportResponse(), err
}

// logsReceiver implements plogotlp.GRPCServer for gRPC logs.
type logsReceiver struct {
	plogotlp.UnimplementedGRPCServer
	consumer consumer.Logs
	obsrep   *receiverhelper.ObsReport
	recv     *agentGatewayReceiver
}

func (r *logsReceiver) Export(ctx context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	ld := req.Logs()
	numRecords := ld.LogRecordCount()
	if numRecords == 0 {
		return plogotlp.NewExportResponse(), nil
	}

	r.recv.injectAuthToLogs(ld, ctx)

	ctx = r.obsrep.StartLogsOp(ctx)
	err := r.consumer.ConsumeLogs(ctx, ld)
	r.obsrep.EndLogsOp(ctx, "protobuf", numRecords, err)

	return plogotlp.NewExportResponse(), err
}

// ===== HTTP Handlers =====

// otlpRequest is an interface for OTLP request types.
type otlpRequest interface {
	UnmarshalProto([]byte) error
	UnmarshalJSON([]byte) error
}

// otlpResponse is an interface for OTLP response types.
type otlpResponse interface {
	MarshalProto() ([]byte, error)
	MarshalJSON() ([]byte, error)
}

// unmarshalOTLPRequest unmarshals OTLP request based on content type.
func unmarshalOTLPRequest(body []byte, contentType string, req otlpRequest) error {
	switch contentType {
	case pbContentType:
		return req.UnmarshalProto(body)
	default:
		return req.UnmarshalJSON(body)
	}
}

// writeOTLPResponse writes OTLP response in the appropriate format.
func writeOTLPResponse(w http.ResponseWriter, contentType string, resp otlpResponse) {
	var respBytes []byte
	var err error

	switch contentType {
	case pbContentType:
		respBytes, err = resp.MarshalProto()
		w.Header().Set("Content-Type", pbContentType)
	default:
		respBytes, err = resp.MarshalJSON()
		w.Header().Set("Content-Type", jsonContentType)
	}

	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

// dataFormatForContentType returns the data format string for observability.
func dataFormatForContentType(contentType string) string {
	if contentType == pbContentType {
		return "protobuf"
	}
	return "json"
}

// handleTraces handles HTTP traces requests.
func (r *agentGatewayReceiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := ptraceotlp.NewExportRequest()

	if err := unmarshalOTLPRequest(body, contentType, &otlpReq); err != nil {
		http.Error(w, "Failed to unmarshal request", http.StatusBadRequest)
		return
	}

	td := otlpReq.Traces()
	numSpans := td.SpanCount()
	if numSpans == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Inject auth info into resource attributes if configured
	r.injectAuthToTraces(td, req.Context())

	ctx := r.obsrepHTTP.StartTracesOp(req.Context())
	err = r.tracesConsumer.ConsumeTraces(ctx, td)
	r.obsrepHTTP.EndTracesOp(ctx, dataFormatForContentType(contentType), numSpans, err)

	if err != nil {
		http.Error(w, "Failed to consume traces", http.StatusInternalServerError)
		return
	}

	writeOTLPResponse(w, contentType, ptraceotlp.NewExportResponse())
}

// handleMetrics handles HTTP metrics requests.
func (r *agentGatewayReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := pmetricotlp.NewExportRequest()

	if err := unmarshalOTLPRequest(body, contentType, &otlpReq); err != nil {
		http.Error(w, "Failed to unmarshal request", http.StatusBadRequest)
		return
	}

	md := otlpReq.Metrics()
	numDataPoints := md.DataPointCount()
	if numDataPoints == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Inject auth info into resource attributes if configured
	r.injectAuthToMetrics(md, req.Context())

	ctx := r.obsrepHTTP.StartMetricsOp(req.Context())
	err = r.metricsConsumer.ConsumeMetrics(ctx, md)
	r.obsrepHTTP.EndMetricsOp(ctx, dataFormatForContentType(contentType), numDataPoints, err)

	if err != nil {
		http.Error(w, "Failed to consume metrics", http.StatusInternalServerError)
		return
	}

	writeOTLPResponse(w, contentType, pmetricotlp.NewExportResponse())
}

// handleLogs handles HTTP logs requests.
func (r *agentGatewayReceiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := plogotlp.NewExportRequest()

	if err := unmarshalOTLPRequest(body, contentType, &otlpReq); err != nil {
		http.Error(w, "Failed to unmarshal request", http.StatusBadRequest)
		return
	}

	ld := otlpReq.Logs()
	numRecords := ld.LogRecordCount()
	if numRecords == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Inject auth info into resource attributes if configured
	r.injectAuthToLogs(ld, req.Context())

	ctx := r.obsrepHTTP.StartLogsOp(req.Context())
	err = r.logsConsumer.ConsumeLogs(ctx, ld)
	r.obsrepHTTP.EndLogsOp(ctx, dataFormatForContentType(contentType), numRecords, err)

	if err != nil {
		http.Error(w, "Failed to consume logs", http.StatusInternalServerError)
		return
	}

	writeOTLPResponse(w, contentType, plogotlp.NewExportResponse())
}

// ===== Auth Info Injection =====

// injectAuthAttrs injects appID and token into resource attributes based on config.
func (r *agentGatewayReceiver) injectAuthAttrs(attrs pcommon.Map, ctx context.Context) {
	if appIDKey := r.config.TokenAuth.InjectAttributeKey; appIDKey != "" {
		if appID := GetAppIDFromContext(ctx); appID != "" {
			attrs.PutStr(appIDKey, appID)
		}
	}
	if tokenKey := r.config.TokenAuth.InjectTokenKey; tokenKey != "" {
		if token := GetTokenFromContext(ctx); token != "" {
			attrs.PutStr(tokenKey, token)
		}
	}
}

// injectAuthToTraces injects auth info into all resource attributes of the traces.
func (r *agentGatewayReceiver) injectAuthToTraces(td ptrace.Traces, ctx context.Context) {
	resourceSpans := td.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		r.injectAuthAttrs(resourceSpans.At(i).Resource().Attributes(), ctx)
	}
}

// injectAuthToMetrics injects auth info into all resource attributes of the metrics.
func (r *agentGatewayReceiver) injectAuthToMetrics(md pmetric.Metrics, ctx context.Context) {
	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		r.injectAuthAttrs(resourceMetrics.At(i).Resource().Attributes(), ctx)
	}
}

// injectAuthToLogs injects auth info into all resource attributes of the logs.
func (r *agentGatewayReceiver) injectAuthToLogs(ld plog.Logs, ctx context.Context) {
	resourceLogs := ld.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		r.injectAuthAttrs(resourceLogs.At(i).Resource().Attributes(), ctx)
	}
}
