// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"context"
	"io"
	"net/http"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
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
}

func (r *traceReceiver) Export(ctx context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	td := req.Traces()
	numSpans := td.SpanCount()
	if numSpans == 0 {
		return ptraceotlp.NewExportResponse(), nil
	}

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
}

func (r *metricsReceiver) Export(ctx context.Context, req pmetricotlp.ExportRequest) (pmetricotlp.ExportResponse, error) {
	md := req.Metrics()
	numDataPoints := md.DataPointCount()
	if numDataPoints == 0 {
		return pmetricotlp.NewExportResponse(), nil
	}

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
}

func (r *logsReceiver) Export(ctx context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	ld := req.Logs()
	numRecords := ld.LogRecordCount()
	if numRecords == 0 {
		return plogotlp.NewExportResponse(), nil
	}

	ctx = r.obsrep.StartLogsOp(ctx)
	err := r.consumer.ConsumeLogs(ctx, ld)
	r.obsrep.EndLogsOp(ctx, "protobuf", numRecords, err)

	return plogotlp.NewExportResponse(), err
}

// ===== HTTP Handlers =====

// handleTraces handles HTTP traces requests.
func (r *enhancedOTLPReceiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := ptraceotlp.NewExportRequest()

	switch contentType {
	case pbContentType:
		if err := otlpReq.UnmarshalProto(body); err != nil {
			http.Error(w, "Failed to unmarshal protobuf", http.StatusBadRequest)
			return
		}
	case jsonContentType, "":
		if err := otlpReq.UnmarshalJSON(body); err != nil {
			http.Error(w, "Failed to unmarshal JSON", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "Unsupported content type", http.StatusUnsupportedMediaType)
		return
	}

	td := otlpReq.Traces()
	numSpans := td.SpanCount()
	if numSpans == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.obsrepHTTP.StartTracesOp(req.Context())
	err = r.tracesConsumer.ConsumeTraces(ctx, td)
	r.obsrepHTTP.EndTracesOp(ctx, dataFormatForContentType(contentType), numSpans, err)

	if err != nil {
		http.Error(w, "Failed to consume traces", http.StatusInternalServerError)
		return
	}

	writeResponse(w, contentType, ptraceotlp.NewExportResponse())
}

// handleMetrics handles HTTP metrics requests.
func (r *enhancedOTLPReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := pmetricotlp.NewExportRequest()

	switch contentType {
	case pbContentType:
		if err := otlpReq.UnmarshalProto(body); err != nil {
			http.Error(w, "Failed to unmarshal protobuf", http.StatusBadRequest)
			return
		}
	case jsonContentType, "":
		if err := otlpReq.UnmarshalJSON(body); err != nil {
			http.Error(w, "Failed to unmarshal JSON", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "Unsupported content type", http.StatusUnsupportedMediaType)
		return
	}

	md := otlpReq.Metrics()
	numDataPoints := md.DataPointCount()
	if numDataPoints == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.obsrepHTTP.StartMetricsOp(req.Context())
	err = r.metricsConsumer.ConsumeMetrics(ctx, md)
	r.obsrepHTTP.EndMetricsOp(ctx, dataFormatForContentType(contentType), numDataPoints, err)

	if err != nil {
		http.Error(w, "Failed to consume metrics", http.StatusInternalServerError)
		return
	}

	writeResponse(w, contentType, pmetricotlp.NewExportResponse())
}

// handleLogs handles HTTP logs requests.
func (r *enhancedOTLPReceiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	contentType := req.Header.Get("Content-Type")
	otlpReq := plogotlp.NewExportRequest()

	switch contentType {
	case pbContentType:
		if err := otlpReq.UnmarshalProto(body); err != nil {
			http.Error(w, "Failed to unmarshal protobuf", http.StatusBadRequest)
			return
		}
	case jsonContentType, "":
		if err := otlpReq.UnmarshalJSON(body); err != nil {
			http.Error(w, "Failed to unmarshal JSON", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "Unsupported content type", http.StatusUnsupportedMediaType)
		return
	}

	ld := otlpReq.Logs()
	numRecords := ld.LogRecordCount()
	if numRecords == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.obsrepHTTP.StartLogsOp(req.Context())
	err = r.logsConsumer.ConsumeLogs(ctx, ld)
	r.obsrepHTTP.EndLogsOp(ctx, dataFormatForContentType(contentType), numRecords, err)

	if err != nil {
		http.Error(w, "Failed to consume logs", http.StatusInternalServerError)
		return
	}

	writeResponse(w, contentType, plogotlp.NewExportResponse())
}

// responseMarshaler is an interface for OTLP response types.
type responseMarshaler interface {
	MarshalProto() ([]byte, error)
	MarshalJSON() ([]byte, error)
}

// writeResponse writes the OTLP response in the appropriate format.
func writeResponse(w http.ResponseWriter, contentType string, resp responseMarshaler) {
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
	switch contentType {
	case pbContentType:
		return "protobuf"
	default:
		return "json"
	}
}
